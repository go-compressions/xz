package xz_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-compressions/xz"
)

// BenchmarkDecodeCorpus decodes the 64 MiB corpus internal/benchxz compresses
// (-6). Skipped when the corpus is absent.
func BenchmarkDecodeCorpus(b *testing.B) {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, "Library", "Application Support", "go-compressions-xz-bench", "corpus-p6.xz"))
	if err != nil {
		b.Skip("no corpus:", err)
	}
	b.SetBytes(64 << 20)
	b.ReportAllocs()
	for b.Loop() {
		r, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatal(err)
		}
	}
}
