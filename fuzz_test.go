package xz_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	uxz "github.com/ulikunitz/xz"
	ulzma "github.com/ulikunitz/xz/lzma"

	"github.com/go-compressions/xz"
	"github.com/go-compressions/xz/internal/testfiles"
	"github.com/go-compressions/xz/lzma"
)

const (
	fuzzOutput    = 1 << 20  // bytes read from either decoder
	fuzzMaxDict   = 1 << 22  // we decode with this ceiling
	fuzzOracleMax = 16 << 20 // the oracle only runs if no header declared more
)

func typed(err error) bool {
	return err == nil || errors.Is(err, xz.ErrCorrupt) || errors.Is(err, xz.ErrTooLarge) ||
		errors.Is(err, xz.ErrUnsupported)
}

func seedFixtures(f *testing.F, dirs ...string) {
	for _, dir := range dirs {
		entries, err := fs.ReadDir(testfiles.FS, dir)
		if err != nil {
			f.Fatal(err)
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !(strings.HasSuffix(n, ".xz") || strings.HasSuffix(n, ".lzma")) {
				continue
			}
			p := n
			if dir != "." {
				p = dir + "/" + n
			}
			b, err := fs.ReadFile(testfiles.FS, p)
			if err != nil {
				f.Fatal(err)
			}
			if len(b) <= 4096 {
				f.Add(b)
			}
		}
	}
}

// checkXZ is the body of FuzzXZ, shared with the named crasher tests.
func checkXZ(t testing.TB, data []byte) {
	r, err := xz.ReaderConfig{MaxDictionary: fuzzMaxDict}.NewReader(bytes.NewReader(data))
	var got []byte
	var gerr error
	var declared uint64
	if err == nil {
		got, gerr = io.ReadAll(io.LimitReader(r, fuzzOutput))
		declared = r.MaxDeclared()
		err = gerr
	}
	if !typed(err) {
		t.Fatalf("untyped error: %v", err)
	}
	if declared > fuzzOracleMax {
		return
	}
	// Differential: wherever ulikunitz/xz decodes the whole input within the
	// output bound, so must this decoder, to the same bytes.
	or, oerr := uxz.NewReader(bytes.NewReader(data))
	if oerr != nil {
		return
	}
	want, werr := io.ReadAll(io.LimitReader(or, fuzzOutput))
	if werr != nil || len(want) >= fuzzOutput {
		return
	}
	if err != nil {
		// ulikunitz/xz is more lenient than the xz-utils specification
		// (it accepts non-minimal integers, stray chunk bytes, ...): this
		// decoder may refuse input it accepts, with the typed error
		// checked above. Only when both succeed must the bytes agree.
		return
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs from ulikunitz/xz: %d vs %d bytes", len(got), len(want))
	}
}

func FuzzXZ(f *testing.F) {
	seedFixtures(f, ".", "xz-utils")
	f.Add(buildStream(10, simpleBlock(10, plain, 40)))
	f.Add(buildStream(4, simpleBlock(4, plain, 0), simpleBlock(4, plain, 5)))
	f.Fuzz(func(t *testing.T, data []byte) { checkXZ(t, data) })
}

// checkLZMA is the body of FuzzLZMA.
func checkLZMA(t testing.TB, data []byte) {
	r, err := lzma.ReaderConfig{MaxDictionary: fuzzMaxDict}.NewReader(bytes.NewReader(data))
	var got []byte
	if err == nil {
		got, err = io.ReadAll(io.LimitReader(r, fuzzOutput))
	}
	if !typed(err) {
		t.Fatalf("untyped error: %v", err)
	}
	if len(data) < 13 {
		return
	}
	// ulikunitz/xz refuses headers with a dictionary above DictCap instead
	// of allocating it.
	or, oerr := ulzma.ReaderConfig{DictCap: fuzzOracleMax}.NewReader(bytes.NewReader(data))
	if oerr != nil {
		return
	}
	want, werr := io.ReadAll(io.LimitReader(or, fuzzOutput))
	if werr != nil || len(want) >= fuzzOutput {
		return
	}
	if err != nil {
		return // stricter than the oracle: see checkXZ
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs from ulikunitz/xz: %d vs %d bytes", len(got), len(want))
	}
}

func FuzzLZMA(f *testing.F) {
	seedFixtures(f, ".", "xz-utils")
	f.Fuzz(func(t *testing.T, data []byte) { checkLZMA(t, data) })
}

// checkLZMA2 is the body of FuzzLZMA2: raw LZMA2 with a dictionary size
// taken from the first input byte.
func checkLZMA2(t testing.TB, data []byte) {
	if len(data) == 0 {
		return
	}
	dict := 4096 << (data[0] % 12) // 4 KiB .. 8 MiB
	data = data[1:]
	r, err := lzma.Reader2Config{DictCap: dict, MaxDictionary: fuzzMaxDict}.NewReader2(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(io.LimitReader(r, fuzzOutput))
	if !typed(err) {
		t.Fatalf("untyped error: %v", err)
	}
	or, oerr := ulzma.Reader2Config{DictCap: dict}.NewReader2(bytes.NewReader(data))
	if oerr != nil {
		return
	}
	want, werr := io.ReadAll(io.LimitReader(or, fuzzOutput))
	if werr != nil || len(want) >= fuzzOutput {
		return
	}
	if err != nil {
		return // stricter than the oracle: see checkXZ
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs from ulikunitz/xz: %d vs %d bytes", len(got), len(want))
	}
}

func FuzzLZMA2(f *testing.F) {
	f.Add(append([]byte{0}, stored(plain)...))
	f.Add([]byte{3, 1, 0, 2, 'a', 'b', 'c', 0})
	f.Fuzz(func(t *testing.T, data []byte) { checkLZMA2(t, data) })
}
