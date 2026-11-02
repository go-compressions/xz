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

	// bitModelOffset is liblzma's RC_BIT_MODEL_OFFSET, wrapped to uint32:
	// v - (v+offset)>>numMoveBits, kept to 16 bits, is the bit-0 update.
	bitModelOffset = uint32(1<<32 + 1<<numMoveBits - 1 - 1<<numBitModelTotalBits)
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

// The loops below decode many bits per call, so they are bit written out by
// hand with rng, code, in and pos held in locals, the way liblzma's rc_bit
// macros work. Go does not inline bit (cost 124, budget 80), and through the
// pointer every bit loads and stores the coder state. Each loop is the
// reference bit, step for step; only where the state lives changes.

// refillAt is next for the loops that hold the position in a local: it
// refills at pos and hands back the new input and position.
func (rc *rangeDec) refillAt(pos int) (byte, []byte, int) {
	rc.pos = pos
	b := rc.refill()
	return b, rc.in, rc.pos
}

// tree is CBitTreeDecoder::Decode.
func (rc *rangeDec) tree(probs []uint16, numBits uint) uint32 {
	return rc.treeFrom(probs, 1, 1<<numBits) - 1<<numBits
}

// treeFrom continues a bit tree from node m until m reaches top; the literal
// coder resumes here after its matched bits. Slicing probs to top lets the
// loop test bound the index, so it carries no bounds check.
func (rc *rangeDec) treeFrom(probs []uint16, m, top uint32) uint32 {
	rng, code, in, pos := rc.rng, rc.code, rc.in, rc.pos
	probs = probs[:top]
	if m >= top {
		return m
	}
	v := uint32(probs[m])
	for {
		bound := (rng >> numBitModelTotalBits) * v
		// liblzma's rc_c_bit: the bit becomes a mask instead of a branch.
		bit := uint32((uint64(code)-uint64(bound))>>63) ^ 1
		mask := 0 - bit
		rng &= mask
		bound ^= mask
		bound -= mask
		rng += bound
		code += bound & mask
		probs[m] = uint16(v - (v+(^mask&bitModelOffset))>>numMoveBits)
		n := m << 1
		m = n + bit
		last := n >= top
		if !last {
			// Both children are loaded before the bit is known and the mask
			// picks one, so the next probability is off the critical path.
			v0, v1 := uint32(probs[n]), uint32(probs[n+1])
			v = v0 ^ (v0^v1)&mask
		}
		if rng < topValue {
			rng <<= 8
			var b byte
			if uint(pos) < uint(len(in)) {
				b = in[pos]
				pos++
			} else {
				b, in, pos = rc.refillAt(pos)
			}
			code = code<<8 | uint32(b)
		}
		if last {
			break
		}
	}
	rc.rng, rc.code, rc.in, rc.pos = rng, code, in, pos
	return m
}

// reverse is BitTreeReverseDecode.
func (rc *rangeDec) reverse(probs []uint16, numBits uint) uint32 {
	rng, code, in, pos := rc.rng, rc.code, rc.in, rc.pos
	m := uint32(1)
	var sym uint32
	for i := uint(0); i < numBits; i++ {
		p := &probs[m]
		v := uint32(*p)
		bound := (rng >> numBitModelTotalBits) * v
		if code < bound {
			*p = uint16(v + ((1<<numBitModelTotalBits)-v)>>numMoveBits)
			rng = bound
			m <<= 1
		} else {
			*p = uint16(v - v>>numMoveBits)
			code -= bound
			rng -= bound
			m = m<<1 + 1
			sym |= 1 << i
		}
		if rng < topValue {
			rng <<= 8
			var b byte
			if uint(pos) < uint(len(in)) {
				b = in[pos]
				pos++
			} else {
				b, in, pos = rc.refillAt(pos)
			}
			code = code<<8 | uint32(b)
		}
	}
	rc.rng, rc.code, rc.in, rc.pos = rng, code, in, pos
	return sym
}

// matchedLiteral is the first loop of CLzmaDecoder::DecodeLiteral after a
// match: bits are decoded with the match byte's bit as context until one
// differs. It returns the partial symbol for treeFrom to finish.
func (rc *rangeDec) matchedLiteral(probs []uint16, matchByte uint32) uint32 {
	rng, code, in, pos := rc.rng, rc.code, rc.in, rc.pos
	sym := uint32(1)
	for {
		matchBit := (matchByte >> 7) & 1
		matchByte <<= 1
		p := &probs[(1+matchBit)<<8+sym]
		v := uint32(*p)
		bound := (rng >> numBitModelTotalBits) * v
		var bit uint32
		if code < bound {
			*p = uint16(v + ((1<<numBitModelTotalBits)-v)>>numMoveBits)
			rng = bound
		} else {
			*p = uint16(v - v>>numMoveBits)
			code -= bound
			rng -= bound
			bit = 1
		}
		if rng < topValue {
			rng <<= 8
			var b byte
			if uint(pos) < uint(len(in)) {
				b = in[pos]
				pos++
			} else {
				b, in, pos = rc.refillAt(pos)
			}
			code = code<<8 | uint32(b)
		}
		sym = sym<<1 | bit
		if matchBit != bit || sym >= 0x100 {
			break
		}
	}
	rc.rng, rc.code, rc.in, rc.pos = rng, code, in, pos
	return sym
}

func initProbs(p []uint16) {
	for i := range p {
		p[i] = probInit
	}
}
