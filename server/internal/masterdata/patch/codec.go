// Package patch reads, rewrites and re-encrypts the master-data archive
// (database.bin.e).
//
// Archive layout, after AES-CBC decryption and PKCS#7 unpadding:
//
//	[ msgpack map: table name -> [offset, length] ][ concatenated table blobs ]
//
// Each table blob is either a plain msgpack array of rows, or an msgpack ext
// (code 99) wrapping an LZ4 block. Rows are positional arrays — column meaning
// comes from schemas.json, not from the data.
package patch

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/pierrec/lz4/v4"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	aesKeyHex  = "36436230313332314545356536624265"
	aesIVHex   = "45666341656634434165356536446141"
	lz4ExtCode = int8(99)
	blockSize  = 16
)

// Archive is a decoded master-data file. Order fixes the table layout of a
// rewritten archive, so regenerating from the same input twice produces the
// same bytes.
type Archive struct {
	Order  []string
	Tables map[string][]byte // raw blob bytes, exactly as stored
}

// Load decrypts an archive and splits it into its table-of-contents and the
// raw bytes of each table.
func Load(data []byte) (*Archive, error) {
	dec, err := decrypt(data)
	if err != nil {
		return nil, err
	}
	toc, blob, err := parseHeader(dec)
	if err != nil {
		return nil, err
	}

	order := make([]string, 0, len(toc))
	for name := range toc {
		order = append(order, name)
	}
	// Original offset order; ties broken by name so the result is deterministic.
	sort.Slice(order, func(i, j int) bool {
		a, b := toc[order[i]], toc[order[j]]
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		return order[i] < order[j]
	})

	tables := make(map[string][]byte, len(toc))
	for name, ol := range toc {
		off, length := ol[0], ol[1]
		if off < 0 || length < 0 || off+length > len(blob) {
			return nil, fmt.Errorf("table %q: [%d,%d] out of range for blob of %d", name, off, length, len(blob))
		}
		tables[name] = blob[off : off+length]
	}
	return &Archive{Order: order, Tables: tables}, nil
}

// Bytes re-assembles the archive and encrypts it, ready to write to disk.
func (a *Archive) Bytes() ([]byte, error) {
	var blob bytes.Buffer
	type entry struct {
		name        string
		off, length int
	}
	entries := make([]entry, 0, len(a.Order))
	for _, name := range a.Order {
		data, ok := a.Tables[name]
		if !ok {
			return nil, fmt.Errorf("table %q in Order but missing from Tables", name)
		}
		entries = append(entries, entry{name, blob.Len(), len(data)})
		blob.Write(data)
	}

	// Written entry by entry rather than from a map: Go randomises map
	// iteration, which would make the output differ run to run.
	var out bytes.Buffer
	enc := msgpack.NewEncoder(&out)
	if err := enc.EncodeMapLen(len(entries)); err != nil {
		return nil, fmt.Errorf("encode header len: %w", err)
	}
	for _, e := range entries {
		if err := enc.EncodeString(e.name); err != nil {
			return nil, fmt.Errorf("encode header key %q: %w", e.name, err)
		}
		if err := enc.EncodeArrayLen(2); err != nil {
			return nil, err
		}
		if err := enc.EncodeInt(int64(e.off)); err != nil {
			return nil, err
		}
		if err := enc.EncodeInt(int64(e.length)); err != nil {
			return nil, err
		}
	}
	out.Write(blob.Bytes())

	return encrypt(out.Bytes())
}

// DecodeRows decodes one table blob into positional rows, transparently
// unwrapping the LZ4 ext container when present.
func DecodeRows(raw []byte) ([]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	plain, err := maybeDecompress(raw)
	if err != nil {
		return nil, err
	}

	d := msgpack.NewDecoder(bytes.NewReader(plain))
	d.UseLooseInterfaceDecoding(true)
	var rows []any
	if err := d.Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode rows: %w", err)
	}
	return rows, nil
}

// EncodeRows writes rows back as a plain msgpack array. Rewritten tables are
// stored uncompressed; only untouched tables keep their original (possibly
// LZ4) bytes.
func EncodeRows(rows []any) ([]byte, error) {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.SetSortMapKeys(true)
	// Smallest width that fits each value. Without this every integer is
	// written full-width and the archive grows by about 6%.
	enc.UseCompactInts(true)
	if err := enc.Encode(rows); err != nil {
		return nil, fmt.Errorf("encode rows: %w", err)
	}
	return buf.Bytes(), nil
}

func maybeDecompress(raw []byte) ([]byte, error) {
	d := msgpack.NewDecoder(bytes.NewReader(raw))
	code, extData, err := decodeExt(d)
	if err != nil || code != lz4ExtCode {
		return raw, nil // plain msgpack array
	}
	size, block, err := readLZ4ExtHeader(extData)
	if err != nil {
		return nil, fmt.Errorf("lz4 ext header: %w", err)
	}
	if size < 0 {
		return nil, fmt.Errorf("negative uncompressed size %d", size)
	}
	buf := make([]byte, size)
	n, err := lz4.UncompressBlock(block, buf)
	if err != nil {
		return nil, fmt.Errorf("lz4 decompress: %w", err)
	}
	return buf[:n], nil
}

func decodeExt(dec *msgpack.Decoder) (int8, []byte, error) {
	var ext msgpack.RawMessage
	if err := dec.Decode(&ext); err != nil {
		return 0, nil, err
	}
	inner := msgpack.NewDecoder(bytes.NewReader(ext))
	id, length, err := inner.DecodeExtHeader()
	if err != nil {
		return 0, nil, err
	}
	data := make([]byte, length)
	if _, err := inner.Buffered().Read(data); err != nil {
		return 0, nil, fmt.Errorf("read ext data: %w", err)
	}
	return id, data, nil
}

// readLZ4ExtHeader strips the msgpack-encoded uncompressed-size prefix that
// precedes the LZ4 block inside the ext payload.
func readLZ4ExtHeader(data []byte) (int, []byte, error) {
	if len(data) == 0 {
		return 0, nil, fmt.Errorf("empty ext data")
	}
	switch tag := data[0]; {
	case tag == 0xd2: // int32
		if len(data) < 5 {
			return 0, nil, fmt.Errorf("truncated int32 size")
		}
		return int(int32(binary.BigEndian.Uint32(data[1:5]))), data[5:], nil
	case tag == 0xce: // uint32
		if len(data) < 5 {
			return 0, nil, fmt.Errorf("truncated uint32 size")
		}
		return int(binary.BigEndian.Uint32(data[1:5])), data[5:], nil
	case tag < 0x80: // positive fixint
		return int(tag), data[1:], nil
	default:
		return 0, nil, fmt.Errorf("unsupported size tag %#x", tag)
	}
}

func parseHeader(data []byte) (map[string][2]int, []byte, error) {
	r := bytes.NewReader(data)
	dec := msgpack.NewDecoder(r)
	dec.UseLooseInterfaceDecoding(true)

	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("decode header: %w", err)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("header is %T, want map", raw)
	}

	toc := make(map[string][2]int, len(m))
	for name, v := range m {
		arr, ok := v.([]any)
		if !ok || len(arr) != 2 {
			return nil, nil, fmt.Errorf("table %q: want [offset,length], got %T", name, v)
		}
		off, err := AsInt(arr[0])
		if err != nil {
			return nil, nil, fmt.Errorf("table %q offset: %w", name, err)
		}
		length, err := AsInt(arr[1])
		if err != nil {
			return nil, nil, fmt.Errorf("table %q length: %w", name, err)
		}
		toc[name] = [2]int{int(off), int(length)}
	}
	consumed := len(data) - r.Len()
	return toc, data[consumed:], nil
}

// AsInt normalises any msgpack integer flavour to int64. Returns an error for
// non-integers, which callers use to skip rows whose column isn't a timestamp.
func AsInt(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint:
		return int64(n), nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("not an integer: %T", v)
	}
}

func decrypt(data []byte) ([]byte, error) {
	block, iv, err := cipherBlock()
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of %d", len(data), blockSize)
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return pkcs7Unpad(out)
}

func encrypt(data []byte) ([]byte, error) {
	block, iv, err := cipherBlock()
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(data)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func cipherBlock() (cipher.Block, []byte, error) {
	key, err := hex.DecodeString(aesKeyHex)
	if err != nil {
		return nil, nil, fmt.Errorf("decode key: %w", err)
	}
	iv, err := hex.DecodeString(aesIVHex)
	if err != nil {
		return nil, nil, fmt.Errorf("decode iv: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("new cipher: %w", err)
	}
	return block, iv, nil
}

func pkcs7Pad(data []byte) []byte {
	n := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	n := int(data[len(data)-1])
	if n == 0 || n > blockSize || n > len(data) {
		return nil, fmt.Errorf("invalid padding length %d", n)
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, fmt.Errorf("invalid padding byte")
		}
	}
	return data[:len(data)-n], nil
}
