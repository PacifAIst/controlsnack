// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// step is one stage of the quality gate. A non-empty skip reason with a nil error reports the step as skipped.
type step struct {
	name string
	run  func() (skip string, err error)
}

// cmdCheck runs the quality gate of PLAN.md section 13.1 and prints a summary. Every step runs even after a
// failure, so one invocation shows everything that needs fixing.
func cmdCheck(args []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	skipFlag := flags.String("skip", "", "comma-separated step names to skip (gofmt, mod-tidy, vet, lint, test, vuln, licenses, cross, repo)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	skip := map[string]bool{}
	for _, s := range strings.Split(*skipFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			skip[s] = true
		}
	}

	steps := []step{
		{"gofmt", stepGofmt},
		{"mod-tidy", stepModTidy},
		{"vet", stepVet},
		{"lint", stepLint},
		{"test", stepTest},
		{"vuln", stepVuln},
		{"licenses", stepLicenses},
		{"cross", stepCross},
		{"repo", stepRepo},
	}

	type result struct {
		name, note string
		err        error
		took       time.Duration
	}
	var results []result
	failed := 0
	for _, s := range steps {
		if skip[s.name] {
			results = append(results, result{name: s.name, note: "skipped (--skip)"})
			continue
		}
		fmt.Printf("\n== %s ==\n", s.name)
		start := time.Now()
		reason, err := s.run()
		r := result{name: s.name, err: err, took: time.Since(start).Round(time.Millisecond)}
		switch {
		case err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", s.name, err)
		case reason != "":
			r.note = "skipped: " + reason
			fmt.Println("SKIP", s.name+":", reason)
		}
		results = append(results, r)
	}

	fmt.Println("\n== summary ==")
	for _, r := range results {
		status := "ok"
		switch {
		case r.err != nil:
			status = "FAIL"
		case r.note != "":
			status = "skip"
		}
		fmt.Printf("  %-4s  %-9s %8s  %s\n", status, r.name, r.took, r.note)
	}
	if failed > 0 {
		return fmt.Errorf("%d check step(s) failed", failed)
	}
	fmt.Println("\ncheck passed")
	return nil
}

func inCI() bool { return os.Getenv("CI") != "" }

// walkGo calls fn for every Go source file outside generated or vendored trees.
func walkGo(fn func(path string, content []byte) error) error {
	return filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "dist", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		content, err := os.ReadFile(path) //nolint:gosec // walking our own working tree
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(path), content)
	})
}

// stepGofmt is `gofmt -l` done in-process, so it needs nothing but the Go toolchain.
func stepGofmt() (string, error) {
	var bad []string
	err := walkGo(func(path string, content []byte) error {
		formatted, ferr := format.Source(content)
		if ferr != nil {
			bad = append(bad, path+": "+ferr.Error())
			return nil
		}
		if !bytes.Equal(formatted, content) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(bad) > 0 {
		return "", fmt.Errorf("not gofmt-clean (run gofmt -w):\n  %s", strings.Join(bad, "\n  "))
	}
	return "", nil
}

func stepModTidy() (string, error) {
	return "", run(nil, "go", "mod", "tidy", "-diff")
}

// stepVet vets the host packages natively and the guest agent as linux, the platform it actually runs on.
func stepVet() (string, error) {
	if err := run([]string{"CGO_ENABLED=0"}, "go", "vet", "./..."); err != nil {
		return "", err
	}
	linux := target{"linux", "amd64"}
	return "", run(targetEnv(linux), "go", "vet", "./internal/agent/...", agentPkg)
}

func stepLint() (string, error) {
	path, err := exec.LookPath("golangci-lint")
	if err != nil {
		if inCI() {
			return "", errors.New("golangci-lint is not installed")
		}
		return "golangci-lint is not installed (go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)", nil
	}
	return "", run(nil, path, "run", "./...")
}

// raceSupport reports whether -race can be used: it needs cgo and a C compiler on the machine.
func raceSupport() (ok bool, why string) {
	if os.Getenv("CONTROLSNACK_NO_RACE") != "" {
		return false, "disabled by CONTROLSNACK_NO_RACE"
	}
	cc, err := capture(nil, "go", "env", "CC")
	if err != nil || strings.TrimSpace(cc) == "" {
		return false, "cannot determine the C compiler"
	}
	compiler := strings.Fields(cc)[0]
	if _, err := exec.LookPath(compiler); err != nil {
		return false, "no C compiler (" + compiler + ") on PATH"
	}
	return true, ""
}

func stepTest() (string, error) {
	args := []string{"test", "-count=1"}
	var env []string
	if ok, why := raceSupport(); ok {
		args = append(args, "-race")
		env = append(env, "CGO_ENABLED=1")
	} else {
		fmt.Println("note: running without -race:", why, "(CI runs it)")
	}
	args = append(args, "./...")
	return "", run(env, "go", args...)
}

func stepVuln() (string, error) {
	return "", run(nil, "go", "tool", "govulncheck", "./...")
}

func stepLicenses() (string, error) {
	return "", checkLicenses()
}

func stepCross() (string, error) {
	tmp, err := os.MkdirTemp("", "controlsnack-check-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	return "", crossBuild(tmp, true)
}
