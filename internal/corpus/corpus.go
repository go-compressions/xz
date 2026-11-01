// Package corpus generates the deterministic inputs used by the tests and by
// the fixture generator (internal/mkfixtures). Fixtures committed under
// testdata/ are compressed forms of these inputs, so the expected plain data
// is regenerated and never stored.
package corpus

import "encoding/binary"

// Names lists the generators accepted by Make.
var Names = []string{
	"text", "random", "mixed", "audio",
	"x86", "arm", "armthumb", "ppc", "sparc", "ia64", "arm64", "riscv",
}

type rng uint64

func (r *rng) next() uint64 { // xorshift64*
	x := uint64(*r)
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	*r = rng(x)
	return x * 0x2545F4914F6CDD1D
}

func (r *rng) intn(n int) int { return int(r.next() >> 33 % uint64(n)) }

func newRNG(seed uint64) *rng {
	r := rng(seed*0x9E3779B97F4A7C15 + 0x1234567)
	return &r
}

var words = []string{
	"the", "of", "and", "compression", "dictionary", "window", "stream", "block",
	"decoder", "memory", "follows", "data", "header", "declared", "size", "lzma",
	"range", "coder", "literal", "match", "distance", "length", "state", "chunk",
}

// Make returns n bytes (rounded down to a multiple of 16) of the named kind.
func Make(name string, n int, seed uint64) []byte {
	n &^= 15
	r := newRNG(seed)
	out := make([]byte, 0, n+64)
	switch name {
	case "text":
		for len(out) < n {
			out = append(out, words[r.intn(len(words))]...)
			out = append(out, ' ')
			if r.intn(12) == 0 {
				out = append(out, '\n')
			}
		}
	case "random":
		for len(out) < n {
			out = binary.LittleEndian.AppendUint64(out, r.next())
		}
	case "mixed":
		for len(out) < n {
			switch r.intn(4) {
			case 0:
				for i := r.intn(200) + 1; i > 0; i-- {
					out = append(out, byte(r.next()>>40))
				}
			case 1:
				b := byte(r.next() >> 40)
				for i := r.intn(300) + 1; i > 0; i-- {
					out = append(out, b)
				}
			case 2:
				for i := r.intn(30) + 1; i > 0; i-- {
					out = append(out, words[r.intn(len(words))]...)
					out = append(out, ' ')
				}
			default:
				if len(out) > 64 {
					start := r.intn(len(out) - 32)
					for i := r.intn(100) + 4; i > 0; i-- {
						out = append(out, out[start])
						start++
					}
				}
			}
		}
	case "audio":
		v := 0
		for len(out) < n {
			v += r.intn(2001) - 1000
			out = binary.LittleEndian.AppendUint16(out, uint16(v))
		}
	case "x86":
		for len(out) < n {
			if r.intn(3) == 0 {
				op := byte(0xE8)
				if r.intn(3) == 0 {
					op = 0xE9
				}
				rel := uint32(r.intn(1 << 20))
				if r.intn(2) == 0 {
					rel = -rel
				}
				out = append(out, op)
				out = binary.LittleEndian.AppendUint32(out, rel)
			} else {
				for i := r.intn(8) + 1; i > 0; i-- {
					out = append(out, byte(r.next()>>40))
				}
			}
		}
	case "arm":
		for len(out) < n {
			w := uint32(r.next() >> 20)
			if r.intn(4) == 0 {
				w = w&0x00FFFFFF | 0xEB000000
			}
			out = binary.LittleEndian.AppendUint32(out, w)
		}
	case "armthumb":
		for len(out) < n {
			if r.intn(4) == 0 {
				hi := uint16(0xF000 | r.intn(0x800))
				lo := uint16(0xF800 | r.intn(0x800))
				out = binary.LittleEndian.AppendUint16(out, hi)
				out = binary.LittleEndian.AppendUint16(out, lo)
			} else {
				out = binary.LittleEndian.AppendUint16(out, uint16(r.next()>>40))
			}
		}
	case "ppc":
		for len(out) < n {
			w := uint32(r.next() >> 20)
			if r.intn(4) == 0 {
				w = 0x48000001 | w&0x03FFFFFC
			}
			out = binary.BigEndian.AppendUint32(out, w)
		}
	case "sparc":
		for len(out) < n {
			w := uint32(r.next() >> 20)
			switch r.intn(4) {
			case 0:
				w = 0x40000000 | w&0x003FFFFF
			case 1:
				w = 0x7FC00000 | w&0x003FFFFF
			}
			out = binary.BigEndian.AppendUint32(out, w)
		}
	case "ia64":
		tmpl := []byte{0x10, 0x11, 0x12, 0x13, 0x16, 0x17, 0x18, 0x19, 0x1C, 0x1D, 0x05}
		for len(out) < n {
			var b [16]byte
			binary.LittleEndian.PutUint64(b[0:], r.next())
			binary.LittleEndian.PutUint64(b[8:], r.next())
			t := tmpl[r.intn(len(tmpl))]
			b[0] = b[0]&0xE0 | t
			for slot := 0; slot < 3; slot++ {
				if r.intn(2) == 0 {
					setBits(b[:], 5+41*slot+37, 4, 5) // opcode 5
					setBits(b[:], 5+41*slot+9, 3, 0)  // btype 0
				}
			}
			out = append(out, b[:]...)
		}
	case "arm64":
		for len(out) < n {
			w := uint32(r.next() >> 20)
			switch r.intn(4) {
			case 0:
				w = 0x94000000 | w&0x03FFFFFF
			case 1:
				w = 0x90000000 | w&0x60FFFFFF
			}
			out = binary.LittleEndian.AppendUint32(out, w)
		}
	case "riscv":
		for len(out) < n {
			rd := uint32(1 + r.intn(31))
			switch r.intn(6) {
			case 0: // JAL ra / t0 / other
				rds := []uint32{1, 5, 3}
				w := uint32(r.next()>>20)&0xFFFFF000 | rds[r.intn(3)]<<7 | 0x6F
				out = binary.LittleEndian.AppendUint32(out, w)
			case 1, 2: // AUIPC + paired instruction
				imm := uint32(r.next()>>20) & 0xFFFFF000
				out = binary.LittleEndian.AppendUint32(out, imm|rd<<7|0x17)
				rs1 := rd
				if r.intn(8) == 0 {
					rs1 = uint32(1 + r.intn(31))
				}
				i2 := uint32(r.next()>>20)&0xFFF00000 | rs1<<15 | uint32(r.intn(8))<<12 | rd<<7 | 0x03
				out = binary.LittleEndian.AppendUint32(out, i2)
			case 3: // AUIPC with rd = x0 (landing pad) or x2 (the filter's own form)
				imm := uint32(r.next()>>20) & 0xFFFFF000
				rd = uint32(2 * r.intn(2))
				out = binary.LittleEndian.AppendUint32(out, imm|rd<<7|0x17)
				out = binary.LittleEndian.AppendUint32(out, uint32(r.next()>>20))
			case 4: // compressed instructions
				out = binary.LittleEndian.AppendUint16(out, uint16(r.next()>>40)&^3|1)
				out = binary.LittleEndian.AppendUint16(out, uint16(r.next()>>40)&^3|1)
			default:
				out = binary.LittleEndian.AppendUint32(out, uint32(r.next()>>20))
			}
		}
	default:
		panic("corpus: unknown kind " + name)
	}
	return out[:n]
}

// setBits stores the low width bits of v at bit offset pos (little endian
// bit order) of b.
func setBits(b []byte, pos, width int, v uint) {
	for i := 0; i < width; i++ {
		bit := pos + i
		if v>>uint(i)&1 == 1 {
			b[bit/8] |= 1 << uint(bit%8)
		} else {
			b[bit/8] &^= 1 << uint(bit%8)
		}
	}
}
