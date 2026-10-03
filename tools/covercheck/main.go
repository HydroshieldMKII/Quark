// Command covercheck enforces per-package statement coverage gates
// (constitution, Principle IV) on a Go coverage profile.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

const module = "example.org/quark/"

// gates maps package paths (relative to the module) to minimum coverage in percent.
var gates = map[string]float64{
	"internal/pdcli":     90,
	"internal/drive":     90,
	"internal/app":       85,
	"internal/jobs":      85,
	"internal/settings":  85,
	"internal/xlog":      80,
	"internal/tray":      75,
	"internal/autostart": 80,
	"internal/fusefs":    80,
}

type counts struct{ total, covered int }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: covercheck coverage.out [more.out...]")
		os.Exit(2)
	}
	var data []byte
	for _, f := range os.Args[1:] {
		b, err := os.ReadFile(f) //nolint:gosec // developer-supplied profile paths
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		data = append(append(data, b...), '\n')
	}

	// Blocks can appear several times (one per test binary); keep the max count.
	type block struct{ stmts, count int }
	blocks := map[string]block{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "mode:") || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		stmts, _ := strconv.Atoi(fields[1])
		count, _ := strconv.Atoi(fields[2])
		b := blocks[fields[0]]
		b.stmts = stmts
		b.count = max(b.count, count)
		blocks[fields[0]] = b
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	pkgs := map[string]*counts{}
	for key, b := range blocks {
		file, _, _ := strings.Cut(key, ":")
		pkg := strings.TrimPrefix(path.Dir(file), module)
		c := pkgs[pkg]
		if c == nil {
			c = &counts{}
			pkgs[pkg] = c
		}
		c.total += b.stmts
		if b.count > 0 {
			c.covered += b.stmts
		}
	}

	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	failed := false
	for _, n := range names {
		c := pkgs[n]
		pct := 100 * float64(c.covered) / float64(max(c.total, 1))
		gate, gated := gates[n]
		status := "     "
		if gated {
			status = "ok   "
			if pct < gate {
				status = "FAIL "
				failed = true
			}
		}
		if gated {
			fmt.Printf("%s %-28s %5.1f%% (gate %.0f%%)\n", status, n, pct, gate)
		} else {
			fmt.Printf("%s %-28s %5.1f%%\n", status, n, pct)
		}
	}
	if failed {
		fmt.Fprintln(os.Stderr, "coverage gates failed")
		os.Exit(1)
	}
}
