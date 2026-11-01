package lzmacore

import (
	"io"
	"slices"

	"github.com/go-compressions/xz/internal/xerr"
)

// DictSizeFromProp decodes the one-byte LZMA2 dictionary size property
// (xz-file-format.txt, section 5.3.1). ok is false for values above 40.
func DictSizeFromProp(b byte) (size uint64, ok bool) {
	switch {
	case b > 40:
		return 0, false
	case b == 40:
		return 0xFFFFFFFF, true
	}
	return uint64(2|b&1) << (b/2 + 11), true
}

// LZMA2 decodes a sequence of LZMA2 chunks (liblzma/lzma/lzma2_decoder.c,
// XZ Embedded xz_dec_lzma2.c). It reads from src exactly the bytes it
// consumes, so the caller knows where the stream ends.
type LZMA2 struct {
	src      io.Reader
	dec      Decoder
	chunk    []byte
	n        int64 // bytes consumed from src
	limit    int64 // fail if more than this is consumed; 0 means no limit
	allowEOF bool  // a clean EOF at a chunk boundary ends the stream

	needDictReset bool
	needProps     bool
	inLZMA        bool
	rawLeft       int
	eos           bool
	err           error
	hdr           [5]byte
}

// NewLZMA2 returns a decoder for the LZMA2 stream in src. dictSize is the
// declared dictionary size and max the ceiling on the window actually
// allocated; the window follows the data and never the declared size.
func NewLZMA2(src io.Reader, dictSize, max uint64) *LZMA2 {
	r := &LZMA2{}
	r.Reset(src, dictSize, max)
	return r
}

// Reset reuses r (and its window memory) for another LZMA2 stream.
func (r *LZMA2) Reset(src io.Reader, dictSize, max uint64) {
	r.src = src
	r.n, r.limit, r.allowEOF = 0, 0, false
	r.needDictReset, r.needProps = true, true
	r.inLZMA, r.rawLeft, r.eos, r.err = false, 0, false, nil
	r.dec.win.reset(dictSize, max)
	r.dec.err, r.dec.finished, r.dec.needRoom = nil, false, false
	r.dec.pend, r.dec.pendLit = 0, false
}

// SetLimit makes the decoder fail once it has consumed more than n bytes
// (0: no limit). The xz block reader uses it for the declared compressed size.
func (r *LZMA2) SetLimit(n int64) { r.limit = n }

// SetAllowEOF accepts a clean end of input at a chunk boundary as the end of
// the stream, for raw LZMA2 data that carries no 0x00 terminator.
func (r *LZMA2) SetAllowEOF(v bool) { r.allowEOF = v }

// Consumed is the number of bytes read from src so far.
func (r *LZMA2) Consumed() int64 { return r.n }

// read reads exactly len(b) bytes from src. atBoundary says that an EOF
// before the first byte is a clean end rather than a truncation.
func (r *LZMA2) read(b []byte, atBoundary bool) error {
	if r.limit > 0 && r.n+int64(len(b)) > r.limit {
		return xerr.Corrupt("lzma2 data exceeds the declared compressed size")
	}
	n, err := io.ReadFull(r.src, b)
	r.n += int64(n)
	if err == io.EOF && atBoundary {
		return io.EOF
	}
	return xerr.Truncate(err)
}

func (r *LZMA2) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w := &r.dec.win
	for {
		if w.rd < w.pos {
			n := copy(p, w.buf[w.rd:w.pos])
			w.rd += n
			return n, nil
		}
		if r.err != nil {
			return 0, r.err
		}
		switch {
		case r.eos:
			r.err = io.EOF
		case r.rawLeft > 0:
			r.err = r.copyRaw()
		case r.inLZMA:
			n, done, err := r.dec.Read(p)
			switch {
			case err != nil:
				r.err = err
			case done:
				r.err = r.endChunk()
			default:
				return n, nil
			}
		default:
			r.err = r.nextChunk()
		}
	}
}

func (r *LZMA2) endChunk() error {
	r.inLZMA = false
	if r.dec.rc.pos != len(r.dec.rc.in) {
		return xerr.Corrupt("lzma2 chunk is longer than its data")
	}
	return nil
}

func (r *LZMA2) copyRaw() error {
	w := &r.dec.win
	if w.pos == len(w.buf) {
		if err := w.advance(); err != nil {
			return err
		}
	}
	n := len(w.buf) - w.pos
	if n > r.rawLeft {
		n = r.rawLeft
	}
	if err := r.read(w.buf[w.pos:w.pos+n], false); err != nil {
		return err
	}
	w.pos += n
	w.total += uint32(n)
	r.rawLeft -= n
	return nil
}

// nextChunk reads a chunk header and prepares the chunk. It returns io.EOF
// only at the end of the stream.
func (r *LZMA2) nextChunk() error {
	if err := r.read(r.hdr[:1], r.allowEOF); err != nil {
		if err == io.EOF {
			r.eos = true
			return nil
		}
		return err
	}
	c := r.hdr[0]
	if c == 0 {
		r.eos = true
		return nil
	}
	if c > 2 && c < 0x80 {
		return xerr.Corrupt("invalid lzma2 control byte 0x%02x", c)
	}
	if c == 1 || c >= 0xE0 {
		r.dec.win.resetDict()
		r.needDictReset = false
		r.needProps = true
	} else if r.needDictReset {
		return xerr.Corrupt("lzma2 stream does not start with a dictionary reset")
	}
	if c < 0x80 {
		if err := r.read(r.hdr[:2], false); err != nil {
			return err
		}
		r.rawLeft = (int(r.hdr[0])<<8 | int(r.hdr[1])) + 1
		return nil
	}

	size := 4
	if c >= 0xC0 {
		size = 5
	}
	if err := r.read(r.hdr[:size], false); err != nil {
		return err
	}
	usize := (uint64(c&0x1F)<<16 | uint64(r.hdr[0])<<8 | uint64(r.hdr[1])) + 1
	csize := (int(r.hdr[2])<<8 | int(r.hdr[3])) + 1
	if c >= 0xC0 {
		d := uint(r.hdr[4])
		if d >= 9*5*5 {
			return xerr.Corrupt("invalid lzma2 properties byte")
		}
		lc := d % 9
		d /= 9
		lp := d % 5
		pb := d / 5
		if lc+lp > 4 {
			return xerr.Corrupt("lzma2 requires lc+lp <= 4")
		}
		r.dec.SetProps(lc, lp, pb)
		r.needProps = false
	} else if r.needProps {
		return xerr.Corrupt("lzma2 chunk without properties")
	}
	if c >= 0xA0 {
		r.dec.ResetState()
	}

	// The compressed chunk is read in steps so that what is allocated
	// follows the bytes that really arrive, not the size the header claims.
	r.chunk = r.chunk[:0]
	for len(r.chunk) < csize {
		step := csize - len(r.chunk)
		if step > 16<<10 {
			step = 16 << 10
		}
		old := len(r.chunk)
		r.chunk = slices.Grow(r.chunk, step)[:old+step]
		if err := r.read(r.chunk[old:], false); err != nil {
			return err
		}
	}
	r.dec.rc.in, r.dec.rc.pos, r.dec.rc.src = r.chunk, 0, nil
	r.dec.start(true, usize, false, true)
	r.inLZMA = true
	return nil
}
