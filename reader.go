// Package xz is a pure-Go decoder for the .xz container
// (xz-file-format.txt, tukaani.org) whose memory follows the decoded data and
// not the sizes a header declares.
//
// Its API mirrors the decode side of github.com/ulikunitz/xz so that a
// consumer can swap the import. The dictionary window of the LZMA2 filter is
// allocated lazily and never beyond [ReaderConfig.MaxDictionary]; a stream
// that needs more fails with [ErrTooLarge] before the allocation is made.
package xz

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"

	"github.com/go-compressions/xz/internal/filter"
	"github.com/go-compressions/xz/internal/lzmacore"
	"github.com/go-compressions/xz/internal/xerr"
)

// DefaultMaxDictionary is the window ceiling used when
// ReaderConfig.MaxDictionary is zero: 256 MiB, four times the largest
// dictionary of xz -9 (64 MiB).
const DefaultMaxDictionary = 256 << 20

// HeaderLen is the length of a stream header and of a stream footer.
const HeaderLen = 12

// Errors. They are shared with package lzma and work with errors.Is.
var (
	// ErrCorrupt is wrapped by every error caused by malformed input.
	ErrCorrupt = xerr.ErrCorrupt
	// ErrChecksum is a mismatch of a block check (CRC32, CRC64, SHA-256).
	ErrChecksum = xerr.ErrChecksum
	// ErrTruncated is input that ends inside a structure; it is also an
	// ErrCorrupt and an io.ErrUnexpectedEOF.
	ErrTruncated = xerr.ErrTruncated
	// ErrTooLarge means the stream needs a dictionary window larger than
	// MaxDictionary. No memory was allocated for it.
	ErrTooLarge = xerr.ErrTooLarge
	// ErrUnsupported is a well-formed feature that is not implemented: an
	// unknown filter ID, a reserved check type, a new stream version.
	ErrUnsupported = xerr.ErrUnsupported
)

// ReaderConfig defines the parameters of the xz reader.
type ReaderConfig struct {
	// MaxDictionary is the ceiling in bytes on the dictionary window the
	// reader will allocate for a block; zero means DefaultMaxDictionary.
	// It bounds memory, not the format: a block that declares a 4 GiB
	// dictionary decodes fine as long as the data it produces fits.
	MaxDictionary int

	// DictCap is accepted so that code written for ulikunitz/xz compiles.
	// There it is a minimum allocation; here the window follows the data,
	// so the field has no effect.
	//
	// Deprecated: use MaxDictionary.
	DictCap int

	// SingleStream makes the reader expect exactly one stream, without
	// padding, and report anything after it as an error.
	SingleStream bool
}

// Verify checks the parameters and replaces zero values by defaults.
func (c *ReaderConfig) Verify() error {
	if c == nil {
		return errors.New("xz: reader parameters are nil")
	}
	if c.MaxDictionary < 0 {
		return errors.New("xz: negative MaxDictionary")
	}
	if c.MaxDictionary == 0 {
		c.MaxDictionary = DefaultMaxDictionary
	}
	return nil
}

// Check type IDs (xz-file-format.txt, 2.1.1.2).
const (
	checkNone   = 0x00
	checkCRC32  = 0x01
	checkCRC64  = 0x04
	checkSHA256 = 0x0A
)

// Filter IDs (xz-file-format.txt, 5.3).
const (
	idDelta    = 0x03
	idX86      = 0x04
	idPowerPC  = 0x05
	idIA64     = 0x06
	idARM      = 0x07
	idARMThumb = 0x08
	idSPARC    = 0x09
	idARM64    = 0x0A
	idRISCV    = 0x0B
	idLZMA2    = 0x21
)

var (
	headerMagic = []byte{0xFD, '7', 'z', 'X', 'Z', 0x00}
	footerMagic = []byte{'Y', 'Z'}
	crc64Table  = crc64.MakeTable(crc64.ECMA)
)

type readerState int

const (
	stBlock  readerState = iota // decoding a block's data
	stHeader                    // next: a block header or the index
)

// Reader decodes one or several concatenated xz streams.
type Reader struct {
	cfg ReaderConfig
	src io.Reader
	err error

	state readerState

	// current stream
	flags     [2]byte
	checkSize int
	newCheck  func() hash.Hash
	blocks    uint64
	idxHash   hash.Hash

	// the largest dictionary size any block header declared (for tests)
	maxDeclared uint64

	// current block
	blk   block
	l2    *lzmacore.LZMA2
	hbuf  [1024]byte
	chain io.Reader
}

type block struct {
	hdrSize    int
	hasC, hasU bool
	csize      uint64
	usize      uint64
	count      uint64 // uncompressed bytes seen
	sum        hash.Hash
}

// NewReader creates a reader with the default parameters. It reads and
// checks the header of the first stream.
func NewReader(xz io.Reader) (*Reader, error) {
	return ReaderConfig{}.NewReader(xz)
}

// NewReader creates a reader using the configuration. Unless SingleStream is
// set it processes concatenated streams and stream padding.
func (c ReaderConfig) NewReader(xz io.Reader) (*Reader, error) {
	if err := c.Verify(); err != nil {
		return nil, err
	}
	r := &Reader{cfg: c, src: xz}
	if err := r.startStream(); err != nil {
		return nil, err
	}
	return r, nil
}

// startStream reads a stream header.
func (r *Reader) startStream() error {
	var h [HeaderLen]byte
	if _, err := io.ReadFull(r.src, h[:]); err != nil {
		return xerr.Truncate(err)
	}
	return r.parseStreamHeader(h[:])
}

func (r *Reader) parseStreamHeader(h []byte) error {
	if !bytes.Equal(h[:6], headerMagic) {
		return xerr.Corrupt("not an xz stream (bad magic)")
	}
	if crc32.ChecksumIEEE(h[6:8]) != binary.LittleEndian.Uint32(h[8:]) {
		return xerr.Corrupt("stream header CRC32 mismatch")
	}
	if h[6] != 0 || h[7]&0xF0 != 0 {
		return xerr.Unsupported("stream flags with reserved bits set")
	}
	r.flags = [2]byte{h[6], h[7]}
	switch h[7] & 0x0F {
	case checkNone:
		r.checkSize, r.newCheck = 0, nil
	case checkCRC32:
		r.checkSize, r.newCheck = 4, func() hash.Hash { return crc32.NewIEEE() }
	case checkCRC64:
		r.checkSize, r.newCheck = 8, func() hash.Hash { return crc64.New(crc64Table) }
	case checkSHA256:
		r.checkSize, r.newCheck = 32, sha256.New
	default:
		return xerr.Unsupported("check type 0x%02x", h[7]&0x0F)
	}
	r.blocks = 0
	r.idxHash = sha256.New()
	r.state = stHeader
	return nil
}

// Read reads uncompressed data. Data of a block is delivered before the
// block's check is verified; an error from the check follows the data.
func (r *Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if r.err != nil {
			return 0, r.err
		}
		if r.state == stHeader {
			if err := r.nextBlock(); err != nil {
				r.err = err
			}
			continue
		}
		n, err := r.chain.Read(p)
		if n > 0 {
			r.blk.count += uint64(n)
			if r.blk.sum != nil {
				r.blk.sum.Write(p[:n])
			}
			if r.blk.hasU && r.blk.count > r.blk.usize {
				err = xerr.Corrupt("block is longer than its declared uncompressed size")
			}
		}
		if err == io.EOF {
			err = r.endBlock()
		}
		if err != nil {
			r.err = err
		}
		if n > 0 {
			return n, nil
		}
	}
}

// nextBlock handles the byte after a block (or the stream header): a block
// header, or the index indicator that ends the stream.
func (r *Reader) nextBlock() error {
	if _, err := io.ReadFull(r.src, r.hbuf[:1]); err != nil {
		return xerr.Truncate(err)
	}
	if r.hbuf[0] == 0 {
		return r.endStream()
	}
	size := (int(r.hbuf[0]) + 1) * 4
	if _, err := io.ReadFull(r.src, r.hbuf[1:size]); err != nil {
		return xerr.Truncate(err)
	}
	return r.startBlock(r.hbuf[:size])
}

type filterSpec struct {
	id    uint64
	props []byte
}

func (r *Reader) startBlock(h []byte) error {
	size := len(h)
	if crc32.ChecksumIEEE(h[:size-4]) != binary.LittleEndian.Uint32(h[size-4:]) {
		return xerr.Corrupt("block header CRC32 mismatch")
	}
	flags := h[1]
	if flags&0x3C != 0 {
		return xerr.Unsupported("block flags with reserved bits set")
	}
	b := &r.blk
	*b = block{hdrSize: size, hasC: flags&0x40 != 0, hasU: flags&0x80 != 0}
	body := h[2 : size-4]
	if b.hasC {
		v, n, err := parseVLI(body)
		if err != nil {
			return headerErr(err)
		}
		if v == 0 {
			return xerr.Corrupt("zero compressed size in block header")
		}
		b.csize, body = v, body[n:]
	}
	if b.hasU {
		v, n, err := parseVLI(body)
		if err != nil {
			return headerErr(err)
		}
		b.usize, body = v, body[n:]
	}
	var specs [4]filterSpec
	nf := int(flags&3) + 1
	for i := 0; i < nf; i++ {
		id, n, err := parseVLI(body)
		if err != nil {
			return headerErr(err)
		}
		body = body[n:]
		plen, n, err := parseVLI(body)
		if err != nil {
			return headerErr(err)
		}
		body = body[n:]
		if plen > uint64(len(body)) {
			return xerr.Corrupt("filter properties exceed the block header")
		}
		specs[i] = filterSpec{id, body[:plen]}
		body = body[plen:]
	}
	for _, c := range body {
		if c != 0 {
			// A valid CRC32 with data where padding is expected may be a
			// field of a newer format version (liblzma reports the same).
			return xerr.Unsupported("non-zero block header padding")
		}
	}
	if err := r.buildChain(specs[:nf]); err != nil {
		return err
	}
	if r.newCheck != nil {
		b.sum = r.newCheck()
	}
	r.state = stBlock
	return nil
}

// headerErr turns the "truncated" report of parseVLI, which inside a block
// header whose CRC is valid means a malformed header, into ErrCorrupt.
func headerErr(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return xerr.Corrupt("block header fields exceed the header")
	}
	return err
}

func (r *Reader) buildChain(specs []filterSpec) error {
	for _, s := range specs {
		switch s.id {
		case idLZMA2, idDelta, idX86, idPowerPC, idIA64, idARM, idARMThumb, idSPARC, idARM64, idRISCV:
		default:
			return xerr.Unsupported("filter ID 0x%x", s.id)
		}
	}
	last := len(specs) - 1
	// Chains the format does not allow are reported as unsupported, as
	// liblzma does (xz-utils tests/files/README, unsupported-filter_flags-2
	// and -3).
	if specs[last].id != idLZMA2 {
		return xerr.Unsupported("the last filter is not LZMA2")
	}
	for _, s := range specs[:last] {
		if s.id == idLZMA2 {
			return xerr.Unsupported("LZMA2 is not the last filter")
		}
	}
	lp := specs[last].props
	if len(lp) != 1 {
		return xerr.Corrupt("LZMA2 properties must be one byte")
	}
	dict, ok := lzmacore.DictSizeFromProp(lp[0])
	if !ok {
		return xerr.Corrupt("invalid LZMA2 dictionary size")
	}
	r.maxDeclared = max(r.maxDeclared, dict)
	if r.l2 == nil {
		r.l2 = lzmacore.NewLZMA2(r.src, dict, uint64(r.cfg.MaxDictionary))
	} else {
		r.l2.Reset(r.src, dict, uint64(r.cfg.MaxDictionary))
	}
	if r.blk.hasC {
		r.l2.SetLimit(int64(min(r.blk.csize, 1<<62)))
	}
	var chain io.Reader = r.l2
	for i := last - 1; i >= 0; i-- {
		f, err := newFilter(chain, specs[i])
		if err != nil {
			return err
		}
		chain = f
	}
	r.chain = chain
	return nil
}

// newFilter wraps src with the decoder for a non-last filter.
func newFilter(src io.Reader, s filterSpec) (io.Reader, error) {
	if s.id == idDelta {
		if len(s.props) != 1 {
			return nil, xerr.Corrupt("delta filter properties must be one byte")
		}
		return filter.NewDelta(src, int(s.props[0])+1), nil
	}
	var start uint32
	switch len(s.props) {
	case 0:
	case 4:
		start = binary.LittleEndian.Uint32(s.props)
	default:
		return nil, xerr.Corrupt("BCJ filter properties must be 0 or 4 bytes")
	}
	var code filter.Code
	var align uint32
	switch s.id {
	case idX86:
		code, align = filter.NewX86().Code, 1
	case idPowerPC:
		code, align = filter.PowerPC, 4
	case idIA64:
		code, align = filter.IA64, 16
	case idARM:
		code, align = filter.ARM, 4
	case idARMThumb:
		code, align = filter.ARMThumb, 2
	case idSPARC:
		code, align = filter.SPARC, 4
	case idARM64:
		code, align = filter.ARM64, 4
	default: // idRISCV
		code, align = filter.RISCV, 2
	}
	if start%align != 0 {
		return nil, xerr.Corrupt("BCJ start offset is not a multiple of the alignment")
	}
	return filter.NewSimple(src, code, start), nil
}

// endBlock verifies a block whose LZMA2 data has ended and records it for the
// index.
func (r *Reader) endBlock() error {
	b := &r.blk
	if b.hasU && b.count != b.usize {
		return xerr.Corrupt("block is shorter than its declared uncompressed size")
	}
	comp := r.l2.Consumed()
	if b.hasC && uint64(comp) != b.csize {
		return xerr.Corrupt("block does not match its declared compressed size")
	}
	var pad [3]byte
	if n := int(-comp & 3); n > 0 {
		if _, err := io.ReadFull(r.src, pad[:n]); err != nil {
			return xerr.Truncate(err)
		}
		for _, c := range pad[:n] {
			if c != 0 {
				return xerr.Corrupt("non-zero block padding")
			}
		}
	}
	if b.sum != nil {
		var want [32]byte
		if _, err := io.ReadFull(r.src, want[:r.checkSize]); err != nil {
			return xerr.Truncate(err)
		}
		// The format stores CRCs little endian; hash.Hash sums are big endian.
		var got []byte
		switch r.checkSize {
		case 4:
			got = binary.LittleEndian.AppendUint32(nil, b.sum.(hash.Hash32).Sum32())
		case 8:
			got = binary.LittleEndian.AppendUint64(nil, b.sum.(hash.Hash64).Sum64())
		default:
			got = b.sum.Sum(nil)
		}
		if !bytes.Equal(got, want[:r.checkSize]) {
			return xerr.ErrChecksum
		}
	}
	unpadded := uint64(b.hdrSize) + uint64(comp) + uint64(r.checkSize)
	var rec []byte
	rec = appendVLI(rec, unpadded)
	rec = appendVLI(rec, b.count)
	r.idxHash.Write(rec)
	r.blocks++
	r.state = stHeader
	return nil
}

// endStream parses the index (its indicator byte was consumed), the stream
// footer, and what follows.
func (r *Reader) endStream() error {
	x := &indexReader{r: r.src, crc: crc32.ChecksumIEEE([]byte{0}), n: 1}
	count, err := x.vli()
	if err != nil {
		return err
	}
	if count != r.blocks {
		return xerr.Corrupt("index lists %d blocks, stream has %d", count, r.blocks)
	}
	h := sha256.New()
	for i := uint64(0); i < count; i++ {
		unpadded, err := x.vli()
		if err != nil {
			return err
		}
		usize, err := x.vli()
		if err != nil {
			return err
		}
		var rec []byte
		rec = appendVLI(rec, unpadded)
		rec = appendVLI(rec, usize)
		h.Write(rec)
	}
	if !bytes.Equal(h.Sum(nil), r.idxHash.Sum(nil)) {
		return xerr.Corrupt("index does not match the blocks")
	}
	for x.n&3 != 0 {
		c, err := x.byte()
		if err != nil {
			return err
		}
		if c != 0 {
			return xerr.Corrupt("non-zero index padding")
		}
	}
	var tail [4]byte
	if _, err := io.ReadFull(r.src, tail[:]); err != nil {
		return xerr.Truncate(err)
	}
	if binary.LittleEndian.Uint32(tail[:]) != x.crc {
		return xerr.Corrupt("index CRC32 mismatch")
	}
	x.n += 4

	var f [HeaderLen]byte
	if _, err := io.ReadFull(r.src, f[:]); err != nil {
		return xerr.Truncate(err)
	}
	if crc32.ChecksumIEEE(f[4:10]) != binary.LittleEndian.Uint32(f[:]) {
		return xerr.Corrupt("stream footer CRC32 mismatch")
	}
	if (int64(binary.LittleEndian.Uint32(f[4:]))+1)*4 != x.n {
		return xerr.Corrupt("backward size does not match the index")
	}
	if f[8] != r.flags[0] || f[9] != r.flags[1] {
		return xerr.Corrupt("stream footer flags differ from the header")
	}
	if !bytes.Equal(f[10:], footerMagic) {
		return xerr.Corrupt("bad stream footer magic")
	}
	return r.afterStream()
}

// afterStream handles stream padding and further streams; it returns io.EOF
// at the end of the input.
func (r *Reader) afterStream() error {
	if r.cfg.SingleStream {
		var one [1]byte
		_, err := io.ReadFull(r.src, one[:])
		if err == io.EOF {
			return io.EOF
		}
		if err != nil {
			return err
		}
		return xerr.Corrupt("unexpected data after the stream")
	}
	var w [4]byte
	for {
		_, err := io.ReadFull(r.src, w[:])
		if err == io.EOF {
			return io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return xerr.Corrupt("stream padding is not a multiple of four bytes")
		}
		if err != nil {
			return err
		}
		if w != [4]byte{} {
			break
		}
	}
	var h [HeaderLen]byte
	copy(h[:], w[:])
	if _, err := io.ReadFull(r.src, h[4:]); err != nil {
		return xerr.Truncate(err)
	}
	return r.parseStreamHeader(h[:])
}
