package xz_test

import (
	"bytes"
	"io"
	"testing"

	uxz "github.com/ulikunitz/xz"
	ulzma "github.com/ulikunitz/xz/lzma"

	"github.com/go-compressions/xz/internal/corpus"
)

func uxzEncode(t testing.TB, data []byte, cfg uxz.WriterConfig) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Streams written by ulikunitz/xz, the library this one replaces on the decode
// side, decode to the same bytes with all checks and several block layouts.
func TestAgainstUlikunitzWriter(t *testing.T) {
	configs := map[string]uxz.WriterConfig{
		"default":      {},
		"crc32":        {CheckSum: uxz.CRC32},
		"sha256":       {CheckSum: uxz.SHA256},
		"no checksum":  {NoCheckSum: true},
		"blocks":       {BlockSize: 20000, DictCap: 1 << 16},
		"small dict":   {DictCap: 1 << 16},
		"lc0 lp2 pb0":  {Properties: &ulzma.Properties{LC: 0, LP: 2, PB: 0}},
		"lc4 lp0 pb4":  {Properties: &ulzma.Properties{LC: 4, LP: 0, PB: 4}},
		"binary tree":  {Matcher: ulzma.BinaryTree},
		"large blocks": {BlockSize: 100000, CheckSum: uxz.CRC64},
	}
	for _, kind := range []string{"text", "mixed", "random", "audio"} {
		data := corpus.Make(kind, 250000, 7)
		for name, cfg := range configs {
			t.Run(kind+"/"+name, func(t *testing.T) {
				enc := uxzEncode(t, data, cfg)
				got := mustDecode(t, enc)
				if !bytes.Equal(got, data) {
					t.Fatal("output differs")
				}
				// ulikunitz's own reader agrees.
				r, err := uxz.NewReader(bytes.NewReader(enc))
				if err != nil {
					t.Fatal(err)
				}
				want, err := io.ReadAll(r)
				if err != nil || !bytes.Equal(want, got) {
					t.Fatalf("oracle: %v", err)
				}
			})
		}
	}
}
