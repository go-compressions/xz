package xz_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash/crc32"
	"hash/crc64"
)

// Helpers that assemble .xz streams byte by byte, so that tests can build
// valid streams and then break one field at a time. Block data is stored in
// LZMA2 uncompressed chunks: no encoder is needed to reach every path of the
// container reader.

func vli(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func crcOf(b []byte) []byte { return le32(crc32.ChecksumIEEE(b)) }

func streamHeader(flags1 byte) []byte {
	h := []byte{0xFD, '7', 'z', 'X', 'Z', 0}
	h = append(h, 0, flags1)
	return append(h, crcOf([]byte{0, flags1})...)
}

func streamFooter(flags1 byte, indexSize int) []byte {
	body := append(le32(uint32(indexSize/4-1)), 0, flags1)
	f := append(crcOf(body), body...)
	return append(f, 'Y', 'Z')
}

type filterSpec struct {
	id    uint64
	props []byte
}

// lzma2 returns the LZMA2 filter with the given dictionary property.
func lzma2(prop byte) filterSpec { return filterSpec{0x21, []byte{prop}} }

// blockHeader builds a block header; csize and usize are written when
// non-nil. The result is padded and carries a valid CRC32.
func blockHeader(csize, usize *uint64, filters ...filterSpec) []byte {
	flags := byte(len(filters) - 1)
	var body []byte
	if csize != nil {
		flags |= 0x40
		body = append(body, vli(*csize)...)
	}
	if usize != nil {
		flags |= 0x80
		body = append(body, vli(*usize)...)
	}
	for _, f := range filters {
		body = append(body, vli(f.id)...)
		body = append(body, vli(uint64(len(f.props)))...)
		body = append(body, f.props...)
	}
	return finishBlockHeader(flags, body)
}

// finishBlockHeader pads (flags, body) to a multiple of four and appends the
// size byte at the front and the CRC32 at the end.
func finishBlockHeader(flags byte, body []byte) []byte {
	n := 2 + len(body) + 4
	for n%4 != 0 {
		body = append(body, 0)
		n++
	}
	h := append([]byte{byte(n/4 - 1), flags}, body...)
	return append(h, crcOf(h)...)
}

// refix recomputes the CRC32 of a block header after a test changed it.
func refix(h []byte) []byte {
	copy(h[len(h)-4:], crcOf(h[:len(h)-4]))
	return h
}

// stored wraps data in LZMA2 uncompressed chunks, ending with the 0x00 marker.
func stored(data []byte) []byte {
	var out []byte
	first := true
	for len(data) > 0 {
		n := min(len(data), 1<<16)
		ctl := byte(2)
		if first {
			ctl, first = 1, false
		}
		out = append(out, ctl, byte((n-1)>>8), byte(n-1))
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return append(out, 0)
}

func checkSum(id byte, data []byte) []byte {
	switch id {
	case 1:
		return crcOf(data)
	case 4:
		return binary.LittleEndian.AppendUint64(nil, crc64.Checksum(data, crc64.MakeTable(crc64.ECMA)))
	case 10:
		s := sha256.Sum256(data)
		return s[:]
	}
	return nil
}

func checkLen(id byte) int { return len(checkSum(id, nil)) }

type block struct {
	bytes    []byte // the whole block
	unpadded uint64
	usize    uint64
}

// makeBlock builds a block whose LZMA2 data is `comp` and whose plain data
// (for the check) is `plain`.
func makeBlock(check byte, header, comp, plain []byte) block {
	b := append([]byte(nil), header...)
	b = append(b, comp...)
	unpadded := uint64(len(b)) + uint64(checkLen(check))
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	b = append(b, checkSum(check, plain)...)
	return block{b, unpadded, uint64(len(plain))}
}

// simpleBlock is a block of stored data with an LZMA2 dictionary property.
func simpleBlock(check byte, plain []byte, prop byte) block {
	comp := stored(plain)
	c, u := uint64(len(comp)), uint64(len(plain))
	return makeBlock(check, blockHeader(&c, &u, lzma2(prop)), comp, plain)
}

func indexOf(blocks []block) []byte {
	idx := append([]byte{0}, vli(uint64(len(blocks)))...)
	for _, b := range blocks {
		idx = append(idx, vli(b.unpadded)...)
		idx = append(idx, vli(b.usize)...)
	}
	for len(idx)%4 != 0 {
		idx = append(idx, 0)
	}
	return append(idx, crcOf(idx)...)
}

func buildStream(check byte, blocks ...block) []byte {
	s := streamHeader(check)
	for _, b := range blocks {
		s = append(s, b.bytes...)
	}
	idx := indexOf(blocks)
	s = append(s, idx...)
	return append(s, streamFooter(check, len(idx))...)
}

// parts returns a valid single-block stream split into its fields, so tests
// can replace one of them.
type parts struct {
	header, blk, index, footer []byte
}

func (p parts) join() []byte {
	var b bytes.Buffer
	for _, f := range [][]byte{p.header, p.blk, p.index, p.footer} {
		b.Write(f)
	}
	return b.Bytes()
}

func partsOf(check byte, plain []byte) parts {
	b := simpleBlock(check, plain, 0)
	idx := indexOf([]block{b})
	return parts{streamHeader(check), b.bytes, idx, streamFooter(check, len(idx))}
}
