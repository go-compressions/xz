// Command covgaps prints the blocks of a Go coverage profile that were never
// executed (summing the counts a block has in every test binary), and the
// overall statement coverage.
//
//	go test -coverpkg=./... -coverprofile=cov.out ./... && go run ./internal/covgaps cov.out
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type block struct {
	stmts, count int
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: covgaps profile")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	blocks := map[string]*block{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "mode:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		stmts, _ := strconv.Atoi(fields[1])
		count, _ := strconv.Atoi(fields[2])
		b := blocks[fields[0]]
		if b == nil {
			b = &block{stmts: stmts}
			blocks[fields[0]] = b
		}
		b.count += count
	}
	var total, covered int
	var gaps []string
	for k, b := range blocks {
		total += b.stmts
		if b.count > 0 {
			covered += b.stmts
		} else {
			gaps = append(gaps, k)
		}
	}
	sort.Strings(gaps)
	for _, g := range gaps {
		fmt.Println("uncovered:", g)
	}
	if total == 0 {
		fmt.Println("no statements")
		return
	}
	fmt.Printf("coverage: %.1f%% (%d/%d statements)\n", 100*float64(covered)/float64(total), covered, total)
}
