// Package lzma decodes the .lzma ("LZMA alone") format and raw LZMA2 streams.
//
// The API mirrors the decode side of github.com/ulikunitz/xz/lzma. The
// dictionary window is allocated lazily and never beyond MaxDictionary, so
// a header that declares a 4 GiB dictionary costs kilobytes unless the data
// really needs more.
//
// Sources: LZMA SDK DOC/lzma-specification.txt and its reference decoder
// LzmaSpec.cpp (public domain); xz-utils doc/lzma-file-format.txt;
// liblzma/lzma/lzma2_decoder.c (0BSD).
package lzma

import (
	"errors"
	"io"
	"math"

	"github.com/go-compressions/xz/internal/lzmacore"
	"github.com/go-compressions/xz/internal/xerr"
)

// DefaultMaxDictionary is the window ceiling used when MaxDictionary is
// zero: 256 MiB.
const DefaultMaxDictionary = 256 << 20

// MinDictCap is the smallest dictionary size (LZMA_DIC_MIN).
const MinDictCap = lzmacore.MinDictSize

// MaxDictCap is the largest dictionary size an LZMA2 property can declare.
const MaxDictCap = 1<<32 - 1

// Errors, shared with package xz.
var (
	ErrCorrupt     = xerr.ErrCorrupt
	ErrTruncated   = xerr.ErrTruncated
	ErrTooLarge    = xerr.ErrTooLarge
	ErrUnsupported = xerr.ErrUnsupported
)

func ceiling(max int) (uint64, error) {
	switch {
	case max < 0:
		return 0, errors.New("lzma: negative MaxDictionary")
	case max == 0:
		return DefaultMaxDictionary, nil
	}
	return uint64(max), nil
}

// Header is the header of a .lzma stream.
type Header struct {
	// Properties lc, lp and pb.
	LC, LP, PB uint
	// DictCap is the dictionary size declared by the header.
	DictCap uint32
	// Size is the uncompressed size, or -1 when the stream is terminated by
	// an end marker instead.
	Size int64
}

// ReaderConfig holds the parameters of Reader.
type ReaderConfig struct {
	// MaxDictionary is the ceiling in bytes on the dictionary window that
	// will be allocated; zero means DefaultMaxDictionary.
	MaxDictionary int

	// DictCap exists for source compatibility with ulikunitz/xz and is
	// ignored: use MaxDictionary.
	//
	// Deprecated: use MaxDictionary.
	DictCap int
}

// Verify checks the configuration and replaces zero values by defaults.
func (c *ReaderConfig) Verify() error {
	m, err := ceiling(c.MaxDictionary)
	if err != nil {
		return err
	}
	c.MaxDictionary = int(min(m, math.MaxInt))
	return nil
}

// Reader decodes a .lzma stream.
type Reader struct {
	a *lzmacore.Alone
}

// NewReader reads the header of a .lzma stream from lzma and returns a
// reader of the uncompressed data.
func NewReader(lzma io.Reader) (*Reader, error) {
	return ReaderConfig{}.NewReader(lzma)
}

// NewReader creates a Reader with the configuration.
func (c ReaderConfig) NewReader(lzma io.Reader) (*Reader, error) {
	if err := c.Verify(); err != nil {
		return nil, err
	}
	a, err := lzmacore.NewAlone(lzma, uint64(c.MaxDictionary))
	if err != nil {
		return nil, err
	}
	return &Reader{a}, nil
}

// Header returns the header of the stream.
func (r *Reader) Header() Header {
	h := r.a.Header()
	out := Header{LC: h.LC, LP: h.LP, PB: h.PB, DictCap: h.DictSize, Size: -1}
	if h.SizeKnown && h.Size <= math.MaxInt64 {
		out.Size = int64(h.Size)
	}
	return out
}

// Read reads uncompressed data.
func (r *Reader) Read(p []byte) (int, error) { return r.a.Read(p) }

// Reader2Config holds the parameters of Reader2.
type Reader2Config struct {
	// DictCap is the dictionary size declared by the container (for
	// example by the LZMA2 property byte of a 7z coder, see DictCapFromProp).
	// Matches reaching further back are rejected. Zero means "as large as
	// the format allows", bounded in memory by MaxDictionary.
	DictCap int

	// MaxDictionary is the ceiling in bytes on the window that will be
	// allocated; zero means DefaultMaxDictionary.
	MaxDictionary int
}

// Verify checks the configuration; zero values are replaced by defaults,
// except DictCap, which stays zero.
func (c *Reader2Config) Verify() error {
	if c.DictCap < 0 || int64(c.DictCap) > MaxDictCap {
		return errors.New("lzma: dictionary capacity is out of range")
	}
	m, err := ceiling(c.MaxDictionary)
	if err != nil {
		return err
	}
	c.MaxDictionary = int(min(m, math.MaxInt))
	return nil
}

// DictCapFromProp decodes the one-byte LZMA2 dictionary property used by
// xz, 7z and zip (method 14).
func DictCapFromProp(prop byte) (uint32, error) {
	d, ok := lzmacore.DictSizeFromProp(prop)
	if !ok {
		return 0, xerr.Corrupt("invalid LZMA2 dictionary property %d", prop)
	}
	return uint32(d), nil
}

// Reader2 decodes a raw LZMA2 chunk sequence, as found in 7z and zip.
type Reader2 struct {
	l *lzmacore.LZMA2
}

// NewReader2 creates a reader for raw LZMA2 data with the default
// configuration.
func NewReader2(lzma2 io.Reader) (*Reader2, error) {
	return Reader2Config{}.NewReader2(lzma2)
}

// NewReader2 creates a reader for raw LZMA2 data. The stream ends at the
// 0x00 chunk, or at a clean end of input between chunks.
func (c Reader2Config) NewReader2(lzma2 io.Reader) (*Reader2, error) {
	if err := c.Verify(); err != nil {
		return nil, err
	}
	dict := uint64(c.DictCap)
	if dict == 0 {
		dict = MaxDictCap
	} else if dict < MinDictCap {
		dict = MinDictCap
	}
	l := lzmacore.NewLZMA2(lzma2, dict, uint64(c.MaxDictionary))
	l.SetAllowEOF(true)
	return &Reader2{l}, nil
}

// Read reads uncompressed data.
func (r *Reader2) Read(p []byte) (int, error) { return r.l.Read(p) }
