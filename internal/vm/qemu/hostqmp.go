// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"strconv"
	"time"

	"github.com/PacifAIst/controlsnack/internal/platform"
)

// QEMU for Windows does not serve QMP on a standard input that is a pipe (QEMU 11.1.0 answers the greeting and the first command
// and then reads the rest as garbage, and never quits), so a script for `-qmp stdio` reaches it through a loopback socket there:
// the same QMP, on the kind of endpoint a session uses on Windows (D-28).

// usesQMPStdio says whether args ask for QMP on standard input.
func usesQMPStdio(args []string) bool { return qmpStdioAt(args) >= 0 }

// qmpStdioAt is the index of the `stdio` that follows `-qmp`, or -1.
func qmpStdioAt(args []string) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-qmp" && args[i+1] == "stdio" {
			return i + 1
		}
	}
	return -1
}

// qmpOverTCP is args with `-qmp stdio` turned into QMP on the loopback port, which QEMU serves without waiting for a client.
func qmpOverTCP(args []string, port int) []string {
	out := append([]string(nil), args...)
	if i := qmpStdioAt(out); i >= 0 {
		out[i] = "tcp:127.0.0.1:" + strconv.Itoa(port) + ",server=on,wait=off"
	}
	return out
}

// runQMPScript runs path with `-qmp stdio` served on a loopback socket, writes script to it once QEMU is listening, and waits for
// QEMU to exit (the script is meant to end with quit). What it returns is what Run returns for a program that was fed stdin.
func runQMPScript(ctx context.Context, path, script string, args []string) (RunResult, error) {
	l, err := platform.ListenLoopback(ctx, 0)
	if err != nil {
		return RunResult{}, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // QEMU takes the port; the moment between is the cost of asking the system for a free one

	cmd := exec.CommandContext(ctx, path, qmpOverTCP(args, port)...) //nolint:gosec // path comes from discovery, args are constants
	cmd.Env = probeEnv()
	var stdout, stderr capBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return RunResult{}, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var waitErr error
	ended := false
	dialer := net.Dialer{Timeout: time.Second}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var conn net.Conn
	for conn == nil && !ended {
		c, derr := dialer.DialContext(ctx, "tcp4", addr)
		switch {
		case derr == nil:
			conn = c
		case ctx.Err() != nil:
			waitErr, ended = <-exited, true
		default:
			select {
			case waitErr = <-exited: // QEMU stopped before it listened: its exit status says why
				ended = true
			case <-time.After(40 * time.Millisecond):
			}
		}
	}
	if conn != nil {
		go func() { _, _ = io.Copy(io.Discard, conn) }() // the greeting and the answers are not read, but must not block QEMU
		_, _ = io.WriteString(conn, script)
		waitErr = <-exited
		_ = conn.Close()
	}
	if ctx.Err() != nil {
		return RunResult{}, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		return RunResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exit.ExitCode()}, nil
	}
	if waitErr != nil {
		return RunResult{}, waitErr
	}
	return RunResult{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}
