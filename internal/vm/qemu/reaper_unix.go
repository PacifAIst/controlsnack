// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package qemu

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// RunReaper is the body of the hidden `__reaper` command. It starts argv in its own process group, reports the pid
// on report, and then waits: when QEMU exits it returns QEMU's exit status, and when the parent controller goes
// away (the reaper is then re-parented) or a termination signal arrives, it kills QEMU's group first. The result
// is the exit status the reaper process should end with.
func RunReaper(parent int, argv []string, report io.Writer, stdout, stderr io.Writer) int {
	tell := func(format string, args ...any) { _, _ = fmt.Fprintf(report, reaperMagic+" "+format+"\n", args...) }
	if len(argv) == 0 {
		tell("error=no command to run")
		return 2
	}
	if os.Getppid() != parent {
		tell("error=the controller is already gone")
		return 70
	}
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) //nolint:gosec // argv is the QEMU command line built by this program
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		tell("error=%s", oneLine(err.Error()))
		return 127
	}
	tell("pid=%d", cmd.Process.Pid)

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	killGroup := func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	tick := time.NewTicker(reaperPoll)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return cmd.ProcessState.ExitCode()
		case <-tick.C:
			if os.Getppid() != parent {
				killGroup()
				return 137
			}
		case <-sigs:
			killGroup()
			return 143
		}
	}
}

// reaperPoll is how often the reaper looks at its parent.
const reaperPoll = 200 * time.Millisecond

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
