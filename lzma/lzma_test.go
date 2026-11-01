package lzma_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"runtime"
	"testing"

	ulzma "github.com/ulikunitz/xz/lzma"

	"github.com/go-compressions/xz/internal/corpus"
	"github.com/go-compressions/xz/internal/testfiles"
	"github.com/go-compressions/xz/lzma"
)

func encodeAlone(t testing.TB, data []byte, cfg ulzma.WriterConfig) []byte {
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

func decodeAlone(data []byte, cfg lzma.ReaderConfig) ([]byte, error) {
	r, err := cfg.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestAloneAgainstUlikunitz(t *testing.T) {
	for _, kind := range []string{"text", "mixed", "random"} {
		data := corpus.Make(kind, 150000, 3)
		for _, c := range []struct {
			name string
			cfg  ulzma.WriterConfig
		}{
			{"default", ulzma.WriterConfig{}},
			{"size in header", ulzma.WriterConfig{SizeInHeader: true, Size: int64(len(data))}},
			{"size and marker", ulzma.WriterConfig{SizeInHeader: true, Size: int64(len(data)), EOSMarker: true}},
			{"marker", ulzma.WriterConfig{EOSMarker: true}},
			{"lc8 lp4", ulzma.WriterConfig{Properties: &ulzma.Properties{LC: 8, LP: 4, PB: 4}, EOSMarker: true}},
			{"lc0 lp0 pb0", ulzma.WriterConfig{Properties: &ulzma.Properties{LC: 0, LP: 0, PB: 0}, DictCap: 4096}},
			{"small dict", ulzma.WriterConfig{DictCap: 4096, EOSMarker: true}},
			{"large dict", ulzma.WriterConfig{DictCap: 16 << 20}},
		} {
			t.Run(kind+"/"+c.name, func(t *testing.T) {
				enc := encodeAlone(t, data, c.cfg)
				got, err := decodeAlone(enc, lzma.ReaderConfig{})
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("err=%v equal=%v", err, bytes.Equal(got, data))
				}
				// ulikunitz agrees (it is the oracle for the cases it supports).
				want, err := ioReadAll(ulzma.NewReader(bytes.NewReader(enc)))
				if err != nil || !bytes.Equal(want, data) {
					t.Fatalf("oracle: %v", err)
				}
			})
		}
	}
}

func ioReadAll(r io.Reader, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestHeader(t *testing.T) {
	data := corpus.Make("text", 1000, 1)
	n := int64(len(data))
	r, err := lzma.NewReader(bytes.NewReader(encodeAlone(t, data, ulzma.WriterConfig{SizeInHeader: true, Size: n})))
	if err != nil {
		t.Fatal(err)
	}
	if h := r.Header(); h.LC != 3 || h.LP != 0 || h.PB != 2 || h.Size != n || h.DictCap != 8<<20 {
		t.Fatalf("%+v", h)
	}
	r, _ = lzma.NewReader(bytes.NewReader(encodeAlone(t, data, ulzma.WriterConfig{EOSMarker: true})))
	if h := r.Header(); h.Size != -1 {
		t.Fatalf("%+v", h)
	}
	// A size above 2^63 is reported as unknown by Header but still decodes
	// by the size field (the reader never allocates from it).
	enc := encodeAlone(t, data, ulzma.WriterConfig{SizeInHeader: true, Size: n})
	binary.LittleEndian.PutUint64(enc[5:], 1<<63+5)
	r, _ = lzma.NewReader(bytes.NewReader(enc))
	if h := r.Header(); h.Size != -1 {
		t.Fatalf("%+v", h)
	}
}

func TestReaderErrors(t *testing.T) {
	if _, err := lzma.NewReader(bytes.NewReader([]byte{1, 2})); !errors.Is(err, lzma.ErrTruncated) {
		t.Fatal(err)
	}
	bad := make([]byte, 13)
	bad[0] = 225
	if _, err := lzma.NewReader(bytes.NewReader(bad)); !errors.Is(err, lzma.ErrCorrupt) {
		t.Fatal(err)
	}
	if _, err := (lzma.ReaderConfig{MaxDictionary: -1}).NewReader(bytes.NewReader(bad)); err == nil {
		t.Fatal("negative MaxDictionary accepted")
	}
	c := lzma.ReaderConfig{DictCap: 12345}
	if err := c.Verify(); err != nil || c.MaxDictionary != lzma.DefaultMaxDictionary {
		t.Fatalf("%v %d", err, c.MaxDictionary)
	}
	c = lzma.ReaderConfig{MaxDictionary: 1 << 20}
	if err := c.Verify(); err != nil || c.MaxDictionary != 1<<20 {
		t.Fatalf("%v %d", err, c.MaxDictionary)
	}
}

func TestZeroPrefixAndHugeDictionary(t *testing.T) {
	// The .lzma header has no magic: a zero byte in front of a stream makes
	// the "dictionary size" huge. ulikunitz allocates it; here it costs
	// nothing and the garbage that follows is a typed error.
	data := corpus.Make("text", 5000, 1)
	enc := encodeAlone(t, data, ulzma.WriterConfig{EOSMarker: true})
	enc[1], enc[2], enc[3], enc[4] = 0xFF, 0xFF, 0xFF, 0xFF // 4 GiB - 1 dictionary
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := decodeAlone(enc, lzma.ReaderConfig{})
	runtime.ReadMemStats(&after)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("a huge declared dictionary must decode small data: %v", err)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 1<<20 {
		t.Fatalf("allocated %d bytes", d)
	}
	// The same declared dictionary with data that does need it fails with
	// ErrTooLarge once the ceiling is passed, not before.
	big := corpus.Make("mixed", 300000, 1)
	enc = encodeAlone(t, big, ulzma.WriterConfig{EOSMarker: true, DictCap: 1 << 20})
	_, err = decodeAlone(enc, lzma.ReaderConfig{MaxDictionary: 100000})
	if !errors.Is(err, lzma.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	if got, err := decodeAlone(enc, lzma.ReaderConfig{MaxDictionary: len(big)}); err != nil || !bytes.Equal(got, big) {
		t.Fatalf("ceiling equal to the data size: %v", err)
	}
}

func encodeLZMA2(t testing.TB, data []byte, dictCap int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := ulzma.Writer2Config{DictCap: dictCap}.NewWriter2(&buf)
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

func TestReader2(t *testing.T) {
	for _, kind := range []string{"text", "mixed", "random"} {
		data := corpus.Make(kind, 300000, 5)
		for _, dict := range []int{1 << 16, 1 << 20, 8 << 20} {
			enc := encodeLZMA2(t, data, dict)
			for _, cfg := range []lzma.Reader2Config{{DictCap: dict}, {}} {
				r, err := cfg.NewReader2(bytes.NewReader(enc))
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(r)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("%s dict=%d cfg=%+v: err=%v", kind, dict, cfg, err)
				}
			}
			// Without the 0x00 terminator a clean end of input at a chunk
			// boundary ends the stream.
			r, _ := lzma.NewReader2(bytes.NewReader(enc[:len(enc)-1]))
			if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("unterminated: %v", err)
			}
		}
	}
}

func TestReader2DictionaryRefusesFarMatches(t *testing.T) {
	data := corpus.Make("text", 100000, 5)
	enc := encodeLZMA2(t, data, 1<<20)
	// 100 is raised to the minimum dictionary size, 4096.
	for _, dict := range []int{4096, 100} {
		_, err := io.ReadAll(mustReader2(t, lzma.Reader2Config{DictCap: dict}, enc))
		if !errors.Is(err, lzma.ErrCorrupt) {
			t.Fatalf("dict %d: got %v", dict, err)
		}
	}
}

func mustReader2(t *testing.T, cfg lzma.Reader2Config, enc []byte) io.Reader {
	t.Helper()
	r, err := cfg.NewReader2(bytes.NewReader(enc))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReader2Config(t *testing.T) {
	cfgs := []lzma.Reader2Config{{DictCap: -1}, {MaxDictionary: -5}}
	// A dictionary above 4 GiB - 1 can only be written where int is 64 bits.
	if shift := uint(40); 1<<shift > 0 {
		cfgs = append(cfgs, lzma.Reader2Config{DictCap: 1 << shift})
	}
	for _, cfg := range cfgs {
		if _, err := cfg.NewReader2(bytes.NewReader(nil)); err == nil {
			t.Fatalf("%+v accepted", cfg)
		}
	}
	c := lzma.Reader2Config{}
	if err := c.Verify(); err != nil || c.MaxDictionary != lzma.DefaultMaxDictionary || c.DictCap != 0 {
		t.Fatalf("%v %+v", err, c)
	}
	// Ceiling.
	data := corpus.Make("random", 100000, 1)
	enc := encodeLZMA2(t, data, 1<<20)
	_, err := io.ReadAll(mustReader2(t, lzma.Reader2Config{DictCap: 1 << 20, MaxDictionary: 50000}, enc))
	if !errors.Is(err, lzma.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestDictCapFromProp(t *testing.T) {
	for prop, want := range map[byte]uint32{0: 4096, 1: 6144, 18: 2 << 20, 40: 0xFFFFFFFF} {
		got, err := lzma.DictCapFromProp(prop)
		if err != nil || got != want {
			t.Fatalf("prop %d: %d %v", prop, got, err)
		}
	}
	if _, err := lzma.DictCapFromProp(41); !errors.Is(err, lzma.ErrCorrupt) {
		t.Fatal(err)
	}
}

// Raw LZMA2 taken out of the fixtures made by the xz command line tool.
func TestReader2FromXZFixture(t *testing.T) {
	b, err := fs.ReadFile(testfiles.FS, "text-none.xz")
	if err != nil {
		t.Fatal(err)
	}
	// stream header (12) + block header; the data runs to the index.
	hdrLen := (int(b[12]) + 1) * 4
	body := b[12+hdrLen:]
	want := corpus.Make("text", 50000, 1)
	r, err := lzma.Reader2Config{DictCap: 8 << 20}.NewReader2(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("err=%v equal=%v", err, bytes.Equal(got, want))
	}
}
