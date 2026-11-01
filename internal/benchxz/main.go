// Command benchxz measures single-core decode throughput of this library, of
// github.com/ulikunitz/xz and of the C reference (xz -dc) on a 64 MiB
// realistic corpus: the Go source tree and the Go toolchain binaries of the
// running Go installation, concatenated, then compressed once by xz.
//
// It refuses to measure when the 1-minute load average is above 8 (the
// numbers would measure the machine, not the code) and exits with status 3.
//
//	go run ./internal/benchxz -work ~/Documents/VCS/GIT/localhost/go-compressions-xz-bench
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	uxz "github.com/ulikunitz/xz"

	"github.com/go-compressions/xz"
)

const corpusSize = 64 << 20

func main() {
	work := flag.String("work", "", "durable directory for the corpus and the compressed file")
	rounds := flag.Int("rounds", 5, "interleaved rounds")
	preset := flag.String("preset", "-6", "xz preset used to compress the corpus")
	maxLoad := flag.Float64("maxload", 8, "refuse to measure above this 1-minute load average")
	flag.Parse()
	if *work == "" {
		fmt.Fprintln(os.Stderr, "-work is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*work, 0o755); err != nil {
		fatal(err)
	}
	load, err := loadAverage()
	if err != nil {
		fatal(err)
	}
	fmt.Printf("1-minute load average: %.2f\n", load)
	if load > *maxLoad {
		fmt.Printf("measurement deferred: load %.2f is above %.0f\n", load, *maxLoad)
		os.Exit(3)
	}

	raw := filepath.Join(*work, "corpus.bin")
	comp := filepath.Join(*work, "corpus"+strings.ReplaceAll(*preset, "-", "-p")+".xz")
	if _, err := os.Stat(raw); err != nil {
		if err := buildCorpus(raw); err != nil {
			fatal(err)
		}
	}
	if _, err := os.Stat(comp); err != nil {
		out, err := os.Create(comp)
		if err != nil {
			fatal(err)
		}
		in, _ := os.Open(raw)
		cmd := exec.Command("xz", "-c", "-T1", *preset)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, os.Stderr
		if err := cmd.Run(); err != nil {
			fatal(err)
		}
		out.Close()
		in.Close()
	}
	want, err := os.ReadFile(raw)
	if err != nil {
		fatal(err)
	}
	data, err := os.ReadFile(comp)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("corpus: %d bytes, compressed %d bytes (%s)\n", len(want), len(data), *preset)

	// Verify first: all three must agree with the corpus.
	if got, err := mine(data); err != nil || !bytes.Equal(got, want) {
		fatal(fmt.Errorf("this library does not reproduce the corpus: %v", err))
	}
	runtime.GOMAXPROCS(1)

	type result struct {
		name  string
		times []time.Duration
	}
	results := []*result{{name: "go-compressions/xz"}, {name: "ulikunitz/xz v0.5.17"}, {name: "xz -dc (C)"}}
	for i := 0; i < *rounds; i++ {
		for j, f := range []func() (time.Duration, error){
			func() (time.Duration, error) {
				return timeGo(data, func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) })
			},
			func() (time.Duration, error) {
				return timeGo(data, func(r io.Reader) (io.Reader, error) { return uxz.NewReader(r) })
			},
			func() (time.Duration, error) { return timeC(comp) },
		} {
			d, err := f()
			if err != nil {
				fatal(err)
			}
			results[j].times = append(results[j].times, d)
		}
	}
	fmt.Printf("\n| decoder | best MB/s | median MB/s | rounds |\n|---|---|---|---|\n")
	var best [3]float64
	for i, r := range results {
		sort.Slice(r.times, func(a, b int) bool { return r.times[a] < r.times[b] })
		b := mbps(len(want), r.times[0])
		m := mbps(len(want), r.times[len(r.times)/2])
		best[i] = b
		fmt.Printf("| %s | %.1f | %.1f | %d |\n", r.name, b, m, len(r.times))
	}
	fmt.Printf("\nratios (best): this/ulikunitz = %.2f, this/xz = %.2f\n", best[0]/best[1], best[0]/best[2])
	end, _ := loadAverage()
	fmt.Printf("1-minute load average at the end: %.2f\n", end)
}

func mbps(n int, d time.Duration) float64 { return float64(n) / 1e6 / d.Seconds() }

func mine(data []byte) ([]byte, error) {
	r, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func timeGo(data []byte, mk func(io.Reader) (io.Reader, error)) (time.Duration, error) {
	r, err := mk(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	start := time.Now()
	_, err = io.Copy(io.Discard, r)
	return time.Since(start), err
}

func timeC(path string) (time.Duration, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	cmd := exec.Command("xz", "-dc", "-T1")
	cmd.Stdin = in // Stdout is nil: /dev/null
	start := time.Now()
	err = cmd.Run()
	return time.Since(start), err
}

func loadAverage() (float64, error) {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		return strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	}
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output() // "{ 1.23 4.56 7.89 }"
	if err != nil {
		return 0, err
	}
	f := strings.Fields(strings.Trim(string(out), "{} \n"))
	return strconv.ParseFloat(f[0], 64)
}

// buildCorpus concatenates the Go sources and the toolchain binaries of the
// running installation (deterministic order) up to 64 MiB: about half text,
// half machine code.
func buildCorpus(path string) error {
	goroot := runtime.GOROOT()
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	var total int
	add := func(root string, limit int, accept func(string) bool) error {
		start := total
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !accept(p) || total-start >= limit || total >= corpusSize {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if total+len(b) > corpusSize {
				b = b[:corpusSize-total]
			}
			n, err := out.Write(b)
			total += n
			return err
		})
	}
	if err := add(filepath.Join(goroot, "src"), corpusSize/2, func(p string) bool { return strings.HasSuffix(p, ".go") }); err != nil {
		return err
	}
	if err := add(filepath.Join(goroot, "pkg", "tool"), corpusSize, func(p string) bool { return true }); err != nil {
		return err
	}
	if err := add(filepath.Join(goroot, "bin"), corpusSize, func(p string) bool { return true }); err != nil {
		return err
	}
	if total < corpusSize {
		return fmt.Errorf("only %d bytes of corpus found under %s", total, goroot)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
