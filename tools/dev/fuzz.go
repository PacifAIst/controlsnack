// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"flag"
	"fmt"
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
		if err := run(nil, "go", "test", "-run=^$", "-fuzz=^"+t.name+"$", "-fuzztime="+dur.String(), t.pkg); err != nil {
			return fmt.Errorf("%s %s: %w", t.pkg, t.name, err)
		}
	}
	return nil
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
