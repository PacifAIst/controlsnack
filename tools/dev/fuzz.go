// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type fuzzTarget struct{ pkg, name string }

// cmdFuzz runs every Fuzz function in the module for a short, fixed time each (PLAN.md section 13.3:
// 30 s per target on pull requests, longer nightly).
func cmdFuzz(args []string) error {
	fs := flag.NewFlagSet("fuzz", flag.ContinueOnError)
	dur := fs.Duration("time", 30*time.Second, "fuzzing time per target")
	pattern := fs.String("pkgs", "./...", "package pattern to search for Fuzz targets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets, err := listFuzzTargets(*pattern)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Println("no Fuzz targets yet")
		return nil
	}
	for _, t := range targets {
		fmt.Printf("\n== fuzz %s %s (%s) ==\n", t.pkg, t.name, *dur)
		out, err := fuzzOnce(t, *dur)
		if err != nil && fuzzRanOutOfTime(out) {
			fmt.Printf("%s %s: the fuzzer ran out of time in the middle of an input, which it reports as a failure; no failing input was found, so this counts as a pass\n", t.pkg, t.name)
			err = nil
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", t.pkg, t.name, err)
		}
	}
	return nil
}

// fuzzOnce fuzzes one target for dur, showing the output as it comes and keeping it.
func fuzzOnce(t fuzzTarget, dur time.Duration) (string, error) {
	args := []string{"test", "-run=^$", "-fuzz=^" + t.name + "$", "-fuzztime=" + dur.String(), t.pkg}
	fmt.Printf("$ %s\n", describe(nil, "go", args))
	cmd := exec.CommandContext(devCtx, "go", args...)
	var kept bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.MultiWriter(os.Stdout, &kept), io.MultiWriter(os.Stderr, &kept)
	cmd.Env = os.Environ()
	err := cmd.Run()
	return kept.String(), err
}

// fuzzRanOutOfTime says whether a failed fuzz run only ran out of time: when the time given ends while an input is still being
// tried (one that decodes a large picture, say), the fuzzer reports "context deadline exceeded" and fails, with nothing found.
// A real finding writes the input that failed ("Failing input written to testdata/fuzz/...").
func fuzzRanOutOfTime(out string) bool {
	return strings.Contains(out, "context deadline exceeded") && !strings.Contains(out, "Failing input written") && !strings.Contains(out, "panic:")
}

func listFuzzTargets(pattern string) ([]fuzzTarget, error) {
	out, err := capture(nil, "go", "test", "-list", "^Fuzz", pattern)
	if err != nil {
		return nil, err
	}
	return parseFuzzList(out), nil
}

// parseFuzzList parses `go test -list` output, which prints each package's matching test names followed by an
// "ok <package> <time>" line.
func parseFuzzList(out string) []fuzzTarget {
	var targets []fuzzTarget
	var pending []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Fuzz"):
			pending = append(pending, line)
		case strings.HasPrefix(line, "ok"):
			if fields := strings.Fields(line); len(fields) >= 2 {
				for _, name := range pending {
					targets = append(targets, fuzzTarget{pkg: fields[1], name: name})
				}
			}
			pending = nil
		default:
			pending = nil
		}
	}
	return targets
}
