package xz_test

import (
	"bytes"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/go-compressions/xz"
	"github.com/go-compressions/xz/internal/corpus"
	"github.com/go-compressions/xz/internal/testfiles"
	"github.com/go-compressions/xz/lzma"
)

type fixture struct {
	file string
	kind string
	n    int
	seed uint64
	raw  []byte
}

// loadFixtures reads testdata/INDEX written by internal/mkfixtures.
func loadFixtures(t testing.TB) []fixture {
	t.Helper()
	idx, err := fs.ReadFile(testfiles.FS, "INDEX")
	if err != nil {
		t.Fatal(err)
	}
	var out []fixture
	for _, line := range strings.Split(strings.TrimSpace(string(idx)), "\n") {
		f := strings.Fields(line)
		n, _ := strconv.Atoi(f[2])
		seed, _ := strconv.ParseUint(f[3], 10, 64)
		out = append(out, fixture{file: f[0], kind: f[1], n: n, seed: seed,
			raw: corpus.Make(f[1], n, seed)})
	}
	return out
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(testfiles.FS, name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestXZCLIFixtures(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.file, func(t *testing.T) {
			data := readFixture(t, f.file)
			var r io.Reader
			var err error
			if strings.HasSuffix(f.file, ".lzma") {
				r, err = lzma.NewReader(bytes.NewReader(data))
			} else {
				r, err = xz.NewReader(bytes.NewReader(data))
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, f.raw) {
				t.Fatalf("output differs: got %d bytes, want %d", len(got), len(f.raw))
			}
		})
	}
}
