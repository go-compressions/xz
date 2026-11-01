package lzmacore

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"runtime"
	"testing"
	"testing/iotest"

	"github.com/go-compressions/xz/internal/xerr"
)

// chunk reset levels of the LZMA2 control byte.
const (
	resetNone  = 0
	resetState = 1
	resetProps = 2
	resetDict  = 3
)

// l2Builder assembles an LZMA2 stream from symbol-level chunks.
type l2Builder struct {
	out  []byte // the stream
	want []byte // the expected decoded output
	e    *symEnc
}

func propsByte(lc, lp, pb uint) byte { return byte((pb*5+lp)*9 + lc) }

// lzma applies f to the encoder and emits one LZMA chunk with the given
// reset level. For levels above resetState the properties are lc, lp, pb.
func (b *l2Builder) lzma(level int, lc, lp, pb uint, f func(e *symEnc)) {
	if b.e == nil || level >= resetProps {
		old := b.e
		b.e = newSymEnc(lc, lp, pb)
		if old != nil && level < resetDict {
			b.e.hist, b.e.posBase = old.hist, old.posBase
		}
	} else if level == resetState {
		b.e.ResetState()
	}
	e := b.e
	if level == resetDict {
		e.hist, e.posBase = nil, 0
	}
	e.rc = newRCEnc()
	e.base = len(e.hist)
	f(e)
	data := e.finish()
	usize := len(e.hist) - e.base
	b.want = append(b.want, e.data()...)
	ctl := byte(0x80 | level<<5 | (usize-1)>>16)
	b.out = append(b.out, ctl, byte((usize-1)>>8), byte(usize-1), byte((len(data)-1)>>8), byte(len(data)-1))
	if level >= resetProps {
		b.out = append(b.out, propsByte(lc, lp, pb))
	}
	b.out = append(b.out, data...)
}

// raw emits an uncompressed chunk; dictReset selects control 1.
func (b *l2Builder) raw(dictReset bool, data []byte) {
	ctl := byte(2)
	if dictReset {
		ctl = 1
		if b.e != nil {
			b.e.hist, b.e.posBase = nil, 0
		}
	}
	if b.e != nil {
		b.e.hist = append(b.e.hist, data...)
	}
	b.want = append(b.want, data...)
	b.out = append(b.out, ctl, byte((len(data)-1)>>8), byte(len(data)-1))
	b.out = append(b.out, data...)
}

func (b *l2Builder) end() []byte { return append(b.out[:len(b.out):len(b.out)], 0) }

func decodeL2(stream []byte, dict, max uint64, bufSize int, allowEOF bool) ([]byte, error) {
	r := NewLZMA2(bytes.NewReader(stream), dict, max)
	r.SetAllowEOF(allowEOF)
	return readAll(r, bufSize)
}

func lits(e *symEnc, n int) {
	for i := 0; i < n; i++ {
		e.lit(byte('a' + i%5))
	}
}

func TestLZMA2Chunks(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	var b l2Builder
	b.lzma(resetDict, 3, 0, 2, func(e *symEnc) { randomScript(e, r, 3000, 4096) })
	b.lzma(resetNone, 0, 0, 0, func(e *symEnc) { randomScript(e, r, len(e.hist)+500, 4096) })
	b.raw(false, []byte("stored chunk in the middle"))
	b.lzma(resetState, 0, 0, 0, func(e *symEnc) { randomScript(e, r, len(e.hist)+500, 4096) })
	b.lzma(resetProps, 0, 2, 1, func(e *symEnc) { randomScript(e, r, len(e.hist)+500, 4096) })
	b.lzma(resetProps, 4, 0, 0, func(e *symEnc) { randomScript(e, r, len(e.hist)+500, 4096) })
	b.raw(true, []byte("dictionary reset by a stored chunk"))
	b.lzma(resetProps, 1, 3, 4, func(e *symEnc) { randomScript(e, r, len(e.hist)+500, 4096) })
	b.lzma(resetDict, 3, 0, 2, func(e *symEnc) { randomScript(e, r, 800, 4096) })
	stream := b.end()
	for _, bs := range []int{1, 100, 1 << 16} {
		got, err := decodeL2(stream, 4096, 1<<20, bs, false)
		if err != nil || !bytes.Equal(got, b.want) {
			t.Fatalf("bs=%d: err=%v equal=%v (%d vs %d)", bs, err, bytes.Equal(got, b.want), len(got), len(b.want))
		}
	}
	// The same stream from a source that returns one byte per call.
	got, err := readAll(NewLZMA2(iotest.OneByteReader(bytes.NewReader(stream)), 4096, 1<<20), 5000)
	if err != nil || !bytes.Equal(got, b.want) {
		t.Fatalf("one-byte source: %v", err)
	}
	if n, err := NewLZMA2(bytes.NewReader(stream), 4096, 1<<20).Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = %d, %v", n, err)
	}
}

func TestLZMA2WindowWrapsInRawChunks(t *testing.T) {
	data := make([]byte, 10000)
	rand.New(rand.NewSource(5)).Read(data)
	var b l2Builder
	b.raw(true, data[:6000])
	b.raw(false, data[6000:])
	got, err := decodeL2(b.end(), 4096, 1<<20, 777, false)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("err=%v equal=%v", err, bytes.Equal(got, data))
	}
}

func TestLZMA2EndOfStream(t *testing.T) {
	var b l2Builder
	b.raw(true, []byte("abc"))
	noEnd := b.out
	if got, err := decodeL2(noEnd, 4096, 1<<20, 10, true); err != nil || string(got) != "abc" {
		t.Fatalf("allowEOF: %q %v", got, err)
	}
	if _, err := decodeL2(noEnd, 4096, 1<<20, 10, false); !errors.Is(err, xerr.ErrTruncated) {
		t.Fatalf("strict: %v", err)
	}
	// Reading again after the end keeps returning EOF; Reset reuses the decoder.
	r := NewLZMA2(bytes.NewReader(b.end()), 4096, 1<<20)
	if got, err := readAll(r, 10); err != nil || string(got) != "abc" {
		t.Fatal(got, err)
	}
	if _, err := r.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal(err)
	}
	if r.Consumed() != int64(len(b.out)+1) {
		t.Fatalf("consumed %d, stream %d", r.Consumed(), len(b.out)+1)
	}
	r.Reset(bytes.NewReader(b.end()), 4096, 1<<20)
	if got, err := readAll(r, 10); err != nil || string(got) != "abc" {
		t.Fatal(got, err)
	}
}

func TestLZMA2Errors(t *testing.T) {
	var ok l2Builder
	ok.lzma(resetDict, 3, 0, 2, func(e *symEnc) { lits(e, 20) })
	okStream := ok.end()

	// stream returns the builder's output with a tweak.
	tweak := func(f func(b []byte) []byte) []byte {
		return f(append([]byte(nil), okStream...))
	}
	var noProps l2Builder
	noProps.raw(true, []byte("abc"))
	noProps.out = append(noProps.out, 0x80, 0, 1, 0, 4, 0, 0, 0, 0, 0) // LZMA chunk, no properties since the reset

	var long l2Builder // chunk data longer than the declared uncompressed size
	long.lzma(resetDict, 3, 0, 2, func(e *symEnc) { lits(e, 20) })
	long.out[2] = 9 // usize 10 instead of 20

	var padded l2Builder // chunk longer than its range coder data
	padded.lzma(resetDict, 3, 0, 2, func(e *symEnc) { lits(e, 20) })
	padded.out[4]++ // csize+1
	padded.out = append(padded.out, 0)

	var farMatch l2Builder // a match to data before the dictionary reset
	farMatch.lzma(resetDict, 3, 0, 2, func(e *symEnc) { lits(e, 10) })
	farMatch.lzma(resetDict, 3, 0, 2, func(e *symEnc) {
		lits(e, 3)
		e.skipCopy = true
		e.match(5, 2)
		e.hist = append(e.hist, 0, 0) // the chunk declares the bytes the match would add
	})

	cases := []struct {
		name   string
		stream []byte
		dict   uint64
		want   error
	}{
		{"control 3", []byte{3}, 4096, xerr.ErrCorrupt},
		{"control 0x7f", []byte{0x7F}, 4096, xerr.ErrCorrupt},
		{"first chunk without dictionary reset (stored)", []byte{2, 0, 0, 'a', 0}, 4096, xerr.ErrCorrupt},
		{"first chunk without dictionary reset (lzma)", tweak(func(b []byte) []byte { b[0] = 0xC0; return b }), 4096, xerr.ErrCorrupt},
		{"lzma chunk without properties", noProps.end(), 4096, xerr.ErrCorrupt},
		{"bad properties byte", tweak(func(b []byte) []byte { b[5] = 225; return b }), 4096, xerr.ErrCorrupt},
		{"lc+lp > 4", tweak(func(b []byte) []byte { b[5] = propsByte(3, 2, 0); return b }), 4096, xerr.ErrCorrupt},
		{"chunk data exceeds size", long.end(), 4096, xerr.ErrCorrupt},
		{"chunk longer than data", padded.end(), 4096, xerr.ErrCorrupt},
		{"match before the dictionary reset", farMatch.end(), 4096, xerr.ErrCorrupt},
		{"truncated control", nil, 4096, xerr.ErrTruncated},
		{"truncated stored header", []byte{1, 0}, 4096, xerr.ErrTruncated},
		{"truncated stored data", []byte{1, 0, 5, 'a'}, 4096, xerr.ErrTruncated},
		{"truncated lzma header", okStream[:3], 4096, xerr.ErrTruncated},
		{"truncated lzma data", okStream[:12], 4096, xerr.ErrTruncated},
		{"range coder header", tweak(func(b []byte) []byte { b[6] = 1; return b }), 4096, xerr.ErrCorrupt},
		{"range coder too short", []byte{0xE0, 0, 0, 0, 1, propsByte(3, 0, 2), 0, 0}, 4096, xerr.ErrCorrupt},
		{"too large stored", func() []byte {
			var b l2Builder
			b.raw(true, make([]byte, 60000))
			return b.end()
		}(), 0xFFFFFFFF, xerr.ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			max := uint64(1 << 20)
			if c.want == xerr.ErrTooLarge {
				max = 40000
			}
			_, err := decodeL2(c.stream, c.dict, max, 100, false)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}

	// Declared compressed size: a limit smaller than the stream is refused,
	// both at a chunk header and in the middle of chunk data.
	for _, limit := range []int64{1, 4, 8} {
		r := NewLZMA2(bytes.NewReader(okStream), 4096, 1<<20)
		r.SetLimit(limit)
		if _, err := readAll(r, 100); !errors.Is(err, xerr.ErrCorrupt) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	r := NewLZMA2(bytes.NewReader(okStream), 4096, 1<<20)
	r.SetLimit(int64(len(okStream)))
	if _, err := readAll(r, 100); err != nil {
		t.Fatalf("exact limit: %v", err)
	}
}

// A chunk of more than 16 KiB of compressed data is read in several steps.
func TestLZMA2LargeChunk(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	var b l2Builder
	b.lzma(resetDict, 3, 0, 2, func(e *symEnc) {
		for i := 0; i < 40000; i++ {
			e.lit(byte(r.Intn(256)))
		}
	})
	if len(b.out) < 40000 {
		t.Fatalf("test chunk is only %d bytes", len(b.out))
	}
	got, err := decodeL2(b.end(), 1<<20, 1<<22, 4096, false)
	if err != nil || !bytes.Equal(got, b.want) {
		t.Fatalf("err=%v", err)
	}
}

func TestRangeDecoderDirectBitsCorruption(t *testing.T) {
	rc := rangeDec{rng: 0x20000000, code: 0x20000000, in: make([]byte, 8)}
	rc.direct(1)
	if !errors.Is(rc.err, xerr.ErrCorrupt) {
		t.Fatalf("err = %v", rc.err)
	}
}

type noProgress struct{}

func (noProgress) Read([]byte) (int, error) { return 0, nil }

func TestRangeDecoderNoProgress(t *testing.T) {
	rc := rangeDec{src: noProgress{}}
	rc.next()
	if !errors.Is(rc.err, io.ErrNoProgress) {
		t.Fatalf("err = %v", rc.err)
	}
	// The failure is sticky.
	rc.next()
	if !errors.Is(rc.err, io.ErrNoProgress) {
		t.Fatalf("err = %v", rc.err)
	}
}

func TestLZMA2MemoryFollowsData(t *testing.T) {
	var b l2Builder
	b.lzma(resetDict, 3, 0, 2, func(e *symEnc) { lits(e, 50); e.match(40, 100) })
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := decodeL2(b.end(), 0xFFFFFFFF, 256<<20, 100, false)
	runtime.ReadMemStats(&after)
	if err != nil || !bytes.Equal(got, b.want) {
		t.Fatal(err)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 1<<20 {
		t.Fatalf("allocated %d bytes for a %d byte stream with a 4 GiB dictionary", d, len(b.want))
	}
}
