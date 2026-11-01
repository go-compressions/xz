package lzmacore

import (
	"encoding/binary"
	"io"

	"github.com/go-compressions/xz/internal/xerr"
)

// HeaderLen is the size of the .lzma ("LZMA alone") header.
const HeaderLen = 13

// MinDictSize is LZMA_DIC_MIN of the reference decoder.
const MinDictSize = 1 << 12

// Header is the 13-byte header of a .lzma file (xz-utils
// doc/lzma-file-format.txt).
type Header struct {
	LC, LP, PB uint
	// DictSize is the dictionary size in the header, DecodeDictSize the one
	// used for decoding (raised to MinDictSize as in the reference).
	DictSize, DecodeDictSize uint32
	// Size is the uncompressed size, valid when SizeKnown. When it is not
	// known the stream carries an end marker.
	Size      uint64
	SizeKnown bool
}

// ParseHeader decodes a .lzma header.
func ParseHeader(b []byte) (Header, error) {
	var h Header
	d := uint(b[0])
	if d >= 9*5*5 {
		return h, xerr.Corrupt("invalid lzma properties byte")
	}
	h.LC = d % 9
	d /= 9
	h.PB = d / 5
	h.LP = d % 5
	h.DictSize = binary.LittleEndian.Uint32(b[1:])
	h.DecodeDictSize = h.DictSize
	if h.DecodeDictSize < MinDictSize {
		h.DecodeDictSize = MinDictSize
	}
	h.Size = binary.LittleEndian.Uint64(b[5:])
	h.SizeKnown = h.Size != ^uint64(0)
	return h, nil
}

// Alone decodes a .lzma stream.
type Alone struct {
	h   Header
	dec Decoder
	err error
}

// NewAlone reads the .lzma header from src and prepares decoding. max is the
// ceiling on the window allocation.
func NewAlone(src io.Reader, max uint64) (*Alone, error) {
	var b [HeaderLen]byte
	if _, err := io.ReadFull(src, b[:]); err != nil {
		return nil, xerr.Truncate(err)
	}
	h, err := ParseHeader(b[:])
	if err != nil {
		return nil, err
	}
	return newAlone(src, h, max), nil
}

func newAlone(src io.Reader, h Header, max uint64) *Alone {
	a := &Alone{h: h}
	a.dec.win.reset(uint64(h.DecodeDictSize), max)
	a.dec.SetProps(h.LC, h.LP, h.PB)
	a.dec.ResetState()
	a.dec.rc.src = src
	a.dec.start(h.SizeKnown, h.Size, !h.SizeKnown, false)
	return a
}

// NewRaw prepares decoding of a headerless LZMA stream of unknown size that
// ends with an end marker (the coding inside an lzip member). The caller has
// validated lc, lp and pb. dict is raised to MinDictSize as in the reference.
func NewRaw(src io.Reader, lc, lp, pb uint, dict uint32, max uint64) *Alone {
	h := Header{LC: lc, LP: lp, PB: pb, DictSize: dict, DecodeDictSize: max32(dict, MinDictSize)}
	return newAlone(src, h, max)
}

func max32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

// Unconsumed returns the bytes read from the source that the decoder did not
// use. After the end marker has been reported as io.EOF they are the bytes that
// follow the stream. The slice is valid until the next Read.
func (a *Alone) Unconsumed() []byte { return a.dec.rc.in[a.dec.rc.pos:] }

// Header returns the stream header.
func (a *Alone) Header() Header { return a.h }

func (a *Alone) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if a.err != nil {
		return 0, a.err
	}
	n, done, err := a.dec.Read(p)
	switch {
	case err != nil:
		a.err = err
	case done:
		a.err = io.EOF
	}
	return n, a.err
}
