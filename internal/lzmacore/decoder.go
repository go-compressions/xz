package lzmacore

import (
	"github.com/go-compressions/xz/internal/xerr"
)

const (
	numPosBitsMax       = 4
	numStates           = 12
	numLenToPosStates   = 4
	numAlignBits        = 4
	endPosModelIndex    = 14
	numFullDistances    = 1 << (endPosModelIndex >> 1)
	matchMinLen         = 2
	litBlock            = 0x300
	minLiteralStates    = 16
	markerDistance      = 0xFFFFFFFF
	minRun              = 4 << 10
	maxLCPlusLP         = 12
	maxLiteralStatesLog = maxLCPlusLP
)

type lenDec struct {
	choice, choice2 uint16
	low, mid        [1 << numPosBitsMax][8]uint16
	high            [256]uint16
}

func (l *lenDec) init() {
	l.choice, l.choice2 = probInit, probInit
	for i := range l.low {
		initProbs(l.low[i][:])
		initProbs(l.mid[i][:])
	}
	initProbs(l.high[:])
}

func (l *lenDec) decode(rc *rangeDec, posState uint32) uint32 {
	if rc.bit(&l.choice) == 0 {
		return rc.tree(l.low[posState][:], 3)
	}
	if rc.bit(&l.choice2) == 0 {
		return 8 + rc.tree(l.mid[posState][:], 3)
	}
	return 16 + rc.tree(l.high[:], 8)
}

type status int

const (
	// stDone: the segment (LZMA2 chunk, or the whole LZMA stream) has ended.
	stDone status = iota
	// stRoom: output reached the limit; decode again with more room.
	stRoom
)

// Decoder is the reference CLzmaDecoder: range decoder, probability models,
// the LZ window and the repeat/state registers.
type Decoder struct {
	rc  rangeDec
	win window

	lc, lp, pb uint
	lpMask     uint32
	pbMask     uint32

	litProbs  []uint16
	litStates int // number of initialised literal states in litProbs

	isMatch    [numStates << numPosBitsMax]uint16
	isRep      [numStates]uint16
	isRepG0    [numStates]uint16
	isRepG1    [numStates]uint16
	isRepG2    [numStates]uint16
	isRep0Long [numStates << numPosBitsMax]uint16
	posSlot    [numLenToPosStates][64]uint16
	posDec     [1 + numFullDistances - endPosModelIndex]uint16
	align      [1 << numAlignBits]uint16
	lenDec     lenDec
	repLenDec  lenDec

	state                  uint32
	rep0, rep1, rep2, rep3 uint32

	// Output not yet written because the window had no room: a literal
	// (pendLit) or the tail of a match at distance rep0+1.
	pend    int
	pendLit bool
	litByte byte

	// The three unpackSize modes of the reference: unknown (marker
	// mandatory), known with an optional marker, and known without marker
	// (LZMA2 chunks, noMarker).
	known           bool
	unpack          uint64
	markerMandatory bool
	noMarker        bool

	needRoom bool
	finished bool
	err      error
}

// SetProps sets lc, lp and pb. The caller has validated them.
func (d *Decoder) SetProps(lc, lp, pb uint) {
	d.lc, d.lp, d.pb = lc, lp, pb
	d.lpMask = 1<<lp - 1
	d.pbMask = 1<<pb - 1
	d.litProbs = d.litProbs[:0]
	d.litStates = 0
}

// ResetState is the reference Init(): every probability back to one half,
// state and repeat distances zero.
func (d *Decoder) ResetState() {
	initProbs(d.litProbs[:d.litStates*litBlock])
	initProbs(d.isMatch[:])
	initProbs(d.isRep[:])
	initProbs(d.isRepG0[:])
	initProbs(d.isRepG1[:])
	initProbs(d.isRepG2[:])
	initProbs(d.isRep0Long[:])
	for i := range d.posSlot {
		initProbs(d.posSlot[i][:])
	}
	initProbs(d.posDec[:])
	initProbs(d.align[:])
	d.lenDec.init()
	d.repLenDec.init()
	d.state = 0
	d.rep0, d.rep1, d.rep2, d.rep3 = 0, 0, 0, 0
	d.pend, d.pendLit = 0, false
}

// growLiterals initialises literal states up to and including ls.
func (d *Decoder) growLiterals(ls uint32) {
	total := 1 << (d.lc + d.lp)
	n := d.litStates * 2
	if n < minLiteralStates {
		n = minLiteralStates
	}
	if n < int(ls)+1 {
		n = int(ls) + 1
	}
	if n > total {
		n = total
	}
	old := len(d.litProbs)
	if cap(d.litProbs) >= n*litBlock {
		d.litProbs = d.litProbs[:n*litBlock]
	} else {
		nb := make([]uint16, n*litBlock)
		copy(nb, d.litProbs)
		d.litProbs = nb
	}
	initProbs(d.litProbs[old:])
	d.litStates = n
}

// Start begins a new LZMA segment on the already prepared range decoder
// input. known/unpack/markerMandatory are the unpackSize modes.
func (d *Decoder) start(known bool, unpack uint64, markerMandatory, noMarker bool) {
	d.known, d.unpack = known, unpack
	d.markerMandatory, d.noMarker = markerMandatory, noMarker
	d.needRoom, d.finished = false, false
	d.err = nil
	d.rc.err = nil
	d.rc.init()
}

func (d *Decoder) fail(err error) (status, error) {
	d.err = err
	return stDone, err
}

func updateLiteral(s uint32) uint32 {
	switch {
	case s < 4:
		return 0
	case s < 10:
		return s - 3
	}
	return s - 6
}

func updateMatch(s uint32) uint32 {
	if s < 7 {
		return 7
	}
	return 10
}

func updateRep(s uint32) uint32 {
	if s < 7 {
		return 8
	}
	return 11
}

func updateShortRep(s uint32) uint32 {
	if s < 7 {
		return 9
	}
	return 11
}

// decodeLiteral is CLzmaDecoder::DecodeLiteral, returning the byte.
func (d *Decoder) decodeLiteral() byte {
	w := &d.win
	rc := &d.rc
	var prev uint32
	if !w.empty() {
		prev = uint32(w.prev())
	}
	ls := (w.total&d.lpMask)<<d.lc + prev>>(8-d.lc)
	if int(ls) >= d.litStates {
		d.growLiterals(ls)
	}
	probs := d.litProbs[litBlock*ls : litBlock*ls+litBlock]
	sym := uint32(1)
	if d.state >= 7 {
		matchByte := uint32(w.at(int(d.rep0) + 1))
		for {
			matchBit := (matchByte >> 7) & 1
			matchByte <<= 1
			bit := rc.bit(&probs[(1+matchBit)<<8+sym])
			sym = sym<<1 | bit
			if matchBit != bit || sym >= 0x100 {
				break
			}
		}
	}
	for sym < 0x100 {
		sym = sym<<1 | rc.bit(&probs[sym])
	}
	return byte(sym - 0x100)
}

// decodeDistance is CLzmaDecoder::DecodeDistance.
func (d *Decoder) decodeDistance(length uint32) uint32 {
	rc := &d.rc
	ls := length
	if ls > numLenToPosStates-1 {
		ls = numLenToPosStates - 1
	}
	slot := rc.tree(d.posSlot[ls][:], 6)
	if slot < 4 {
		return slot
	}
	nd := uint(slot>>1 - 1)
	dist := (2 | slot&1) << nd
	if slot < endPosModelIndex {
		return dist + rc.reverse(d.posDec[dist-slot:], nd)
	}
	dist += rc.direct(nd-numAlignBits) << numAlignBits
	return dist + rc.reverse(d.align[:], numAlignBits)
}

// decode runs the main loop of CLzmaDecoder::Decode until the segment ends,
// an error occurs, or the window position reaches limit with output pending.
//
// Output that does not fit below limit is kept as a pending literal or match
// tail and written first on the next call, so no symbol is ever decoded
// twice and a symbol that writes nothing (the end marker) never needs room.
func (d *Decoder) decode(limit int) (status, error) {
	w := &d.win
	rc := &d.rc
	for {
		if rc.err != nil {
			return d.fail(rc.err)
		}
		if d.pend > 0 {
			if d.pendLit {
				if w.pos >= limit {
					d.needRoom = true
					return stRoom, nil
				}
				w.buf[w.pos] = d.litByte
				w.pos++
				w.total++
				d.pend, d.pendLit = 0, false
			} else {
				n := d.pend
				if room := limit - w.pos; n > room {
					n = room
				}
				if n > 0 {
					w.copyMatch(int(d.rep0)+1, n)
					d.pend -= n
				}
				if d.pend > 0 {
					d.needRoom = true
					return stRoom, nil
				}
			}
		}

		if d.known && d.unpack == 0 && !d.markerMandatory {
			if rc.finishedOK() {
				d.finished = true
				return stDone, nil
			}
			if d.noMarker {
				return d.fail(xerr.Corrupt("lzma2 chunk data exceeds its size"))
			}
		}

		posState := w.total & d.pbMask
		if rc.bit(&d.isMatch[d.state<<numPosBitsMax+posState]) == 0 {
			if d.known && d.unpack == 0 {
				return d.fail(xerr.Corrupt("literal after declared end"))
			}
			b := d.decodeLiteral()
			d.state = updateLiteral(d.state)
			d.unpack--
			if w.pos < limit {
				w.buf[w.pos] = b
				w.pos++
				w.total++
			} else {
				d.pend, d.pendLit, d.litByte = 1, true, b
			}
			continue
		}

		var length uint32
		if rc.bit(&d.isRep[d.state]) != 0 {
			if d.known && d.unpack == 0 {
				return d.fail(xerr.Corrupt("match after declared end"))
			}
			if w.empty() {
				return d.fail(xerr.Corrupt("repeat match in empty window"))
			}
			if rc.bit(&d.isRepG0[d.state]) == 0 {
				if rc.bit(&d.isRep0Long[d.state<<numPosBitsMax+posState]) == 0 {
					d.state = updateShortRep(d.state)
					b := w.at(int(d.rep0) + 1)
					d.unpack--
					if w.pos < limit {
						w.buf[w.pos] = b
						w.pos++
						w.total++
					} else {
						d.pend, d.pendLit, d.litByte = 1, true, b
					}
					continue
				}
			} else {
				var dist uint32
				if rc.bit(&d.isRepG1[d.state]) == 0 {
					dist = d.rep1
				} else {
					if rc.bit(&d.isRepG2[d.state]) == 0 {
						dist = d.rep2
					} else {
						dist = d.rep3
						d.rep3 = d.rep2
					}
					d.rep2 = d.rep1
				}
				d.rep1 = d.rep0
				d.rep0 = dist
			}
			length = d.repLenDec.decode(rc, posState)
			d.state = updateRep(d.state)
		} else {
			d.rep3, d.rep2, d.rep1 = d.rep2, d.rep1, d.rep0
			length = d.lenDec.decode(rc, posState)
			d.state = updateMatch(d.state)
			d.rep0 = d.decodeDistance(length)
			if d.rep0 == markerDistance {
				if rc.err != nil {
					return d.fail(rc.err)
				}
				if !rc.finishedOK() {
					return d.fail(xerr.Corrupt("end marker with trailing range coder state"))
				}
				if d.noMarker || (d.known && d.unpack != 0) {
					return d.fail(xerr.Corrupt("end marker before declared size"))
				}
				d.finished = true
				return stDone, nil
			}
			if d.known && d.unpack == 0 {
				return d.fail(xerr.Corrupt("match after declared end"))
			}
			if uint64(d.rep0) >= w.size || (!w.full && uint64(d.rep0) >= uint64(w.pos)) {
				return d.fail(xerr.Corrupt("match distance beyond the dictionary"))
			}
		}
		length += matchMinLen
		if d.known && d.unpack < uint64(length) {
			return d.fail(xerr.Corrupt("match exceeds declared size"))
		}
		d.unpack -= uint64(length)
		n := int(length)
		if room := limit - w.pos; n > room {
			n = room
		}
		if n > 0 {
			w.copyMatch(int(d.rep0)+1, n)
		}
		d.pend, d.pendLit = int(length)-n, false
	}
}

// makeRoom is called after decode reported stRoom with the window position at
// the end of the buffer: it wraps or grows the window (ErrTooLarge).
func (d *Decoder) makeRoom() error {
	d.needRoom = false
	if d.win.pos == len(d.win.buf) {
		return d.win.advance()
	}
	return nil
}

// Read hands out decoded bytes, decoding more as needed. It returns
// (0, true, nil) once the segment has ended and all its output was handed out.
func (d *Decoder) Read(p []byte) (n int, done bool, err error) {
	w := &d.win
	for {
		if w.rd < w.pos {
			n = copy(p, w.buf[w.rd:w.pos])
			w.rd += n
			return n, false, nil
		}
		if d.err != nil {
			return 0, false, d.err
		}
		if d.finished {
			return 0, true, nil
		}
		if d.needRoom {
			if err := d.makeRoom(); err != nil {
				d.err = err
				return 0, false, err
			}
		}
		want := len(p)
		if want < minRun {
			want = minRun
		}
		limit := len(w.buf)
		if room := limit - w.pos; want < room {
			limit = w.pos + want
		}
		d.decode(limit)
	}
}
