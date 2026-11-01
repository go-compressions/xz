package lzmacore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"runtime"
	"testing"
	"testing/iotest"

	"github.com/go-compressions/xz/internal/xerr"
)

const unknownSize = ^uint64(0)

func aloneStream(lc, lp, pb uint, dict uint32, size uint64, body []byte) []byte {
	h := []byte{byte((pb*5+lp)*9 + lc)}
	h = binary.LittleEndian.AppendUint32(h, dict)
	h = binary.LittleEndian.AppendUint64(h, size)
	return append(h, body...)
}

// readAll drains r with reads of at most bufSize bytes.
func readAll(r io.Reader, bufSize int) ([]byte, error) {
	var out []byte
	buf := make([]byte, bufSize)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

func decodeAlone(stream []byte, max uint64, bufSize int) ([]byte, error) {
	a, err := NewAlone(bytes.NewReader(stream), max)
	if err != nil {
		return nil, err
	}
	return readAll(a, bufSize)
}

// randomScript appends symbols to e until at least n bytes were produced.
func randomScript(e *symEnc, r *rand.Rand, n int, maxDist int) {
	for len(e.hist) < n {
		have := len(e.hist)
		switch k := r.Intn(10); {
		case k < 4 || have == 0:
			e.lit(byte('a' + r.Intn(6)))
		case k < 7:
			d := 1 + r.Intn(16)
			if r.Intn(3) == 0 {
				d = 1 + r.Intn(maxDist)
			}
			d = min(d, have, maxDist)
			l := 2 + r.Intn(10)
			if r.Intn(8) == 0 {
				l = 2 + r.Intn(272)
			}
			e.match(uint32(d), l)
		case k == 7:
			e.shortRep()
		default:
			e.rep(r.Intn(4), 2+r.Intn(30))
		}
	}
}

func TestRandomScripts(t *testing.T) {
	type props struct{ lc, lp, pb uint }
	for _, p := range []props{{3, 0, 2}, {0, 0, 0}, {8, 4, 4}, {4, 0, 0}, {0, 4, 4}, {1, 2, 3}} {
		for _, dict := range []uint32{4096, 10000, 40000, 1 << 20} {
			for _, mode := range []string{"known", "marker", "both"} {
				r := rand.New(rand.NewSource(int64(p.lc*100 + p.lp*10 + p.pb + uint(dict))))
				e := newSymEnc(p.lc, p.lp, p.pb)
				randomScript(e, r, 50000, int(dict))
				size := uint64(len(e.hist))
				if mode != "known" {
					e.marker()
				}
				if mode == "marker" {
					size = unknownSize
				}
				stream := aloneStream(p.lc, p.lp, p.pb, dict, size, e.finish())
				for _, bs := range []int{1, 7, 4096, 1 << 17} {
					got, err := decodeAlone(stream, 1<<30, bs)
					if err != nil {
						t.Fatalf("%v dict=%d %s bs=%d: %v", p, dict, mode, bs, err)
					}
					if !bytes.Equal(got, e.hist) {
						t.Fatalf("%v dict=%d %s bs=%d: output differs", p, dict, mode, bs)
					}
				}
			}
		}
	}
}

// A one-byte-at-a-time source exercises the refill path of the range decoder.
func TestSlowSource(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	e := newSymEnc(3, 0, 2)
	randomScript(e, r, 5000, 4096)
	e.marker()
	stream := aloneStream(3, 0, 2, 4096, unknownSize, e.finish())
	a, err := NewAlone(iotest.OneByteReader(bytes.NewReader(stream)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readAll(a, 1000)
	if err != nil || !bytes.Equal(got, e.hist) {
		t.Fatalf("slow source: err=%v equal=%v", err, bytes.Equal(got, e.hist))
	}
}

func TestEmptyRead(t *testing.T) {
	e := newSymEnc(3, 0, 2)
	e.marker()
	a, err := NewAlone(bytes.NewReader(aloneStream(3, 0, 2, 4096, unknownSize, e.finish())), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := a.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = %d, %v", n, err)
	}
	if _, err := a.Read(make([]byte, 4)); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	if _, err := a.Read(make([]byte, 4)); err != io.EOF {
		t.Fatalf("EOF is not sticky: %v", err)
	}
	if h := a.Header(); h.LC != 3 || h.PB != 2 || h.SizeKnown || h.DecodeDictSize != 4096 {
		t.Fatalf("header %+v", h)
	}
}

func TestHeaderErrors(t *testing.T) {
	if _, err := NewAlone(bytes.NewReader([]byte{1, 2, 3}), 1<<20); !errors.Is(err, xerr.ErrTruncated) {
		t.Fatalf("short header: %v", err)
	}
	h := aloneStream(0, 0, 0, 4096, 0, nil)
	h[0] = 225
	if _, err := NewAlone(bytes.NewReader(h), 1<<20); !errors.Is(err, xerr.ErrCorrupt) {
		t.Fatalf("bad properties: %v", err)
	}
	// The dictionary size is raised to the reference minimum.
	hd, _ := ParseHeader(aloneStream(0, 0, 0, 1, 0, nil))
	if hd.DictSize != 1 || hd.DecodeDictSize != MinDictSize {
		t.Fatalf("dict sizes %d %d", hd.DictSize, hd.DecodeDictSize)
	}
}

// build returns the stream for a script run on a fresh encoder.
func build(lc, lp, pb uint, dict uint32, size uint64, f func(e *symEnc)) []byte {
	e := newSymEnc(lc, lp, pb)
	f(e)
	return aloneStream(lc, lp, pb, dict, size, e.finish())
}

func TestCorruptStreams(t *testing.T) {
	lits := func(e *symEnc, n int) {
		for i := 0; i < n; i++ {
			e.lit(byte('a' + i%3))
		}
	}
	valid := func(extra func(e *symEnc)) []byte {
		return build(3, 0, 2, 4096, 4, func(e *symEnc) { lits(e, 4); extra(e) })
	}
	cases := []struct {
		name   string
		stream []byte
		want   error
	}{
		{"range coder first byte", append([]byte{1}, make([]byte, 4)...), xerr.ErrCorrupt},
		{"range coder code equals range", []byte{0, 0xFF, 0xFF, 0xFF, 0xFF}, xerr.ErrCorrupt},
		{"literal after declared end", valid(func(e *symEnc) { e.lit('x') }), xerr.ErrCorrupt},
		{"match after declared end", valid(func(e *symEnc) { e.match(1, 2) }), xerr.ErrCorrupt},
		{"rep after declared end", valid(func(e *symEnc) { e.rep(0, 2) }), xerr.ErrCorrupt},
		{"marker before declared size", build(3, 0, 2, 4096, 9, func(e *symEnc) { lits(e, 4); e.marker() }), xerr.ErrCorrupt},
		{"repeat in empty window", build(3, 0, 2, 4096, unknownSize, func(e *symEnc) { e.skipCopy = true; e.rep(0, 2); e.marker() }), xerr.ErrCorrupt},
		{"short repeat in empty window", build(3, 0, 2, 4096, unknownSize, func(e *symEnc) { e.skipCopy = true; e.shortRep(); e.marker() }), xerr.ErrCorrupt},
		{"distance beyond data", build(3, 0, 2, 4096, unknownSize, func(e *symEnc) {
			lits(e, 3)
			e.skipCopy = true
			e.match(4, 2)
		}), xerr.ErrCorrupt},
		{"distance beyond dictionary", build(3, 0, 2, 4096, unknownSize, func(e *symEnc) {
			lits(e, 3)
			e.skipCopy = true
			e.match(4097, 2)
		}), xerr.ErrCorrupt},
		{"match longer than declared", build(3, 0, 2, 4096, 6, func(e *symEnc) { lits(e, 4); e.match(1, 5) }), xerr.ErrCorrupt},
		{"truncated", valid(func(e *symEnc) {})[:15], xerr.ErrTruncated},
		{"no end marker", build(3, 0, 2, 4096, unknownSize, func(e *symEnc) { lits(e, 20) }), xerr.ErrTruncated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeAlone(c.stream, 1<<20, 100)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %q, %v; want %v", got, err, c.want)
			}
		})
	}
	// An end marker that leaves the range coder in a non-zero state.
	s := build(3, 0, 2, 4096, unknownSize, func(e *symEnc) { lits(e, 10); e.marker() })
	s[len(s)-1] ^= 0x55
	if _, err := decodeAlone(s, 1<<20, 100); !errors.Is(err, xerr.ErrCorrupt) {
		t.Fatalf("marker with non-zero code: %v", err)
	}
	// Data then error: the bytes decoded before the error are delivered.
	s = build(3, 0, 2, 4096, unknownSize, func(e *symEnc) { lits(e, 10) })
	got, err := decodeAlone(s, 1<<20, 100)
	if !errors.Is(err, xerr.ErrTruncated) || len(got) == 0 {
		t.Fatalf("partial output: %d bytes, %v", len(got), err)
	}
}

// The marker may follow a known size (the reference accepts both).
func TestMarkerAfterKnownSize(t *testing.T) {
	s := build(3, 0, 2, 4096, 4, func(e *symEnc) {
		for i := 0; i < 4; i++ {
			e.lit('q')
		}
		e.marker()
	})
	got, err := decodeAlone(s, 1<<20, 3)
	if err != nil || string(got) != "qqqq" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestMemoryFollowsData(t *testing.T) {
	// A header declaring a 4 GiB dictionary over a few bytes of data.
	s := build(3, 0, 2, 0xFFFFFFFF, 5, func(e *symEnc) {
		e.lit('a')
		e.match(1, 4)
	})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := decodeAlone(s, DefaultTestMax, 100)
	runtime.ReadMemStats(&after)
	if err != nil || string(got) != "aaaaa" {
		t.Fatalf("%q %v", got, err)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 1<<20 {
		t.Fatalf("allocated %d bytes for a 5-byte stream", d)
	}
}

const DefaultTestMax = 256 << 20

func TestCeilingExactness(t *testing.T) {
	const n = 100000
	r := rand.New(rand.NewSource(7))
	e := newSymEnc(3, 0, 2)
	randomScript(e, r, n, 1<<16)
	data := e.hist
	e.marker()
	body := e.finish()
	sizes := map[string]uint64{"known": uint64(len(data)), "marker": unknownSize}
	for name, size := range sizes {
		stream := aloneStream(3, 0, 2, 0xFFFFFFFF, size, body)
		// The script overshoots n slightly: use the exact length.
		l := uint64(len(data))
		if got, err := decodeAlone(stream, l, 4096); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: ceiling == data size must succeed: %d bytes, %v", name, len(got), err)
		}
		got, err := decodeAlone(stream, l-1, 4096)
		if !errors.Is(err, xerr.ErrTooLarge) {
			t.Fatalf("%s: ceiling == size-1 must fail with ErrTooLarge, got %v", name, err)
		}
		if uint64(len(got)) != l-1 {
			t.Fatalf("%s: %d bytes delivered before the refusal, want %d", name, len(got), l-1)
		}
	}
}

func TestWindowBounds(t *testing.T) {
	var w window
	w.reset(100, 50) // ceiling below the dictionary and below the first allocation
	if len(w.buf) != 50 {
		t.Fatalf("initial %d", len(w.buf))
	}
	w.pos = 50
	if err := w.advance(); !errors.Is(err, xerr.ErrTooLarge) {
		t.Fatalf("advance: %v", err)
	}
	w = window{}
	w.reset(1<<20, 1<<30)
	if len(w.buf) != InitialWindow {
		t.Fatalf("initial %d", len(w.buf))
	}
	// Reuse keeps the memory and cuts it to the new dictionary size.
	w.reset(4096, 1<<30)
	if len(w.buf) != 4096 || cap(w.buf) != InitialWindow {
		t.Fatalf("reuse: len %d cap %d", len(w.buf), cap(w.buf))
	}
	w.reset(1<<20, 1<<30)
	if len(w.buf) != InitialWindow {
		t.Fatalf("reuse growth: len %d", len(w.buf))
	}
}

func TestDictSizeFromProp(t *testing.T) {
	for p, want := range map[byte]uint64{0: 4096, 1: 6144, 2: 8192, 18: 2 << 20, 39: 3 << 30, 40: 0xFFFFFFFF} {
		if got, ok := DictSizeFromProp(p); !ok || got != want {
			t.Fatalf("prop %d: %d %v", p, got, ok)
		}
	}
	if _, ok := DictSizeFromProp(41); ok {
		t.Fatal("prop 41 accepted")
	}
}

func TestLiteralProbabilitiesGrowWithUse(t *testing.T) {
	// lc=8 lp=4 declares 6 MiB of literal probabilities; a stream that only
	// uses a few contexts must not allocate them.
	s := build(8, 4, 0, 4096, 3, func(e *symEnc) { e.lit('a'); e.lit('b'); e.lit('c') })
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := decodeAlone(s, 1<<20, 10)
	runtime.ReadMemStats(&after)
	if err != nil || string(got) != "abc" {
		t.Fatalf("%q %v", got, err)
	}
	// The full table is 6 MiB; the tables grow with the highest context used.
	if d := after.TotalAlloc - before.TotalAlloc; d > 3<<20 {
		t.Fatalf("allocated %d bytes", d)
	}
}
