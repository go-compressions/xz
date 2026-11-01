package lzmacore

import (
	"io"

	"github.com/go-compressions/xz/internal/xerr"
)

const (
	numBitModelTotalBits = 11
	numMoveBits          = 5
	probInit             = (1 << numBitModelTotalBits) / 2
	topValue             = 1 << 24
	inputBuffer          = 32 << 10
)

// rangeDec is the reference CRangeDecoder. Input comes from in[pos:]; when
// that is exhausted and src is set, in is refilled from src, otherwise the
// stream is truncated. A failure is sticky in err and feeds zero bytes, so
// the decode loop needs no check per byte, only one per symbol.
type rangeDec struct {
	rng, code uint32
	in        []byte
	pos       int
	src       io.Reader
	err       error
}

func (rc *rangeDec) next() byte {
	if rc.pos < len(rc.in) {
		b := rc.in[rc.pos]
		rc.pos++
		return b
	}
	return rc.refill()
}

func (rc *rangeDec) refill() byte {
	if rc.src == nil {
		rc.fail(xerr.ErrTruncated)
		return 0
	}
	if rc.err != nil {
		return 0
	}
	if cap(rc.in) < inputBuffer {
		rc.in = make([]byte, 0, inputBuffer)
	}
	for tries := 0; tries < 100; tries++ {
		n, err := rc.src.Read(rc.in[:inputBuffer])
		if n > 0 {
			rc.in = rc.in[:n]
			rc.pos = 1
			return rc.in[0]
		}
		if err != nil {
			rc.in, rc.pos = rc.in[:0], 0
			rc.fail(xerr.Truncate(err))
			return 0
		}
	}
	rc.in, rc.pos = rc.in[:0], 0
	rc.fail(io.ErrNoProgress)
	return 0
}

func (rc *rangeDec) fail(err error) {
	if rc.err == nil {
		rc.err = err
	}
}

// init is CRangeDecoder::Init: a zero byte and four bytes of code.
func (rc *rangeDec) init() {
	rc.rng = 0xFFFFFFFF
	rc.code = 0
	b := rc.next()
	for i := 0; i < 4; i++ {
		rc.code = rc.code<<8 | uint32(rc.next())
	}
	if rc.err != nil {
		return
	}
	if b != 0 || rc.code == rc.rng {
		rc.fail(xerr.Corrupt("invalid range coder header"))
	}
}

func (rc *rangeDec) finishedOK() bool { return rc.code == 0 }

func (rc *rangeDec) direct(numBits uint) uint32 {
	var res uint32
	for ; numBits > 0; numBits-- {
		rc.rng >>= 1
		rc.code -= rc.rng
		t := 0 - (rc.code >> 31)
		rc.code += rc.rng & t
		if rc.code == rc.rng {
			rc.fail(xerr.Corrupt("invalid range coder state"))
		}
		if rc.rng < topValue {
			rc.rng <<= 8
			rc.code = rc.code<<8 | uint32(rc.next())
		}
		res = res<<1 + t + 1
	}
	return res
}

func (rc *rangeDec) bit(p *uint16) uint32 {
	v := uint32(*p)
	bound := (rc.rng >> numBitModelTotalBits) * v
	var s uint32
	if rc.code < bound {
		v += ((1 << numBitModelTotalBits) - v) >> numMoveBits
		rc.rng = bound
	} else {
		v -= v >> numMoveBits
		rc.code -= bound
		rc.rng -= bound
		s = 1
	}
	*p = uint16(v)
	if rc.rng < topValue {
		rc.rng <<= 8
		rc.code = rc.code<<8 | uint32(rc.next())
	}
	return s
}

// tree is CBitTreeDecoder::Decode.
func (rc *rangeDec) tree(probs []uint16, numBits uint) uint32 {
	m := uint32(1)
	for i := uint(0); i < numBits; i++ {
		m = m<<1 + rc.bit(&probs[m])
	}
	return m - 1<<numBits
}

// reverse is BitTreeReverseDecode.
func (rc *rangeDec) reverse(probs []uint16, numBits uint) uint32 {
	m := uint32(1)
	var sym uint32
	for i := uint(0); i < numBits; i++ {
		b := rc.bit(&probs[m])
		m = m<<1 + b
		sym |= b << i
	}
	return sym
}

func initProbs(p []uint16) {
	for i := range p {
		p[i] = probInit
	}
}
