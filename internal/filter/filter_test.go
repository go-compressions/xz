package filter

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"

	"github.com/go-compressions/xz/internal/corpus"
)

func codes() map[string]func() Code {
	return map[string]func() Code{
		"x86":      func() Code { return NewX86().Code },
		"ppc":      func() Code { return PowerPC },
		"ia64":     func() Code { return IA64 },
		"arm":      func() Code { return ARM },
		"armthumb": func() Code { return ARMThumb },
		"sparc":    func() Code { return SPARC },
		"arm64":    func() Code { return ARM64 },
		"riscv":    func() Code { return RISCV },
	}
}

var corpusKind = map[string]string{
	"x86": "x86", "ppc": "ppc", "ia64": "ia64", "arm": "arm",
	"armthumb": "armthumb", "sparc": "sparc", "arm64": "arm64", "riscv": "riscv",
}

// The output of a filter does not depend on how the input is cut into reads.
func TestChunkIndependence(t *testing.T) {
	for name, mk := range codes() {
		in := corpus.Make(corpusKind[name], 50000, 9)
		whole, err := io.ReadAll(NewSimple(bytes.NewReader(in), mk(), 0))
		if err != nil {
			t.Fatal(name, err)
		}
		if len(whole) != len(in) {
			t.Fatalf("%s: %d bytes in, %d out", name, len(in), len(whole))
		}
		if bytes.Equal(whole, in) {
			t.Fatalf("%s: the filter changed nothing: the corpus does not exercise it", name)
		}
		for _, wrap := range []func(io.Reader) io.Reader{iotest.OneByteReader, iotest.HalfReader, iotest.DataErrReader} {
			got, err := io.ReadAll(iotest.OneByteReader(NewSimple(wrap(bytes.NewReader(in)), mk(), 0)))
			if err != nil || !bytes.Equal(got, whole) {
				t.Fatalf("%s: chunked output differs (%v)", name, err)
			}
		}
		// A start offset changes the result for position-dependent filters.
		off, _ := io.ReadAll(NewSimple(bytes.NewReader(in), mk(), 4096))
		if bytes.Equal(off, whole) {
			t.Fatalf("%s: start offset ignored", name)
		}
	}
}

func TestShortInputPassesThrough(t *testing.T) {
	for name, mk := range codes() {
		for n := 0; n < 20; n++ {
			in := bytes.Repeat([]byte{0xE8}, n)
			if n >= 5 && name == "x86" {
				continue // an E8 followed by a valid operand is converted
			}
			got, err := io.ReadAll(NewSimple(bytes.NewReader(in), mk(), 0))
			if err != nil || len(got) != n {
				t.Fatalf("%s n=%d: %d bytes, %v", name, n, len(got), err)
			}
		}
		if got := mk()(0, nil); got != 0 {
			t.Fatalf("%s: empty buffer processed %d", name, got)
		}
	}
	if n := NewX86().Code(0, make([]byte, 4)); n != 0 {
		t.Fatalf("x86 on 4 bytes: %d", n)
	}
}

// Dense E8/E9 and 00/FF bytes reach the x86 filter's prev_mask logic: the
// output must still not depend on how the input is cut into reads.
func TestX86Dense(t *testing.T) {
	alphabet := []byte{0xE8, 0xE9, 0x00, 0xFF, 0x01}
	for seed := uint64(1); seed < 300; seed++ {
		in := make([]byte, 200)
		x := seed * 0x9E3779B97F4A7C15
		for i := range in {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			in[i] = alphabet[x%uint64(len(alphabet))]
		}
		whole, err := io.ReadAll(NewSimple(bytes.NewReader(in), NewX86().Code, 0))
		if err != nil {
			t.Fatal(err)
		}
		cut, err := io.ReadAll(NewSimple(iotest.OneByteReader(bytes.NewReader(in)), NewX86().Code, 0))
		if err != nil || !bytes.Equal(whole, cut) {
			t.Fatalf("seed %d: chunked output differs (%v)", seed, err)
		}
	}
}

func TestSimpleErrors(t *testing.T) {
	boom := errors.New("boom")
	in := corpus.Make("arm", 1000, 1)
	r := NewSimple(io.MultiReader(bytes.NewReader(in), iotest.ErrReader(boom)), ARM, 0)
	got, err := io.ReadAll(r)
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if len(got) == 0 || len(got) > len(in) {
		t.Fatalf("delivered %d of %d bytes before the error", len(got), len(in))
	}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, boom) {
		t.Fatalf("not sticky: %v", err)
	}
	if n, err := r.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = %d, %v", n, err)
	}
}

func TestDelta(t *testing.T) {
	for _, dist := range []int{1, 2, 3, 255, 256} {
		plain := corpus.Make("audio", 5000, 2)
		enc := make([]byte, len(plain))
		for i := range plain {
			var prev byte
			if i >= dist {
				prev = plain[i-dist]
			}
			enc[i] = plain[i] - prev
		}
		for _, wrap := range []func(io.Reader) io.Reader{func(r io.Reader) io.Reader { return r }, iotest.OneByteReader} {
			got, err := io.ReadAll(NewDelta(wrap(bytes.NewReader(enc)), dist))
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("dist %d: %v", dist, err)
			}
		}
	}
}
