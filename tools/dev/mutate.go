// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A mutation check is how this project knows a test means something: break one thing on purpose and see a test fail. Done by
// hand with a shell, it went wrong in ways that cost hours (a pattern that matched nothing and was counted as a pass, a
// replacement that did not compile and was counted as a kill, a whole test suite run for each change, a file left broken).
// This command does one mutation, in the foreground, and says exactly what happened.

// mutation is the result of trying one change.
type mutation int

const (
	mutKilled   mutation = iota // a test failed: the change was seen
	mutSurvived                 // every test passed: nothing sees the change
	mutInvalid                  // the change did not build: it says nothing about the tests
)

// replaceOnce replaces old with repl in content, which must hold old exactly once: a change that could land in two places
// is not the change the author meant.
func replaceOnce(content, old, repl string) (string, error) {
	if old == "" {
		return "", errors.New("the text to replace is empty")
	}
	switch n := strings.Count(content, old); {
	case n == 0:
		return "", errors.New("the text to replace is not in the file (no change was made)")
	case n > 1:
		return "", fmt.Errorf("the text to replace is in the file %d times; make it unique", n)
	}
	out := strings.Replace(content, old, repl, 1)
	if out == content {
		return "", errors.New("the replacement is the same as the text (no change was made)")
	}
	return out, nil
}

var (
	failedTest  = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)
	buildFailed = regexp.MustCompile(`(?m)\[build failed\]|^# .*\n.*: (?:undefined|syntax error|cannot use|declared and not used|imported and not used)`)
)

// tryMutation applies the change to the file, runs the tests that can see it (only those: go test -run), and puts the file
// back, whatever happens.
func tryMutation(file, old, repl, runRe string, pkgs []string, timeout time.Duration) (res mutation, detail string, err error) {
	orig, err := os.ReadFile(file) //nolint:gosec // our own working tree
	if err != nil {
		return 0, "", err
	}
	changed, err := replaceOnce(string(orig), old, repl)
	if err != nil {
		return 0, "", err
	}
	info, err := os.Stat(file)
	if err != nil {
		return 0, "", err
	}
	// A copy on disk too, in case this process is killed before it can put the file back.
	spare := filepath.Join(os.TempDir(), "controlsnack-mutate-"+filepath.Base(file)+".orig")
	if err := os.WriteFile(spare, orig, 0o600); err != nil {
		return 0, "", err
	}
	defer func() {
		if werr := os.WriteFile(file, orig, info.Mode().Perm()); werr != nil {
			err = errors.Join(err, fmt.Errorf("could not put %s back (the original is in %s): %w", file, spare, werr))
			return
		}
		_ = os.Remove(spare)
	}()
	if err := os.WriteFile(file, []byte(changed), info.Mode().Perm()); err != nil {
		return 0, "", err
	}
	args := append([]string{"test", "-count=1", "-failfast", "-timeout", timeout.String(), "-run", runRe}, pkgs...)
	cmd := exec.CommandContext(devCtx, "go", args...)
	cmd.Env = append(os.Environ(), "CS_FAST_FAIL=1") // the tests of the page wait only a few seconds for what is not going to happen
	out, runErr := cmd.CombinedOutput()
	text := string(out)
	switch {
	case runErr == nil:
		return mutSurvived, "", nil
	case buildFailed.MatchString(text):
		return mutInvalid, firstLines(text, 4), nil
	}
	if m := failedTest.FindStringSubmatch(text); m != nil {
		return mutKilled, m[1], nil
	}
	return mutKilled, firstLines(text, 2), nil // a panic or a timeout: the tests did not pass
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[:min(n, len(lines))], " | ")
}

// cmdMutate is `go run ./tools/dev mutate -run <tests> -file <file> -old <text> -new <text> [-pkgs "./a ./b"] [-name label]`.
func cmdMutate(args []string) error {
	fl := flag.NewFlagSet("mutate", flag.ContinueOnError)
	name := fl.String("name", "", "a label for the result line")
	runRe := fl.String("run", "", "regular expression of the tests that can see the change (go test -run); required")
	file := fl.String("file", "", "the file to change; required")
	old := fl.String("old", "", "the text to replace; it must be in the file exactly once; required")
	repl := fl.String("new", "", "what to put in its place")
	pkgs := fl.String("pkgs", "", "packages to test (default: the package of the file)")
	esc := fl.Bool("esc", false, `read \n and \t in -old and -new as a line break and a tab`)
	timeout := fl.Duration("timeout", 90*time.Second, "limit for the tests")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *runRe == "" || *file == "" || *old == "" {
		return errors.New("mutate needs -run, -file and -old")
	}
	if *esc {
		r := strings.NewReplacer(`\n`, "\n", `\t`, "\t")
		*old, *repl = r.Replace(*old), r.Replace(*repl)
	}
	list := strings.Fields(*pkgs)
	if len(list) == 0 {
		list = []string{"./" + filepath.ToSlash(filepath.Dir(*file))}
	}
	label := *name
	if label == "" {
		label = *file
	}
	res, detail, err := tryMutation(*file, *old, *repl, *runRe, list, *timeout)
	if err != nil {
		fmt.Printf("ERROR     %s: %v\n", label, err)
		return err
	}
	switch res {
	case mutKilled:
		fmt.Printf("killed    %s (%s)\n", label, detail)
		return nil
	case mutInvalid:
		fmt.Printf("INVALID   %s: the change does not build, so it proves nothing: %s\n", label, detail)
		return errors.New("invalid mutation")
	}
	fmt.Printf("SURVIVED  %s\n", label)
	return errors.New("the mutation survived")
}
