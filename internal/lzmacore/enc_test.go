package lzmacore

import "math/bits"

// A symbol-level LZMA encoder for tests: it emits exactly the literals,
// matches, repeats and markers a test asks for, so every path of the decoder
// (valid and invalid) can be reached deterministically. It reuses the
// decoder's probability arrays, whose layout is checked against xz-utils
// output by the fixture tests of the xz package.

type rcEnc struct {
	low       uint64
	rng       uint32
	cache     byte
	cacheSize int64
	out       []byte
}

func newRCEnc() *rcEnc { return &rcEnc{rng: 0xFFFFFFFF, cacheSize: 1} }

func (e *rcEnc) shiftLow() {
	if uint32(e.low) < 0xFF000000 || e.low>>32 != 0 {
		temp := e.cache
		for {
			e.out = append(e.out, temp+byte(e.low>>32))
			temp = 0xFF
			e.cacheSize--
			if e.cacheSize == 0 {
				break
			}
		}
		e.cache = byte(e.low >> 24)
	}
	e.cacheSize++
	e.low = (e.low & 0x00FFFFFF) << 8
}

func (e *rcEnc) norm() {
	for e.rng < topValue {
		e.rng <<= 8
		e.shiftLow()
	}
}

func (e *rcEnc) bit(p *uint16, b uint32) {
	bound := (e.rng >> numBitModelTotalBits) * uint32(*p)
	if b == 0 {
		e.rng = bound
		*p += (1<<numBitModelTotalBits - *p) >> numMoveBits
	} else {
		e.low += uint64(bound)
		e.rng -= bound
		*p -= *p >> numMoveBits
	}
	e.norm()
}

func (e *rcEnc) direct(v uint32, n uint) {
	for i := int(n) - 1; i >= 0; i-- {
		e.rng >>= 1
		if v>>uint(i)&1 == 1 {
			e.low += uint64(e.rng)
		}
		e.norm()
	}
}

func (e *rcEnc) flush() []byte {
	for i := 0; i < 5; i++ {
		e.shiftLow()
	}
	return e.out
}

func (e *rcEnc) tree(probs []uint16, numBits uint, sym uint32) {
	m := uint32(1)
	for i := int(numBits) - 1; i >= 0; i-- {
		b := sym >> uint(i) & 1
		e.bit(&probs[m], b)
		m = m<<1 | b
	}
}

func (e *rcEnc) reverse(probs []uint16, numBits uint, sym uint32) {
	m := uint32(1)
	for i := uint(0); i < numBits; i++ {
		b := sym >> i & 1
		e.bit(&probs[m], b)
		m = m<<1 | b
	}
}

// symEnc keeps the model state of an LZMA encoder. It is driven by symbol
// calls; hist is the data the decoder will have produced.
type symEnc struct {
	Decoder // probabilities and registers, shared layout with the decoder
	rc      *rcEnc
	hist    []byte // everything encoded since the dictionary start
	base    int    // hist[:base] was encoded by earlier chunks
	posBase int    // position counter origin (TotalPos at hist[0])

	// skipCopy encodes copies without modelling their bytes, to emit
	// deliberately invalid symbols (distances the decoder must refuse).
	skipCopy bool
}

func newSymEnc(lc, lp, pb uint) *symEnc {
	e := &symEnc{rc: newRCEnc()}
	e.SetProps(lc, lp, pb)
	e.growLiterals(uint32(1<<(lc+lp)) - 1)
	e.ResetState()
	return e
}

func (e *symEnc) pos() uint32 { return uint32(len(e.hist) + e.posBase) }

func (e *symEnc) posState() uint32 { return e.pos() & e.pbMask }

func (e *symEnc) lit(b byte) {
	ps := e.posState()
	e.rc.bit(&e.isMatch[e.state<<numPosBitsMax+ps], 0)
	var prev uint32
	if len(e.hist) > 0 {
		prev = uint32(e.hist[len(e.hist)-1])
	}
	ls := (e.pos()&e.lpMask)<<e.lc + prev>>(8-e.lc)
	probs := e.litProbs[litBlock*ls : litBlock*ls+litBlock]
	sym := uint32(1)
	if e.state >= 7 {
		mb := uint32(e.hist[len(e.hist)-int(e.rep0)-1])
		same := true
		for i := 7; i >= 0; i-- {
			bit := uint32(b) >> uint(i) & 1
			if same {
				mbit := mb >> uint(i) & 1
				e.rc.bit(&probs[(1+mbit)<<8+sym], bit)
				same = mbit == bit
			} else {
				e.rc.bit(&probs[sym], bit)
			}
			sym = sym<<1 | bit
		}
	} else {
		for i := 7; i >= 0; i-- {
			bit := uint32(b) >> uint(i) & 1
			e.rc.bit(&probs[sym], bit)
			sym = sym<<1 | bit
		}
	}
	e.state = updateLiteral(e.state)
	e.hist = append(e.hist, b)
}

func (e *symEnc) encLen(l *lenDec, n uint32, ps uint32) {
	n -= matchMinLen
	switch {
	case n < 8:
		e.rc.bit(&l.choice, 0)
		e.rc.tree(l.low[ps][:], 3, n)
	case n < 16:
		e.rc.bit(&l.choice, 1)
		e.rc.bit(&l.choice2, 0)
		e.rc.tree(l.mid[ps][:], 3, n-8)
	default:
		e.rc.bit(&l.choice, 1)
		e.rc.bit(&l.choice2, 1)
		e.rc.tree(l.high[:], 8, n-16)
	}
}

// copyBytes appends length bytes from rep0+1 back, as the decoder will.
func (e *symEnc) copyBytes(length int) {
	if e.skipCopy {
		return
	}
	for i := 0; i < length; i++ {
		e.hist = append(e.hist, e.hist[len(e.hist)-int(e.rep0)-1])
	}
}

// match encodes a new match; dist is the 1-based distance.
func (e *symEnc) match(dist uint32, length int) {
	ps := e.posState()
	e.rc.bit(&e.isMatch[e.state<<numPosBitsMax+ps], 1)
	e.rc.bit(&e.isRep[e.state], 0)
	e.encLen(&e.lenDec, uint32(length), ps)
	e.encDist(dist-1, uint32(length)-matchMinLen)
	e.rep3, e.rep2, e.rep1, e.rep0 = e.rep2, e.rep1, e.rep0, dist-1
	e.state = updateMatch(e.state)
	e.copyBytes(length)
}

func (e *symEnc) encDist(dist uint32, lenSym uint32) {
	ls := lenSym
	if ls > numLenToPosStates-1 {
		ls = numLenToPosStates - 1
	}
	var slot uint32
	if dist < 4 {
		slot = dist
	} else {
		n := uint32(bits.Len32(dist)) - 1
		slot = 2*n + dist>>(n-1)&1
	}
	e.rc.tree(e.posSlot[ls][:], 6, slot)
	if slot < 4 {
		return
	}
	nd := uint(slot>>1 - 1)
	base := (2 | slot&1) << nd
	rest := dist - base
	if slot < endPosModelIndex {
		e.rc.reverse(e.posDec[base-slot:], nd, rest)
		return
	}
	e.rc.direct(rest>>numAlignBits, nd-numAlignBits)
	e.rc.reverse(e.align[:], numAlignBits, rest&(1<<numAlignBits-1))
}

// marker encodes the end marker (distance 0xFFFFFFFF).
func (e *symEnc) marker() {
	ps := e.posState()
	e.rc.bit(&e.isMatch[e.state<<numPosBitsMax+ps], 1)
	e.rc.bit(&e.isRep[e.state], 0)
	e.encLen(&e.lenDec, matchMinLen, ps)
	e.encDist(markerDistance, 0)
}

// shortRep encodes a one-byte repeat of rep0.
func (e *symEnc) shortRep() {
	ps := e.posState()
	e.rc.bit(&e.isMatch[e.state<<numPosBitsMax+ps], 1)
	e.rc.bit(&e.isRep[e.state], 1)
	e.rc.bit(&e.isRepG0[e.state], 0)
	e.rc.bit(&e.isRep0Long[e.state<<numPosBitsMax+ps], 0)
	e.state = updateShortRep(e.state)
	e.copyBytes(1)
}

// rep encodes a repeat match using repeat distance register i (0..3).
func (e *symEnc) rep(i int, length int) {
	ps := e.posState()
	e.rc.bit(&e.isMatch[e.state<<numPosBitsMax+ps], 1)
	e.rc.bit(&e.isRep[e.state], 1)
	switch i {
	case 0:
		e.rc.bit(&e.isRepG0[e.state], 0)
		e.rc.bit(&e.isRep0Long[e.state<<numPosBitsMax+ps], 1)
	case 1:
		e.rc.bit(&e.isRepG0[e.state], 1)
		e.rc.bit(&e.isRepG1[e.state], 0)
		e.rep0, e.rep1 = e.rep1, e.rep0
	case 2:
		e.rc.bit(&e.isRepG0[e.state], 1)
		e.rc.bit(&e.isRepG1[e.state], 1)
		e.rc.bit(&e.isRepG2[e.state], 0)
		e.rep0, e.rep1, e.rep2 = e.rep2, e.rep0, e.rep1
	default:
		e.rc.bit(&e.isRepG0[e.state], 1)
		e.rc.bit(&e.isRepG1[e.state], 1)
		e.rc.bit(&e.isRepG2[e.state], 1)
		e.rep0, e.rep1, e.rep2, e.rep3 = e.rep3, e.rep0, e.rep1, e.rep2
	}
	e.encLen(&e.repLenDec, uint32(length), ps)
	e.state = updateRep(e.state)
	e.copyBytes(length)
}

// finish flushes the range coder and returns the encoded bytes.
func (e *symEnc) finish() []byte { return e.rc.flush() }

// data returns the bytes the decoder is expected to produce for this chunk.
func (e *symEnc) data() []byte { return e.hist[e.base:] }
