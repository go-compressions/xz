package xz_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/go-compressions/xz"
)

// A complete .xz file as written by `xz` (CRC64 check, one LZMA2 block).
const helloXZ = "fd377a585a000004e6d6b4460200210116000000742fe5a301000f636875" +
	"6e6b20636f6d7072657373696f00ab31da83b7defdd400012810e50b6c60" +
	"1fb6f37d010000000004595a"

func Example() {
	data, _ := hex.DecodeString(helloXZ)
	r, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		fmt.Println(err)
		return
	}
	out, err := io.ReadAll(r)
	fmt.Printf("%q %v\n", out, err)
	// Output: "chunk compressio" <nil>
}

// MaxDictionary bounds the memory a hostile file can make the reader use: the
// window grows with the decoded data and stops, with ErrTooLarge, at the
// ceiling. A header that merely declares a huge dictionary costs nothing.
func ExampleReaderConfig() {
	data, _ := hex.DecodeString(helloXZ)
	cfg := xz.ReaderConfig{MaxDictionary: 64 << 20, SingleStream: true}
	r, err := cfg.NewReader(bytes.NewReader(data))
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = io.Copy(io.Discard, r)
	fmt.Println(err, errors.Is(err, xz.ErrTooLarge))
	// Output: <nil> false
}
