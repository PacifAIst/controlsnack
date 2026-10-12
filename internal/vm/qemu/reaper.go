// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The reaper (PLAN.md section 7.4). macOS has no Pdeathsig, so QEMU is started through a hidden
// `controlsnack __reaper --parent <pid> -- <qemu> <args>` subprocess that runs QEMU in its own process group and
// kills it when the controller disappears. The reaper tells the controller QEMU's pid on an extra pipe (file
// descriptor 3), because the controller must be able to kill QEMU itself, not the wrapper.
//
//	reaper -> CSREAPER/1 pid=<n>\n          or          CSREAPER/1 error=<message>\n

const (
	reaperMagic = "CSREAPER/1"
	// reaperReportTimeout bounds the wait for the pid report.
	reaperReportTimeout = 10 * time.Second
	// ReaperCommand is the hidden command that runs the reaper.
	ReaperCommand = "__reaper"
)

// reaperCmd builds the command that runs argv under a reaper. The returned file is the read end of the report
// pipe; the write end is the child's file descriptor 3 and is closed here once the child has it.
func (p *Process) reaperCmd(argv []string, opt StartOptions) (*exec.Cmd, *os.File, error) {
	prefix := opt.ReaperCmd
	if len(prefix) == 0 {
		self, err := os.Executable()
		if err != nil {
			return nil, nil, fmt.Errorf("starting QEMU under the reaper: %w", err)
		}
		prefix = []string{self, ReaperCommand}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("starting QEMU under the reaper: %w", err)
	}
	args := append(append([]string(nil), prefix[1:]...), "--parent", strconv.Itoa(os.Getpid()), "--")
	args = append(args, argv...)
	cmd := exec.CommandContext(context.Background(), prefix[0], args...) //nolint:gosec // this executable, or the test binary
	cmd.ExtraFiles = []*os.File{w}
	p.reportW = w
	return cmd, r, nil
}

// readReaperReport reads the pid line and records QEMU's pid.
func (p *Process) readReaperReport(r *os.File) error {
	_ = r.SetReadDeadline(time.Now().Add(reaperReportTimeout))
	line, err := bufio.NewReaderSize(r, 512).ReadString('\n')
	if err != nil {
		return fmt.Errorf("the reaper did not report QEMU's pid: %w", err)
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), reaperMagic+" ")
	if !ok {
		return fmt.Errorf("unexpected reaper report %q", strings.TrimSpace(line))
	}
	if msg, isErr := strings.CutPrefix(rest, "error="); isErr {
		return errors.New(msg)
	}
	val, ok := strings.CutPrefix(rest, "pid=")
	pid, convErr := strconv.Atoi(val)
	if !ok || convErr != nil || pid <= 1 {
		return fmt.Errorf("unexpected reaper report %q", strings.TrimSpace(line))
	}
	p.pid = pid
	return nil
}
