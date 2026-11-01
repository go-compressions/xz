// Command bombdemo prints how much memory this library and
// github.com/ulikunitz/xz allocate to decode a few bytes of data stored in a
// block whose header declares a large LZMA2 dictionary.
//
// Run it under GOMEMLIMIT. The ulikunitz/xz measurement allocates what the
// header declares (lzma/buffer.go: make([]byte, size+1)), so only the sizes
// given with -ulikunitz are tried on it, and never above 1.5 GiB by default.
//
//	GOMEMLIMIT=2GiB go run ./internal/bombdemo
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"hash/crc64"
	"io"
	"os"
	"runtime"

	uxz "github.com/ulikunitz/xz"

	"github.com/go-compressions/xz"
)

// stream builds an xz stream with one block of LZMA2 stored data and the
// given dictionary property.
func stream(data []byte, prop byte) []byte {
	comp := append([]byte{1, byte((len(data) - 1) >> 8), byte(len(data) - 1)}, data...)
	comp = append(comp, 0)
	// Block header of 12 bytes (size byte 2): flags, filter ID, properties
	// size, the dictionary property, three bytes of padding, CRC32.
	h := []byte{2, 0, 0x21, 1, prop, 0, 0, 0}
	h = binary.LittleEndian.AppendUint32(h, crc32.ChecksumIEEE(h))

	blk := append([]byte(nil), h...)
	blk = append(blk, comp...)
	unpadded := len(blk) + 8
	for len(blk)%4 != 0 {
		blk = append(blk, 0)
	}
	blk = binary.LittleEndian.AppendUint64(blk, crc64.Checksum(data, crc64.MakeTable(crc64.ECMA)))

	idx := []byte{0, 1, byte(unpadded), byte(len(data))}
	idx = binary.LittleEndian.AppendUint32(idx, crc32.ChecksumIEEE(idx))
	flags := []byte{0, 4}
	s := append([]byte{0xFD, '7', 'z', 'X', 'Z', 0}, flags...)
	s = binary.LittleEndian.AppendUint32(s, crc32.ChecksumIEEE(flags))
	s = append(s, blk...)
	s = append(s, idx...)
	foot := binary.LittleEndian.AppendUint32(nil, uint32(len(idx)/4-1))
	foot = append(foot, flags...)
	s = binary.LittleEndian.AppendUint32(s, crc32.ChecksumIEEE(foot))
	s = append(s, foot...)
	return append(s, 'Y', 'Z')
}

func measure(f func() error) (uint64, error) {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	err := f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc, err
}

func main() {
	maxU := flag.Int("ulikunitz", 37, "largest dictionary property tried on ulikunitz/xz (37 = 1.5 GiB)")
	flag.Parse()
	data := []byte("The quick brown fox jumps over the lazy dog.")
	fmt.Printf("%d bytes of data in one block\n\n| declared dictionary | this library | ulikunitz/xz v0.5.17 |\n|---|---|---|\n", len(data))
	for _, p := range []struct {
		prop byte
		name string
	}{{36, "1 GiB"}, {37, "1.5 GiB"}, {38, "2 GiB"}, {39, "3 GiB"}, {40, "4 GiB - 1"}} {
		s := stream(data, p.prop)
		mine, err := measure(func() error {
			r, err := xz.NewReader(bytes.NewReader(s))
			if err != nil {
				return err
			}
			got, err := io.ReadAll(r)
			if err == nil && !bytes.Equal(got, data) {
				err = fmt.Errorf("output differs")
			}
			return err
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "this library:", err)
			os.Exit(1)
		}
		theirs := "not run"
		if int(p.prop) <= *maxU {
			n, err := measure(func() error {
				r, err := uxz.NewReader(bytes.NewReader(s))
				if err != nil {
					return err
				}
				_, err = io.ReadAll(r)
				return err
			})
			theirs = fmt.Sprintf("%d bytes", n)
			if err != nil {
				theirs += fmt.Sprintf(" (error: %v)", err)
			}
		}
		fmt.Printf("| %s | %d bytes | %s |\n", p.name, mine, theirs)
	}
}
