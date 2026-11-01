package xz

import (
	"hash/crc32"
	"io"

	"github.com/go-compressions/xz/internal/xerr"
)

// parseVLI decodes a variable-length integer (xz-file-format.txt, 1.2) from
// the start of b and returns it with its encoded length. Non-minimal encodings
// (a zero last byte after a continuation) and values of 2^63 or more are
// rejected, as is a number that is cut off by the end of b.
func parseVLI(b []byte) (v uint64, n int, err error) {
	for n < 9 {
		if n >= len(b) {
			return 0, 0, xerr.ErrTruncated
		}
		c := b[n]
		v |= uint64(c&0x7F) << (7 * uint(n))
		n++
		if c&0x80 == 0 {
			if c == 0 && n > 1 {
				return 0, 0, xerr.Corrupt("non-minimal variable-length integer")
			}
			return v, n, nil
		}
	}
	return 0, 0, xerr.Corrupt("variable-length integer longer than 9 bytes")
}

func appendVLI(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// indexReader reads the Index field with a running CRC32 and a byte count.
type indexReader struct {
	r   io.Reader
	crc uint32
	n   int64
	one [1]byte
}

func (x *indexReader) byte() (byte, error) {
	if _, err := io.ReadFull(x.r, x.one[:]); err != nil {
		return 0, xerr.Truncate(err)
	}
	x.crc = crc32.Update(x.crc, crc32.IEEETable, x.one[:])
	x.n++
	return x.one[0], nil
}

func (x *indexReader) vli() (uint64, error) {
	var buf [9]byte
	for i := 0; i < 9; i++ {
		c, err := x.byte()
		if err != nil {
			return 0, err
		}
		buf[i] = c
		if c&0x80 == 0 {
			v, _, err := parseVLI(buf[:i+1])
			return v, err
		}
	}
	return 0, xerr.Corrupt("variable-length integer longer than 9 bytes")
}
