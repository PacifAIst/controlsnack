// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// maxToolOutput bounds what is kept of a probe's output. `-device help` prints about 40 KiB; anything near this is
// not QEMU talking.
const maxToolOutput = 512 << 10

// RunResult is what a finished program printed and how it ended.
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Output returns stdout and stderr together, the way help texts are parsed (QEMU versions differ in which stream
// they use).
func (r RunResult) Output() string { return r.Stdout + r.Stderr }

// Host is everything discovery learns from the machine other than the file system: the OS and architecture, the
// environment, and running QEMU's own tools. Tests substitute a fake, so the version and feature logic is
// exercised on every OS without a QEMU installed.
type Host interface {
	GOOS() string
	GOARCH() string
	Getenv(key string) string
	// Run starts the program, feeds it stdin, and returns when it exits or ctx ends. An error means the program
	// could not be run or ctx ended; a non-zero exit status is reported in the result.
	Run(ctx context.Context, path, stdin string, args ...string) (RunResult, error)
}

// OSHost is the Host of the running machine.
type OSHost struct{}

// GOOS returns the operating system.
func (OSHost) GOOS() string { return runtime.GOOS }

// GOARCH returns the architecture.
func (OSHost) GOARCH() string { return runtime.GOARCH }

// Getenv reads the process environment.
func (OSHost) Getenv(key string) string { return os.Getenv(key) }

// Run runs path with a minimal environment and bounded output.
func (OSHost) Run(ctx context.Context, path, stdin string, args ...string) (RunResult, error) {
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path comes from discovery, args are constants
	cmd.Env = probeEnv()
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr capBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second // do not wait forever for a killed child's pipes
	err := cmd.Run()
	if ctx.Err() != nil {
		return RunResult{}, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return RunResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exit.ExitCode()}, nil
	}
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// probeEnv is the environment of probe runs: what a program needs to start, and a fixed locale so the help texts
// that are parsed do not change with the user's language.
func probeEnv() []string {
	env := []string{"LC_ALL=C", "LANG=C"}
	for _, k := range []string{"PATH", "HOME", "SystemRoot", "SystemDrive", "windir", "TEMP", "TMP", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// capBuffer keeps the first maxToolOutput bytes and silently drops the rest, so a chatty child never blocks on a
// full pipe and never fills memory.
type capBuffer struct{ buf bytes.Buffer }

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := maxToolOutput - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *capBuffer) String() string { return c.buf.String() }
