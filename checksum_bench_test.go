package xz

import (
	stdcrc64 "hash/crc64"
	"strconv"
	"testing"

	simdcrc64 "github.com/go-simd/crc64"
)

// BenchmarkCRC64 compares the standard library's CRC-64/ECMA with the go-simd
// kernel the reader now uses.
func BenchmarkCRC64(b *testing.B) {
	stdTab := stdcrc64.MakeTable(stdcrc64.ECMA)
	simdTab := simdcrc64.MakeTable(simdcrc64.ECMA)
	for _, n := range []int{4 << 10, 1 << 20} {
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = byte(i*131 + i>>8)
		}
		name := strconv.Itoa(n>>10) + "KiB"
		b.Run("std/"+name, func(b *testing.B) {
			b.SetBytes(int64(n))
			for b.Loop() {
				_ = stdcrc64.Checksum(buf, stdTab)
			}
		})
		b.Run("simd/"+name, func(b *testing.B) {
			b.SetBytes(int64(n))
			for b.Loop() {
				_ = simdcrc64.Checksum(buf, simdTab)
			}
		})
	}
}

// TestCRC64MatchesStdlib pins the kernel to the standard library over many
// lengths, in one shot and through the streaming hash.
func TestCRC64MatchesStdlib(t *testing.T) {
	buf := make([]byte, 70000)
	for i := range buf {
		buf[i] = byte(i*7 + i>>9)
	}
	stdTab := stdcrc64.MakeTable(stdcrc64.ECMA)
	simdTab := simdcrc64.MakeTable(simdcrc64.ECMA)
	for _, n := range []int{0, 1, 7, 8, 15, 16, 17, 63, 64, 65, 127, 128, 129, 1000, 65536, 70000} {
		if got, want := simdcrc64.Checksum(buf[:n], simdTab), stdcrc64.Checksum(buf[:n], stdTab); got != want {
			t.Fatalf("n=%d: simd %#x, stdlib %#x", n, got, want)
		}
		h := simdcrc64.New(simdTab)
		h.Write(buf[:n/2])
		h.Write(buf[n/2 : n])
		if got, want := h.Sum64(), stdcrc64.Checksum(buf[:n], stdTab); got != want {
			t.Fatalf("n=%d streamed: simd %#x, stdlib %#x", n, got, want)
		}
	}
}
