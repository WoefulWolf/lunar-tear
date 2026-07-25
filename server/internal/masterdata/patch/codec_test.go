package patch

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

// normaliseInts rewrites every integer flavour to int64 so comparisons test
// values rather than which msgpack width happened to be used.
func normaliseInts(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normaliseInts(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normaliseInts(e)
		}
		return out
	default:
		if n, err := AsInt(v); err == nil {
			return n
		}
		return v
	}
}

// loadRealArchive returns the pristine master-data file pointed at by
// LUNAR_MASTERDATA_BIN. These tests are skipped when it isn't set, since the
// game archive can't be checked into the repo.
func loadRealArchive(t *testing.T) ([]byte, *Archive) {
	t.Helper()
	path := os.Getenv("LUNAR_MASTERDATA_BIN")
	if path == "" {
		t.Skip("LUNAR_MASTERDATA_BIN not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	a, err := Load(raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return raw, a
}

func TestLoadRealArchive(t *testing.T) {
	_, a := loadRealArchive(t)

	if len(a.Order) == 0 {
		t.Fatal("no tables loaded")
	}
	if len(a.Order) != len(a.Tables) {
		t.Errorf("Order has %d entries, Tables has %d", len(a.Order), len(a.Tables))
	}
	for _, name := range a.Order {
		if _, ok := a.Tables[name]; !ok {
			t.Errorf("table %q in Order but not Tables", name)
		}
	}
	t.Logf("loaded %d tables", len(a.Order))
}

// Every table must decode — a table we can't read is one we can't safely rewrite.
func TestDecodeEveryTable(t *testing.T) {
	_, a := loadRealArchive(t)

	var decoded, empty int
	for _, name := range a.Order {
		rows, err := DecodeRows(a.Tables[name])
		if err != nil {
			t.Errorf("decode %q: %v", name, err)
			continue
		}
		if len(rows) == 0 {
			empty++
		}
		decoded++
	}
	t.Logf("decoded %d tables (%d empty)", decoded, empty)
}

// Re-encoding an untouched archive must preserve every table byte-for-byte and
// be stable across repeated round-trips. This is the guarantee the rotation
// logic is built on: tables we don't modify come out exactly as they went in.
func TestRoundTripIsLosslessAndStable(t *testing.T) {
	_, a := loadRealArchive(t)

	out1, err := a.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	b, err := Load(out1)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if len(a.Order) != len(b.Order) {
		t.Fatalf("table count changed: %d -> %d", len(a.Order), len(b.Order))
	}
	for i, name := range a.Order {
		if b.Order[i] != name {
			t.Fatalf("order changed at %d: %q -> %q", i, name, b.Order[i])
		}
		if !bytes.Equal(a.Tables[name], b.Tables[name]) {
			t.Errorf("table %q changed across round-trip (%d -> %d bytes)",
				name, len(a.Tables[name]), len(b.Tables[name]))
		}
	}

	out2, err := b.Bytes()
	if err != nil {
		t.Fatalf("second Bytes: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Errorf("re-encode is not idempotent: %d vs %d bytes", len(out1), len(out2))
	}
}

// Decoding a table and re-encoding it unchanged must preserve the row values.
// This is what a rewritten table goes through, so value drift here would mean
// silently corrupting the tables we touch.
func TestDecodeEncodeRowsPreservesValues(t *testing.T) {
	_, a := loadRealArchive(t)

	// Tables the rotator actually rewrites, plus a couple of large ones.
	for _, name := range []string{"m_mom_banner", "m_mission_term", "m_shop", "m_mission", "m_mission_group"} {
		raw, ok := a.Tables[name]
		if !ok {
			t.Logf("skip %q (not in archive)", name)
			continue
		}
		rows, err := DecodeRows(raw)
		if err != nil {
			t.Errorf("decode %q: %v", name, err)
			continue
		}
		reencoded, err := EncodeRows(rows)
		if err != nil {
			t.Errorf("encode %q: %v", name, err)
			continue
		}
		back, err := DecodeRows(reencoded)
		if err != nil {
			t.Errorf("re-decode %q: %v", name, err)
			continue
		}
		// Compare by value, not by Go type: compact encoding writes positive
		// integers as unsigned msgpack types, so an int64 legitimately comes back
		// as uint64. Readers all go through AsInt, so the width doesn't matter.
		if !reflect.DeepEqual(normaliseInts(rows), normaliseInts(back)) {
			t.Errorf("%q: values changed across decode/encode/decode", name)
			continue
		}
		t.Logf("%q: %d rows, %d -> %d bytes (uncompressed on rewrite)", name, len(rows), len(raw), len(reencoded))
	}
}
