// Command mkfixtures regenerates the .xz and .lzma files under testdata/
// with the xz command line tool (the C reference, xz-utils). The tests only
// read the committed files; they never need xz installed.
//
//	go run ./internal/mkfixtures -out testdata
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/go-compressions/xz/internal/corpus"
)

type fixture struct {
	name string // file name without extension; recorded in Fixtures
	kind string // corpus kind
	n    int
	seed uint64
	ext  string
	args []string
}

func fixtures() []fixture {
	var fs []fixture
	add := func(name, kind string, n int, ext string, args ...string) {
		fs = append(fs, fixture{name, kind, n, 1, ext, args})
	}
	add("text-default", "text", 200000, "xz")
	add("text-crc32", "text", 50000, "xz", "--check=crc32")
	add("text-none", "text", 50000, "xz", "--check=none")
	add("text-sha256", "text", 50000, "xz", "--check=sha256")
	add("text-p0", "text", 100000, "xz", "-0")
	add("text-p9e", "text", 100000, "xz", "-9e")
	add("text-dict4k", "text", 100000, "xz", "--lzma2=preset=6,dict=4KiB")
	add("text-lc0lp2", "text", 60000, "xz", "--lzma2=lc=0,lp=2,pb=1")
	add("text-lc4pb0", "text", 60000, "xz", "--lzma2=lc=4,lp=0,pb=0")
	add("text-lc1lp3pb4", "text", 60000, "xz", "--lzma2=lc=1,lp=3,pb=4")
	add("mixed-blocks", "mixed", 200000, "xz", "--block-size=16KiB")
	add("mixed-blocks-sha256", "mixed", 100000, "xz", "--block-size=10000", "--check=sha256")
	add("random-stored", "random", 70000, "xz")
	add("mixed-default", "mixed", 100000, "xz")
	add("empty", "text", 0, "xz")
	add("small", "text", 16, "xz")
	for _, f := range []struct{ kind, flag string }{
		{"x86", "--x86"}, {"arm", "--arm"}, {"armthumb", "--armthumb"},
		{"ppc", "--powerpc"}, {"sparc", "--sparc"}, {"ia64", "--ia64"},
		{"arm64", "--arm64"}, {"riscv", "--riscv"},
	} {
		add("bcj-"+f.kind, f.kind, 24000, "xz", f.flag, "--lzma2=preset=6")
	}
	add("bcj-x86-start", "x86", 20000, "xz", "--x86=start=4096", "--lzma2=preset=6")
	add("bcj-arm64-start", "arm64", 20000, "xz", "--arm64=start=4096", "--lzma2=preset=6")
	add("bcj-x86-blocks", "x86", 30000, "xz", "--x86", "--lzma2=preset=6", "--block-size=8KiB")
	add("delta-audio", "audio", 30000, "xz", "--delta=dist=2", "--lzma2=preset=6")
	add("delta-1", "audio", 12000, "xz", "--delta=dist=1", "--lzma2=preset=6")
	add("delta-256", "audio", 12000, "xz", "--delta=dist=256", "--lzma2=preset=6")
	add("chain-x86-delta", "x86", 20000, "xz", "--x86", "--delta=dist=4", "--lzma2=preset=6")
	add("chain-4", "x86", 20000, "xz", "--delta=dist=1", "--x86", "--delta=dist=3", "--lzma2=preset=6")
	add("lzma-text", "text", 100000, "lzma", "--format=lzma")
	add("lzma-mixed-lc0lp2", "mixed", 60000, "lzma", "--format=lzma", "--lzma1=lc=0,lp=2,pb=0")
	add("lzma-small", "text", 16, "lzma", "--format=lzma")
	add("lzma-empty", "text", 0, "lzma", "--format=lzma")
	return fs
}

func main() {
	out := flag.String("out", "testdata", "output directory")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var index bytes.Buffer
	for _, f := range fixtures() {
		in := corpus.Make(f.kind, f.n, f.seed)
		cmd := exec.Command("xz", append(append([]string{"-c", "-T1"}, f.args...), "-")...)
		cmd.Stdin = bytes.NewReader(in)
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, f.name, err)
			os.Exit(1)
		}
		path := filepath.Join(*out, f.name+"."+f.ext)
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(&index, "%s %s %d %d\n", f.name+"."+f.ext, f.kind, f.n, f.seed)
	}
	if err := os.WriteFile(filepath.Join(*out, "INDEX"), index.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
