package xz_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/go-compressions/xz"
)

func decode(data []byte, cfg xz.ReaderConfig) ([]byte, error) {
	r, err := cfg.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func mustDecode(t *testing.T, data []byte) []byte {
	t.Helper()
	got, err := decode(data, xz.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wantErr(t *testing.T, data []byte, cfg xz.ReaderConfig, want error) {
	t.Helper()
	_, err := decode(data, cfg)
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

var plain = []byte("The quick brown fox jumps over the lazy dog. 0123456789")

func TestCraftedStreams(t *testing.T) {
	for _, check := range []byte{0, 1, 4, 10} {
		got := mustDecode(t, buildStream(check, simpleBlock(check, plain, 0)))
		if !bytes.Equal(got, plain) {
			t.Fatalf("check %d: %q", check, got)
		}
	}
	// Several blocks, with different dictionary sizes: the second reuses the
	// window of the first.
	big := bytes.Repeat([]byte("0123456789abcdef"), 2000)
	s := buildStream(1, simpleBlock(1, big, 20), simpleBlock(1, plain, 0), simpleBlock(1, big, 40))
	want := append(append(append([]byte(nil), big...), plain...), big...)
	if got := mustDecode(t, s); !bytes.Equal(got, want) {
		t.Fatal("multi-block output differs")
	}
	// No block at all.
	if got := mustDecode(t, buildStream(4)); len(got) != 0 {
		t.Fatalf("empty stream gave %q", got)
	}
}

func TestSmallReads(t *testing.T) {
	data := readFixture(t, "mixed-blocks.xz")
	f := loadFixtures(t)
	var want []byte
	for _, x := range f {
		if x.file == "mixed-blocks.xz" {
			want = x.raw
		}
	}
	r, err := xz.NewReader(iotest.OneByteReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := r.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = %d, %v", n, err)
	}
	got, err := io.ReadAll(iotest.OneByteReader(r))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("err=%v equal=%v", err, bytes.Equal(got, want))
	}
	r, _ = xz.NewReader(bytes.NewReader(data))
	if err := iotest.TestReader(r, want); err != nil {
		t.Fatal(err)
	}
}

func TestMultipleStreams(t *testing.T) {
	a := readFixture(t, "small.xz")
	b := readFixture(t, "text-crc32.xz")
	var wantB []byte
	for _, x := range loadFixtures(t) {
		if x.file == "text-crc32.xz" {
			wantB = x.raw
		}
	}
	wantA := mustDecode(t, a)
	zeros := func(n int) []byte { return make([]byte, n) }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	want := append(append([]byte(nil), wantA...), wantB...)

	for name, s := range map[string][]byte{
		"concatenated":       cat(a, b),
		"padding between":    cat(a, zeros(4), b),
		"long padding":       cat(a, zeros(40), b),
		"padding at the end": cat(a, b, zeros(8)),
	} {
		if got := mustDecode(t, s); !bytes.Equal(got, want) {
			t.Fatalf("%s: output differs", name)
		}
	}
	for name, s := range map[string][]byte{
		"padding of 2 bytes":         cat(a, zeros(2)),
		"padding of 6 bytes":         cat(a, zeros(6), b),
		"garbage after the stream":   cat(a, []byte("garbage!")),
		"truncated second header":    cat(a, b[:7]),
		"padding then partial magic": cat(a, zeros(4), []byte{0xFD, '7', 'z'}),
	} {
		_, err := decode(s, xz.ReaderConfig{})
		if !errors.Is(err, xz.ErrCorrupt) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// SingleStream: exactly one stream.
	cfg := xz.ReaderConfig{SingleStream: true}
	if got, err := decode(a, cfg); err != nil || !bytes.Equal(got, wantA) {
		t.Fatalf("single: %v", err)
	}
	wantErr(t, cat(a, b), cfg, xz.ErrCorrupt)
	wantErr(t, cat(a, zeros(4)), cfg, xz.ErrCorrupt)
}

type errReader struct{ r io.Reader }

var errBoom = errors.New("boom")

func (e errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		err = errBoom
	}
	return n, err
}

func TestSourceErrors(t *testing.T) {
	s := readFixture(t, "small.xz")
	// A failing source is reported as is wherever it is read.
	r, err := xz.NewReader(errReader{bytes.NewReader(s)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, errBoom) {
		t.Fatalf("got %v", err)
	}
	_, err = xz.ReaderConfig{SingleStream: true}.NewReader(errReader{bytes.NewReader(s)})
	if err != nil {
		t.Fatal(err)
	}
	r, _ = xz.ReaderConfig{SingleStream: true}.NewReader(errReader{bytes.NewReader(s)})
	if _, err := io.ReadAll(r); !errors.Is(err, errBoom) {
		t.Fatalf("single stream: got %v", err)
	}
	// Errors are sticky.
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, errBoom) {
		t.Fatalf("not sticky: %v", err)
	}
}

func TestConfig(t *testing.T) {
	var nilCfg *xz.ReaderConfig
	if err := nilCfg.Verify(); err == nil {
		t.Fatal("nil config accepted")
	}
	c := xz.ReaderConfig{MaxDictionary: -1}
	if err := c.Verify(); err == nil {
		t.Fatal("negative MaxDictionary accepted")
	}
	if _, err := c.NewReader(bytes.NewReader(nil)); err == nil {
		t.Fatal("NewReader accepted a bad config")
	}
	c = xz.ReaderConfig{}
	if err := c.Verify(); err != nil || c.MaxDictionary != xz.DefaultMaxDictionary {
		t.Fatalf("defaults: %v %d", err, c.MaxDictionary)
	}
	// DictCap is accepted and has no effect.
	got, err := decode(buildStream(1, simpleBlock(1, plain, 0)), xz.ReaderConfig{DictCap: 1 << 30})
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("DictCap: %v", err)
	}
}

func TestStreamHeaderErrors(t *testing.T) {
	good := buildStream(1, simpleBlock(1, plain, 0))
	mod := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	wantErr(t, nil, xz.ReaderConfig{}, xz.ErrTruncated)
	wantErr(t, good[:5], xz.ReaderConfig{}, io.ErrUnexpectedEOF)
	wantErr(t, mod(func(b []byte) { b[0] = 0 }), xz.ReaderConfig{}, xz.ErrCorrupt)
	wantErr(t, mod(func(b []byte) { b[8] ^= 1 }), xz.ReaderConfig{}, xz.ErrCorrupt)
	// Reserved bits and reserved check types, with a valid CRC: not corrupt,
	// but not supported.
	for _, flags := range [][2]byte{{1, 1}, {0, 0x11}, {0, 0x02}, {0, 0x05}, {0, 0x0F}} {
		h := mod(func(b []byte) {
			copy(b[6:8], flags[:])
			copy(b[8:12], crcOf(flags[:]))
		})
		wantErr(t, h, xz.ReaderConfig{}, xz.ErrUnsupported)
	}
}

func TestBlockHeaderErrors(t *testing.T) {
	c, u := uint64(len(stored(plain))), uint64(len(plain))
	comp := stored(plain)
	mk := func(h []byte) []byte { return buildStream(0, makeBlock(0, h, comp, plain)) }
	zero := uint64(0)

	cases := []struct {
		name string
		h    []byte
		want error
	}{
		{"crc", func() []byte { h := blockHeader(&c, &u, lzma2(0)); h[3] ^= 1; return h }(), xz.ErrCorrupt},
		{"reserved flags", refix(func() []byte {
			h := blockHeader(&c, &u, lzma2(0))
			h[1] |= 0x04
			return h
		}()), xz.ErrUnsupported},
		{"zero compressed size", blockHeader(&zero, &u, lzma2(0)), xz.ErrCorrupt},
		{"unknown filter", blockHeader(&c, &u, filterSpec{0x1234, nil}, lzma2(0)), xz.ErrUnsupported},
		{"last filter not LZMA2", blockHeader(&c, &u, lzma2(0), filterSpec{0x04, nil}), xz.ErrUnsupported},
		{"LZMA2 not last", blockHeader(&c, &u, lzma2(0), lzma2(0)), xz.ErrUnsupported},
		{"LZMA2 props length", blockHeader(&c, &u, filterSpec{0x21, []byte{0, 0}}), xz.ErrCorrupt},
		{"LZMA2 no props", blockHeader(&c, &u, filterSpec{0x21, nil}), xz.ErrCorrupt},
		{"LZMA2 dictionary property 41", blockHeader(&c, &u, lzma2(41)), xz.ErrCorrupt},
		{"delta props length", blockHeader(&c, &u, filterSpec{0x03, nil}, lzma2(0)), xz.ErrCorrupt},
		{"bcj props length", blockHeader(&c, &u, filterSpec{0x04, []byte{1}}, lzma2(0)), xz.ErrCorrupt},
		{"bcj start offset alignment", blockHeader(&c, &u, filterSpec{0x05, le32(2)}, lzma2(0)), xz.ErrCorrupt},
		{"properties longer than the header", finishBlockHeader(0x00, []byte{0x21, 0x7F, 0}), xz.ErrCorrupt},
		{"second filter missing", finishBlockHeader(0x01, []byte{0x04, 0}), xz.ErrCorrupt},
		{"missing filter id", finishBlockHeader(0xC0, []byte{1, 1}), xz.ErrCorrupt},
		{"missing properties size", finishBlockHeader(0x40, []byte{1, 0x21}), xz.ErrCorrupt},
		{"truncated compressed size", finishBlockHeader(0x40, []byte{0x80, 0x80}), xz.ErrCorrupt},
		{"truncated uncompressed size", finishBlockHeader(0x80, []byte{0x80}), xz.ErrCorrupt},
		{"non-minimal integer", finishBlockHeader(0x00, []byte{0xA1, 0x00, 1, 0}), xz.ErrCorrupt},
		{"integer of 10 bytes", finishBlockHeader(0x00, bytes.Repeat([]byte{0xFF}, 10)), xz.ErrCorrupt},
		{"non-zero header padding", func() []byte {
			h := blockHeader(&c, &u, lzma2(0))
			h[len(h)-5] = 1
			return refix(h)
		}(), xz.ErrUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { wantErr(t, mk(tc.h), xz.ReaderConfig{}, tc.want) })
	}
}

func TestBlockBodyErrors(t *testing.T) {
	comp := stored(plain)
	c, u := uint64(len(comp)), uint64(len(plain))
	good := func(check byte) block {
		return makeBlock(check, blockHeader(&c, &u, lzma2(0)), comp, plain)
	}
	stream := func(check byte, b block) []byte {
		return buildStream(check, b)
	}
	// Sizes declared in the header that do not match the data.
	short, long := u-1, u+1
	wrongU := makeBlock(0, blockHeader(&c, &short, lzma2(0)), comp, plain)
	wantErr(t, stream(0, wrongU), xz.ReaderConfig{}, xz.ErrCorrupt)
	wrongU = makeBlock(0, blockHeader(&c, &long, lzma2(0)), comp, plain)
	wantErr(t, stream(0, wrongU), xz.ReaderConfig{}, xz.ErrCorrupt)
	for _, cs := range []uint64{c - 1, c + 1, c - 5} {
		cs := cs
		b := makeBlock(0, blockHeader(&cs, &u, lzma2(0)), comp, plain)
		wantErr(t, stream(0, b), xz.ReaderConfig{}, xz.ErrCorrupt)
	}
	// Block padding.
	for _, check := range []byte{1, 4, 10} {
		b := good(check)
		pad := len(b.bytes) - checkLen(check) - len(blockHeader(&c, &u, lzma2(0))) - len(comp)
		if pad == 0 {
			t.Fatalf("test block has no padding (check %d)", check)
		}
		off := len(b.bytes) - checkLen(check) - 1
		bad := b
		bad.bytes = append([]byte(nil), b.bytes...)
		bad.bytes[off] = 7
		wantErr(t, stream(check, bad), xz.ReaderConfig{}, xz.ErrCorrupt)
	}
	// Integrity checks.
	for _, check := range []byte{1, 4, 10} {
		b := good(check)
		hlen := len(blockHeader(&c, &u, lzma2(0)))
		bad := b
		bad.bytes = append([]byte(nil), b.bytes...)
		bad.bytes[hlen+10] ^= 0x20 // a byte of the stored data
		_, err := decode(stream(check, bad), xz.ReaderConfig{})
		if !errors.Is(err, xz.ErrChecksum) || !errors.Is(err, xz.ErrCorrupt) {
			t.Fatalf("check %d: %v", check, err)
		}
		bad.bytes = append([]byte(nil), b.bytes...)
		bad.bytes[len(bad.bytes)-1] ^= 1 // the check itself
		wantErr(t, stream(check, bad), xz.ReaderConfig{}, xz.ErrChecksum)
	}
}

func TestIndexAndFooterErrors(t *testing.T) {
	// 200 bytes: the index records need two-byte integers and padding.
	plain := bytes.Repeat([]byte("x"), 200)
	base := partsOf(1, plain)
	if !bytes.Equal(mustDecode(t, base.join()), plain) {
		t.Fatal("base stream does not decode")
	}
	fixIdx := func(idx []byte) []byte { // recompute the index CRC
		copy(idx[len(idx)-4:], crcOf(idx[:len(idx)-4]))
		return idx
	}
	b := simpleBlock(1, plain, 0)
	mutate := func(f func(p *parts)) []byte {
		p := base
		p.blk = append([]byte(nil), base.blk...)
		p.index = append([]byte(nil), base.index...)
		p.footer = append([]byte(nil), base.footer...)
		f(&p)
		return p.join()
	}
	cases := []struct {
		name string
		s    []byte
		want error
	}{
		{"more blocks listed", buildStreamWithIndex(1, []block{b}, indexOf([]block{b, b})), xz.ErrCorrupt},
		{"fewer blocks listed", buildStreamWithIndex(1, []block{b, b}, indexOf([]block{b})), xz.ErrCorrupt},
		{"wrong unpadded size", mutate(func(p *parts) { p.index[2]++; p.index = fixIdx(p.index) }), xz.ErrCorrupt},
		{"wrong uncompressed size", mutate(func(p *parts) { p.index[4]++; p.index = fixIdx(p.index) }), xz.ErrCorrupt},
		{"index padding", mutate(func(p *parts) { p.index[len(p.index)-5] = 1; p.index = fixIdx(p.index) }), xz.ErrCorrupt},
		{"index crc", mutate(func(p *parts) { p.index[len(p.index)-1] ^= 1 }), xz.ErrCorrupt},
		{"backward size", mutate(func(p *parts) { p.footer[4]++; copy(p.footer, crcOf(p.footer[4:10])) }), xz.ErrCorrupt},
		{"footer crc", mutate(func(p *parts) { p.footer[0] ^= 1 }), xz.ErrCorrupt},
		{"footer flags", mutate(func(p *parts) { p.footer[9] = 4; copy(p.footer, crcOf(p.footer[4:10])) }), xz.ErrCorrupt},
		{"footer magic", mutate(func(p *parts) { p.footer[10] = 'X' }), xz.ErrCorrupt},
		{"index record too long", mutate(func(p *parts) {
			p.index = append([]byte{0, 1}, bytes.Repeat([]byte{0xFF}, 10)...)
		}), xz.ErrCorrupt},
		{"index uncompressed size too long", mutate(func(p *parts) {
			p.index = append(append([]byte{0, 1}, vli(b.unpadded)...), bytes.Repeat([]byte{0xFF}, 10)...)
		}), xz.ErrCorrupt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.s, xz.ReaderConfig{}, c.want) })
	}
}

func buildStreamWithIndex(check byte, blocks []block, idx []byte) []byte {
	s := streamHeader(check)
	for _, b := range blocks {
		s = append(s, b.bytes...)
	}
	s = append(s, idx...)
	return append(s, streamFooter(check, len(idx))...)
}

// Every proper prefix of a valid stream is an error, never a clean EOF, and
// the error is ErrCorrupt.
func TestTruncation(t *testing.T) {
	streams := map[string][]byte{
		"crafted-crc32":  buildStream(1, simpleBlock(1, plain, 0)),
		"crafted-crc64":  buildStream(4, simpleBlock(4, plain, 0), simpleBlock(4, plain, 3)),
		"crafted-sha256": buildStream(10, simpleBlock(10, plain, 0)),
		"empty":          readFixture(t, "empty.xz"),
		"small":          readFixture(t, "small.xz"),
	}
	for name, s := range streams {
		for n := 0; n < len(s); n++ {
			_, err := decode(s[:n], xz.ReaderConfig{})
			if !errors.Is(err, xz.ErrCorrupt) {
				t.Fatalf("%s truncated to %d of %d bytes: %v", name, n, len(s), err)
			}
		}
	}
}

// Flipping any byte never panics, and a stream that still decodes without
// error decodes to the original data unless the check is None.
func TestBitFlips(t *testing.T) {
	s := buildStream(10, simpleBlock(10, plain, 0))
	for i := range s {
		for _, mask := range []byte{1, 0x80, 0xFF} {
			b := append([]byte(nil), s...)
			b[i] ^= mask
			got, err := decode(b, xz.ReaderConfig{})
			if err == nil && !bytes.Equal(got, plain) {
				t.Fatalf("flip %#x at %d decoded to different data without an error", mask, i)
			}
		}
	}
}

func TestBadMagicMessage(t *testing.T) {
	_, err := xz.NewReader(strings.NewReader("this is not an xz file at all"))
	if err == nil || !errors.Is(err, xz.ErrCorrupt) {
		t.Fatal(err)
	}
}
