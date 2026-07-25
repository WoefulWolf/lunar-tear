package service

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// listBinWithRevision builds a minimal list.bin-shaped message: field 1 (varint)
// carrying rev, followed by an opaque tail standing in for the rest of the
// Database message.
func listBinWithRevision(rev uint64, tail []byte) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], rev)
	out := append([]byte{0x08}, buf[:n]...)
	return append(out, tail...)
}

func TestRewriteListRevision(t *testing.T) {
	// Stands in for the remainder of the protobuf; it must survive byte-for-byte,
	// since corrupting it would hand the client an unparseable asset list.
	tail := []byte{0x12, 0x04, 't', 'e', 's', 't', 0x18, 0x2a}

	tests := []struct {
		name   string
		oldRev uint64
		newRev int32
	}{
		{"static 817 to an mtime", 817, 1785000000}, // 2-byte varint -> 5-byte
		{"single byte to mtime", 1, 1785000000},     // 1-byte -> 5-byte (grows)
		{"mtime to a small value", 1785000000, 5},   // 5-byte -> 1-byte (shrinks)
		{"same length replacement", 817, 900},       // 2-byte -> 2-byte
		{"zero revision", 0, 1785000000},            // smallest old varint
		{"max int32", 1, 2147483647},                // largest positive newRev
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := listBinWithRevision(tt.oldRev, tail)
			out, ok := rewriteListRevision(in, tt.newRev)
			if !ok {
				t.Fatal("ok = false, want true")
			}
			if out[0] != 0x08 {
				t.Errorf("field tag = %#x, want 0x08", out[0])
			}
			got, n := binary.Uvarint(out[1:])
			if n <= 0 {
				t.Fatalf("revision varint is unreadable (n=%d)", n)
			}
			if got != uint64(uint32(tt.newRev)) {
				t.Errorf("revision = %d, want %d", got, uint32(tt.newRev))
			}
			if rest := out[1+n:]; !bytes.Equal(rest, tail) {
				t.Errorf("tail corrupted:\n got %x\nwant %x", rest, tail)
			}
		})
	}
}

// A message that isn't "field 1, varint" must be passed through untouched rather
// than rewritten into something the client can't parse.
func TestRewriteListRevisionLeavesMalformedInputAlone(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"single byte", []byte{0x08}},
		{"different field number", []byte{0x12, 0x01, 0xff}},
		{"field 1 but not varint wire type", []byte{0x0a, 0x01, 0xff}},
		{"varint never terminates", []byte{0x08, 0x80, 0x80}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]byte(nil), tt.in...)
			out, ok := rewriteListRevision(tt.in, 1785000000)
			if ok {
				t.Error("ok = true, want false for malformed input")
			}
			if !bytes.Equal(out, original) {
				t.Errorf("data was modified:\n got %x\nwant %x", out, original)
			}
		})
	}
}

// The whole point: a changed file must present a different revision to the client.
func TestRewriteListRevisionDistinctMtimesGiveDistinctRevisions(t *testing.T) {
	tail := []byte{0x12, 0x02, 'h', 'i'}
	in := listBinWithRevision(817, tail)

	first, ok1 := rewriteListRevision(in, 1785000000)
	second, ok2 := rewriteListRevision(in, 1785000001)
	if !ok1 || !ok2 {
		t.Fatal("expected both rewrites to succeed")
	}
	if bytes.Equal(first, second) {
		t.Error("different mtimes produced identical bytes — clients would not re-sync")
	}
}
