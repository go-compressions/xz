// Package filter implements the non-last filters of the xz format in the
// decoding direction: the Delta filter and the Branch/Call/Jump (BCJ)
// filters for executables, ported from liblzma/simple/*.c and
// liblzma/delta/delta_decoder.c (0BSD) of XZ Utils.
package filter

import "encoding/binary"

// Code converts buf in place for a stream position of now and returns how
// many leading bytes are final. The unprocessed tail is presented again,
// with more bytes after it, on the next call.
type Code func(now uint32, buf []byte) int

func le32(b []byte) uint32       { return binary.LittleEndian.Uint32(b) }
func be32(b []byte) uint32       { return binary.BigEndian.Uint32(b) }
func putLE32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

// ARM decodes the ARM (little endian instruction) filter.
func ARM(now uint32, buf []byte) int {
	size := len(buf) &^ 3
	i := 0
	for ; i < size; i += 4 {
		if buf[i+3] == 0xEB {
			src := uint32(buf[i+2])<<16 | uint32(buf[i+1])<<8 | uint32(buf[i])
			src <<= 2
			dest := src - (now + uint32(i) + 8)
			dest >>= 2
			buf[i+2] = byte(dest >> 16)
			buf[i+1] = byte(dest >> 8)
			buf[i] = byte(dest)
		}
	}
	return i
}

// ARMThumb decodes the ARM Thumb filter.
func ARMThumb(now uint32, buf []byte) int {
	if len(buf) < 4 {
		return 0
	}
	size := len(buf) - 4
	i := 0
	for ; i <= size; i += 2 {
		if buf[i+1]&0xF8 == 0xF0 && buf[i+3]&0xF8 == 0xF8 {
			src := (uint32(buf[i+1])&7)<<19 | uint32(buf[i])<<11 |
				(uint32(buf[i+3])&7)<<8 | uint32(buf[i+2])
			src <<= 1
			dest := src - (now + uint32(i) + 4)
			dest >>= 1
			buf[i+1] = byte(0xF0 | (dest>>19)&0x7)
			buf[i] = byte(dest >> 11)
			buf[i+3] = byte(0xF8 | (dest>>8)&0x7)
			buf[i+2] = byte(dest)
			i += 2
		}
	}
	return i
}

// PowerPC decodes the PowerPC (big endian) filter.
func PowerPC(now uint32, buf []byte) int {
	size := len(buf) &^ 3
	i := 0
	for ; i < size; i += 4 {
		if buf[i]>>2 == 0x12 && buf[i+3]&3 == 1 {
			src := (uint32(buf[i])&3)<<24 | uint32(buf[i+1])<<16 |
				uint32(buf[i+2])<<8 | uint32(buf[i+3])&^3
			dest := src - (now + uint32(i))
			buf[i] = byte(0x48 | (dest>>24)&0x03)
			buf[i+1] = byte(dest >> 16)
			buf[i+2] = byte(dest >> 8)
			buf[i+3] &= 0x03
			buf[i+3] |= byte(dest)
		}
	}
	return i
}

// SPARC decodes the SPARC filter.
func SPARC(now uint32, buf []byte) int {
	size := len(buf) &^ 3
	i := 0
	for ; i < size; i += 4 {
		if (buf[i] == 0x40 && buf[i+1]&0xC0 == 0x00) ||
			(buf[i] == 0x7F && buf[i+1]&0xC0 == 0xC0) {
			src := be32(buf[i:])
			src <<= 2
			dest := src - (now + uint32(i))
			dest >>= 2
			dest = ((0-((dest>>22)&1))<<22)&0x3FFFFFFF | dest&0x3FFFFF | 0x40000000
			buf[i] = byte(dest >> 24)
			buf[i+1] = byte(dest >> 16)
			buf[i+2] = byte(dest >> 8)
			buf[i+3] = byte(dest)
		}
	}
	return i
}

var ia64BranchTable = [32]uint32{
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	4, 4, 6, 6, 0, 0, 7, 7,
	4, 4, 0, 0, 4, 4, 0, 0,
}

// IA64 decodes the Itanium filter.
func IA64(now uint32, buf []byte) int {
	size := len(buf) &^ 15
	i := 0
	for ; i < size; i += 16 {
		mask := ia64BranchTable[buf[i]&0x1F]
		bitPos := uint32(5)
		for slot := 0; slot < 3; slot, bitPos = slot+1, bitPos+41 {
			if (mask>>slot)&1 == 0 {
				continue
			}
			bytePos := int(bitPos >> 3)
			bitRes := bitPos & 7
			var instruction uint64
			for j := 0; j < 6; j++ {
				instruction += uint64(buf[i+j+bytePos]) << (8 * j)
			}
			norm := instruction >> bitRes
			if (norm>>37)&0xF == 0x5 && (norm>>9)&0x7 == 0 {
				src := uint32((norm >> 13) & 0xFFFFF)
				src |= uint32((norm>>36)&1) << 20
				src <<= 4
				dest := src - (now + uint32(i))
				dest >>= 4
				norm &^= uint64(0x8FFFFF) << 13
				norm |= uint64(dest&0xFFFFF) << 13
				norm |= uint64(dest&0x100000) << (36 - 20)
				instruction &= (1 << bitRes) - 1
				instruction |= norm << bitRes
				for j := 0; j < 6; j++ {
					buf[i+j+bytePos] = byte(instruction >> (8 * j))
				}
			}
		}
	}
	return i
}

// ARM64 decodes the ARM64 filter (BL and ADRP).
func ARM64(now uint32, buf []byte) int {
	size := len(buf) &^ 3
	i := 0
	for ; i < size; i += 4 {
		pc := now + uint32(i)
		instr := le32(buf[i:])
		if instr>>26 == 0x25 {
			src := instr
			instr = 0x94000000
			pc >>= 2
			pc = 0 - pc
			instr |= (src + pc) & 0x03FFFFFF
			putLE32(buf[i:], instr)
		} else if instr&0x9F000000 == 0x90000000 {
			src := (instr>>29)&3 | (instr>>3)&0x001FFFFC
			if (src+0x00020000)&0x001C0000 != 0 {
				continue
			}
			instr &= 0x9000001F
			pc >>= 12
			pc = 0 - pc
			dest := src + pc
			instr |= (dest & 3) << 29
			instr |= (dest & 0x0003FFFC) << 3
			instr |= (0 - (dest & 0x00020000)) & 0x00E00000
			putLE32(buf[i:], instr)
		}
	}
	return i
}

// X86 is the stateful x86 BCJ filter.
type X86 struct {
	prevMask uint32
	prevPos  uint32
}

// NewX86 returns the x86 filter in its initial state.
func NewX86() *X86 { return &X86{prevPos: ^uint32(0) - 4} } // (uint32)(-5)

var x86MaskToBit = [5]uint32{0, 1, 2, 2, 3}

func test86(b byte) bool { return b == 0 || b == 0xFF }

// Code decodes buf; see liblzma/simple/x86.c.
func (s *X86) Code(now uint32, buf []byte) int {
	prevMask := s.prevMask
	prevPos := s.prevPos
	if len(buf) < 5 {
		return 0
	}
	if now-prevPos > 5 {
		prevPos = now - 5
	}
	limit := len(buf) - 5
	pos := 0
	for pos <= limit {
		b := buf[pos]
		if b != 0xE8 && b != 0xE9 {
			pos++
			continue
		}
		offset := now + uint32(pos) - prevPos
		prevPos = now + uint32(pos)
		if offset > 5 {
			prevMask = 0
		} else {
			for i := uint32(0); i < offset; i++ {
				prevMask &= 0x77
				prevMask <<= 1
			}
		}
		b = buf[pos+4]
		if test86(b) && prevMask>>1 <= 4 && prevMask>>1 != 3 {
			src := uint32(b)<<24 | uint32(buf[pos+3])<<16 | uint32(buf[pos+2])<<8 | uint32(buf[pos+1])
			var dest uint32
			for {
				dest = src - (now + uint32(pos) + 5)
				if prevMask == 0 {
					break
				}
				i := x86MaskToBit[prevMask>>1]
				b = byte(dest >> (24 - i*8))
				if !test86(b) {
					break
				}
				src = dest ^ (uint32(1)<<(32-i*8) - 1)
			}
			buf[pos+4] = byte(^(((dest >> 24) & 1) - 1))
			buf[pos+3] = byte(dest >> 16)
			buf[pos+2] = byte(dest >> 8)
			buf[pos+1] = byte(dest)
			pos += 5
			prevMask = 0
		} else {
			pos++
			prevMask |= 1
			if test86(b) {
				prevMask |= 0x10
			}
		}
	}
	s.prevMask = prevMask
	s.prevPos = prevPos
	return pos
}

func notAUIPCPair(auipc, inst2 uint32) bool {
	return ((auipc<<8)^(inst2-3))&0xF8003 != 0
}

func notSpecialAUIPC(auipc, rs1 uint32) bool {
	return (auipc-0x3117)<<18 >= rs1&0x1D
}

// RISCV decodes the RISC-V filter (JAL and AUIPC pairs).
func RISCV(now uint32, buf []byte) int {
	if len(buf) < 8 {
		return 0
	}
	size := len(buf) - 8
	i := 0
	for ; i <= size; i += 2 {
		inst := uint32(buf[i])
		if inst == 0xEF {
			b1 := uint32(buf[i+1])
			if b1&0x0D != 0 {
				continue
			}
			b2 := uint32(buf[i+2])
			b3 := uint32(buf[i+3])
			pc := now + uint32(i)
			addr := (b1&0xF0)<<13 | b2<<9 | b3<<1
			addr -= pc
			buf[i+1] = byte(b1&0x0F | (addr>>8)&0xF0)
			buf[i+2] = byte((addr>>16)&0x0F | (addr>>7)&0x10 | (addr<<4)&0xE0)
			buf[i+3] = byte((addr>>4)&0x7F | (addr>>13)&0x80)
			i += 2
		} else if inst&0x7F == 0x17 {
			inst |= uint32(buf[i+1])<<8 | uint32(buf[i+2])<<16 | uint32(buf[i+3])<<24
			var inst2 uint32
			if inst&0xE80 != 0 {
				inst2 = le32(buf[i+4:])
				if notAUIPCPair(inst, inst2) {
					i += 4
					continue
				}
				addr := inst & 0xFFFFF000
				addr += inst2 >> 20
				inst = 0x17 | 2<<7 | inst2<<12
				inst2 = addr
			} else {
				rs1 := inst >> 27
				if notSpecialAUIPC(inst, rs1) {
					i += 2
					continue
				}
				addr := be32(buf[i+4:])
				addr -= now + uint32(i)
				inst2 = inst>>12 | addr<<20
				inst = 0x17 | rs1<<7 | (addr+0x800)&0xFFFFF000
			}
			putLE32(buf[i:], inst)
			putLE32(buf[i+4:], inst2)
			i += 6
		}
	}
	return i
}
