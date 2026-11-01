package lzma_test

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"
	"testing/iotest"

	ulzma "github.com/ulikunitz/xz/lzma"

	"github.com/go-compressions/xz/internal/corpus"
	"github.com/go-compressions/xz/lzma"
)

// rawLZMA1 returns the coding of data as an lzip member holds it: no .lzma
// header, an end marker, lc=3 lp=0 pb=2.
func rawLZMA1(t testing.TB, data []byte, dictCap int) []byte {
	t.Helper()
	enc := encodeAlone(t, data, ulzma.WriterConfig{EOSMarker: true, DictCap: dictCap})
	return enc[13:]
}

func TestReader1(t *testing.T) {
	tail := []byte("TRAILER-AND-NEXT-MEMBER")
	for _, kind := range []string{"text", "mixed", "random"} {
		data := corpus.Make(kind, 100000, 5)
		for _, dict := range []int{4096, 1 << 20, 16 << 20} {
			raw := rawLZMA1(t, data, dict)
			for _, slow := range []bool{false, true} {
				var src io.Reader = bytes.NewReader(append(append([]byte{}, raw...), tail...))
				if slow {
					src = iotest.OneByteReader(src)
				}
				r, err := lzma.Reader1Config{LC: 3, LP: 0, PB: 2, DictCap: dict}.NewReader1(src)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(r)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("%s dict=%d slow=%v: err=%v equal=%v", kind, dict, slow, err, bytes.Equal(got, data))
				}
				// What the decoder read and did not use is what follows the
				// stream, whatever the source's chunking.
				rest := append([]byte{}, r.Unconsumed()...)
				if more, _ := io.ReadAll(src); !bytes.Equal(append(rest, more...), tail) {
					t.Fatalf("%s dict=%d slow=%v: tail %q + %q", kind, dict, slow, rest, more)
				}
			}
		}
	}
}

func TestReader1Config(t *testing.T) {
	cfgs := []lzma.Reader1Config{{LC: 9}, {LP: 5}, {PB: 5}, {DictCap: -1}, {MaxDictionary: -1}}
	// A dictionary above 4 GiB - 1 can only be written where int is 64 bits.
	if shift := uint(40); 1<<shift > 0 {
		cfgs = append(cfgs, lzma.Reader1Config{DictCap: 1 << shift})
	}
	for _, cfg := range cfgs {
		if _, err := cfg.NewReader1(bytes.NewReader(nil)); err == nil {
			t.Fatalf("%+v accepted", cfg)
		}
	}
}

func TestReader1Truncated(t *testing.T) {
	raw := rawLZMA1(t, corpus.Make("text", 50000, 2), 1<<20)
	for _, n := range []int{0, 3, len(raw) / 2, len(raw) - 1} {
		r, err := lzma.Reader1Config{LC: 3, PB: 2, DictCap: 1 << 20}.NewReader1(bytes.NewReader(raw[:n]))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(r); !errors.Is(err, lzma.ErrTruncated) && !errors.Is(err, lzma.ErrCorrupt) {
			t.Fatalf("prefix %d: %v", n, err)
		}
	}
}

// A dictionary of 4 GiB - 1 declared over a few bytes of data costs kilobytes,
// and the ceiling is honoured.
func TestReader1HugeDictionary(t *testing.T) {
	data := corpus.Make("text", 300, 1)
	raw := rawLZMA1(t, data, 4096)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	r, err := lzma.Reader1Config{LC: 3, PB: 2, DictCap: 3 << 29}.NewReader1(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	runtime.ReadMemStats(&after)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("err=%v equal=%v", err, bytes.Equal(got, data))
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 1<<20 {
		t.Fatalf("allocated %d bytes", d)
	}

	big := corpus.Make("random", 100000, 1)
	rawBig := rawLZMA1(t, big, 1<<20)
	r, err = lzma.Reader1Config{LC: 3, PB: 2, DictCap: 1 << 20, MaxDictionary: 50000}.NewReader1(bytes.NewReader(rawBig))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, lzma.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}
