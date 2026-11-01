package xz_test

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/go-compressions/xz"
	"github.com/go-compressions/xz/internal/corpus"
)

// Bomb tests assert a refusal or a tiny allocation. They must be run under
// GOMEMLIMIT and ulimit -v (see .github/workflows/ci.yml), never against the
// memory of the whole host: a regression would otherwise allocate gigabytes.

// totalAlloc runs f and returns the bytes it allocated.
func totalAlloc(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// withDictProp rewrites the LZMA2 dictionary property of the first block of
// a stream made by the xz command line tool (no sizes in the block header,
// one filter) and fixes the header CRC.
func withDictProp(t testing.TB, stream []byte, prop byte) []byte {
	t.Helper()
	b := append([]byte(nil), stream...)
	if b[14] != 0x21 || b[15] != 1 {
		t.Fatalf("unexpected block header layout: % x", b[12:20])
	}
	b[16] = prop
	size := (int(b[12]) + 1) * 4
	refix(b[12 : 12+size])
	return b
}

// Dictionary properties 36..40 declare 1 GiB, 1.5 GiB, 2 GiB, 3 GiB and
// 4 GiB - 1.
func TestBombDeclaredDictionary(t *testing.T) {
	for _, prop := range []byte{36, 37, 38, 39, 40} {
		// Stored data in a block that declares a gigantic dictionary.
		s := buildStream(4, simpleBlock(4, plain, prop))
		var got []byte
		var err error
		alloc := totalAlloc(func() { got, err = decode(s, xz.ReaderConfig{}) })
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("prop %d: %v", prop, err)
		}
		if alloc > 1<<20 {
			t.Fatalf("prop %d: %d bytes allocated for %d bytes of data", prop, alloc, len(plain))
		}
	}
	// LZMA-compressed data (made by xz) whose header is patched to declare
	// a gigantic dictionary: same output, kilobytes of memory.
	want := corpus.Make("text", 200000, 1)
	orig := readFixture(t, "text-default.xz")
	for _, prop := range []byte{37, 40} {
		s := withDictProp(t, orig, prop)
		var got []byte
		var err error
		alloc := totalAlloc(func() { got, err = decode(s, xz.ReaderConfig{}) })
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("patched prop %d: %v", prop, err)
		}
		// The window grows to the data (200 KB -> 256 KiB buffer).
		if alloc > 2<<20 {
			t.Fatalf("patched prop %d: %d bytes allocated for %d bytes of output", prop, alloc, len(want))
		}
	}
}

func TestBombCeiling(t *testing.T) {
	data := corpus.Make("random", 3<<20, 1)
	s := buildStream(1, simpleBlock(1, data, 40)) // declares 4 GiB - 1
	const max = 1 << 20
	var err error
	alloc := totalAlloc(func() {
		r, rerr := xz.ReaderConfig{MaxDictionary: max}.NewReader(bytes.NewReader(s))
		if rerr != nil {
			t.Fatal(rerr)
		}
		_, err = io.Copy(io.Discard, r)
	})
	if !errors.Is(err, xz.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	// The window grew 16 KiB, 32 KiB, ... up to max and then stopped; the
	// stream itself (3 MiB) is in memory already.
	if alloc > 3*max {
		t.Fatalf("%d bytes allocated for a ceiling of %d", alloc, max)
	}
	// ErrTooLarge is also an error a caller can tell from corruption.
	if errors.Is(err, xz.ErrCorrupt) {
		t.Fatal("ErrTooLarge must not be ErrCorrupt")
	}
}

func TestCeilingExactness(t *testing.T) {
	data := corpus.Make("random", 100000, 2)
	for _, prop := range []byte{40, 18} { // 4 GiB - 1, and 2 MiB
		s := buildStream(1, simpleBlock(1, data, prop))
		got, err := decode(s, xz.ReaderConfig{MaxDictionary: len(data)})
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("prop %d: ceiling == data size must succeed: %v", prop, err)
		}
		got, err = decode(s, xz.ReaderConfig{MaxDictionary: len(data) - 1})
		if !errors.Is(err, xz.ErrTooLarge) {
			t.Fatalf("prop %d: ceiling == data size - 1 must fail with ErrTooLarge: %v", prop, err)
		}
		if len(got) != len(data)-1 {
			t.Fatalf("prop %d: %d bytes delivered before the refusal, want %d", prop, len(got), len(data)-1)
		}
	}
	// A dictionary smaller than the data wraps instead: the ceiling does
	// not apply to it.
	s := buildStream(1, simpleBlock(1, data, 6)) // 32 KiB
	if got, err := decode(s, xz.ReaderConfig{MaxDictionary: 32 << 10}); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("wrapping window: %v", err)
	}
}

func TestBombHeaderSizes(t *testing.T) {
	// Sizes in block headers and the index are checked against what arrives,
	// never used to size a buffer.
	huge := uint64(1<<62) + 5
	comp := stored(plain)
	c, u := huge, huge
	s := buildStream(1, makeBlock(1, blockHeader(&c, &u, lzma2(40)), comp, plain))
	var err error
	alloc := totalAlloc(func() { _, err = decode(s, xz.ReaderConfig{}) })
	if !errors.Is(err, xz.ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
	if alloc > 1<<20 {
		t.Fatalf("%d bytes allocated", alloc)
	}
	// An index that claims 2^62 records.
	p := partsOf(1, plain)
	p.index = append([]byte{0}, vli(1<<62)...)
	alloc = totalAlloc(func() { _, err = decode(p.join(), xz.ReaderConfig{}) })
	if !errors.Is(err, xz.ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
	if alloc > 1<<20 {
		t.Fatalf("%d bytes allocated", alloc)
	}
	// An LZMA2 chunk that claims 64 KiB of data and delivers 10 bytes.
	stream := append(append([]byte(nil), partsOf(1, plain).header...), blockHeader(nil, nil, lzma2(40))...)
	stream = append(stream, 0xE0, 0, 9, 0xFF, 0xFF, 0x5D, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	alloc = totalAlloc(func() { _, err = decode(stream, xz.ReaderConfig{}) })
	if !errors.Is(err, xz.ErrTruncated) {
		t.Fatalf("got %v", err)
	}
	if alloc > 1<<20 {
		t.Fatalf("%d bytes allocated", alloc)
	}
}
