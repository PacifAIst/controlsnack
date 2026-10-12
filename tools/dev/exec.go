// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// devCtx is canceled by Ctrl+C so child processes (go test, QEMU later) do not outlive the runner.
var devCtx = context.Background()

// run executes a command with extra environment variables and streams its output.
func run(env []string, name string, args ...string) error {
	fmt.Printf("$ %s\n", describe(env, name, args))
	cmd := exec.CommandContext(devCtx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	return cmd.Run()
}

// capture executes a command and returns its trimmed stdout. Stderr is folded into the error.
func capture(env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(devCtx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("%s: %w: %s", describe(env, name, args), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func describe(env []string, name string, args []string) string {
	parts := make([]string, 0, len(env)+1+len(args))
	parts = append(parts, env...)
	parts = append(parts, name)
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"") {
			a = fmt.Sprintf("%q", a)
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// targetEnv is the environment for building target t without cgo (PLAN.md D-06: pure Go everywhere).
func targetEnv(t target) []string {
	return []string{"CGO_ENABLED=0", "GOOS=" + t.GOOS, "GOARCH=" + t.GOARCH}
}

// buildStamp returns the version, short commit and commit date used for -ldflags -X. It falls back to
// placeholders in a repository without commits so builds still work on a fresh clone.
func buildStamp() (version, commit, date string) {
	version, commit, date = "dev", "unknown", "unknown"
	if v, err := capture(nil, "git", "describe", "--tags", "--always", "--dirty"); err == nil && v != "" {
		version = v
	}
	if c, err := capture(nil, "git", "rev-parse", "--short", "HEAD"); err == nil && c != "" {
		commit = c
	}
	// The commit date, not the wall clock, keeps repeated builds of one commit byte-identical.
	if d, err := capture(nil, "git", "show", "-s", "--format=%cI", "HEAD"); err == nil && d != "" {
		date = d
	}
	return version, commit, date
}

// commitTime is the timestamp used for archive entries; the zero time when it cannot be determined.
func commitTime() time.Time {
	_, _, date := buildStamp()
	t, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// ldflags builds the linker flags that stamp internal/buildinfo.
func ldflags(strip bool) string {
	version, commit, date := buildStamp()
	flags := []string{
		"-X " + buildinfoPkg + ".Version=" + version,
		"-X " + buildinfoPkg + ".Commit=" + commit,
		"-X " + buildinfoPkg + ".Date=" + date,
	}
	if strip {
		flags = append([]string{"-s", "-w"}, flags...)
	}
	return strings.Join(flags, " ")
}

// goBuild builds pkg for target t into out. Binaries are always pure Go, trimmed and VCS-info free so the same
// commit produces the same bytes.
func goBuild(t target, pkg, out string, strip bool) error {
	return run(targetEnv(t), "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags(strip), "-o", out, pkg)
}

// exeName appends .exe on Windows targets.
func exeName(base string, t target) string {
	if t.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}
