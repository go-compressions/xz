package xz_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/go-compressions/xz"
	"github.com/go-compressions/xz/internal/testfiles"
	"github.com/go-compressions/xz/lzma"
)

// The test files of XZ Utils (tests/files, 0BSD; see the README next to
// them): good files must decode, bad files must be refused, unsupported
// files must be refused as unsupported, none of them with a lot of memory.

func xzUtilsFiles(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(testfiles.FS, "xz-utils")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".xz") || strings.HasSuffix(n, ".lzma") {
			names = append(names, n)
		}
	}
	if len(names) < 70 {
		t.Fatalf("only %d xz-utils test files embedded", len(names))
	}
	return names
}

func decodeAny(name string, data []byte) ([]byte, error) {
	if strings.HasSuffix(name, ".lzma") {
		r, err := lzma.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	}
	return decode(data, xz.ReaderConfig{})
}

func TestXZUtilsFiles(t *testing.T) {
	for _, name := range xzUtilsFiles(t) {
		t.Run(name, func(t *testing.T) {
			data := readFixture(t, "xz-utils/"+name)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			got, err := decodeAny(name, data)
			runtime.ReadMemStats(&after)
			if d := after.TotalAlloc - before.TotalAlloc; d > 32<<20 {
				t.Errorf("allocated %d bytes", d)
			}
			switch {
			case strings.HasPrefix(name, "good-"):
				if err != nil {
					t.Fatalf("good file refused: %v", err)
				}
			case strings.HasPrefix(name, "bad-"):
				if err == nil {
					t.Fatalf("bad file accepted (%d bytes of output)", len(got))
				}
				if !errors.Is(err, xz.ErrCorrupt) && !errors.Is(err, xz.ErrUnsupported) {
					t.Fatalf("untyped error: %v", err)
				}
			default: // unsupported-*
				if !errors.Is(err, xz.ErrUnsupported) {
					t.Fatalf("got %v, want ErrUnsupported", err)
				}
			}
		})
	}
}

// Where the xz command line tool is installed, the good files must decode to
// what it produces.
func TestXZUtilsFilesAgainstXZ(t *testing.T) {
	xzPath, err := exec.LookPath("xz")
	if err != nil {
		t.Skip("xz is not installed")
	}
	for _, name := range xzUtilsFiles(t) {
		if !strings.HasPrefix(name, "good-") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			data := readFixture(t, "xz-utils/"+name)
			format := "xz"
			if strings.HasSuffix(name, ".lzma") {
				format = "lzma"
			}
			cmd := exec.Command(xzPath, "-dc", "--format="+format)
			cmd.Stdin = bytes.NewReader(data)
			want, err := cmd.Output()
			if err != nil {
				t.Skipf("xz refuses the file: %v", err)
			}
			got, err := decodeAny(name, data)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("output differs from xz -dc: err=%v got %d bytes, want %d", err, len(got), len(want))
			}
		})
	}
}
