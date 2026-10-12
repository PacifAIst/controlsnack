// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"errors"
	"fmt"
	"os"
)

// ErrNoSuchProcess is returned for a process that does not exist.
var ErrNoSuchProcess = errors.New("no such process")

// ProcessInspector looks at and ends other processes by PID. A PID alone is not an identity: the operating system
// reuses them, so anything that kills a process recorded earlier (the stale-session reaper, `controlsnack kill`)
// compares the process's start time with the one recorded and refuses on a mismatch.
type ProcessInspector interface {
	// Alive reports whether a process with this PID exists.
	Alive(pid int) bool
	// StartTime returns an opaque string that identifies when the process started. It is only ever compared for
	// equality with a value obtained earlier from this same method.
	StartTime(pid int) (string, error)
	// Kill ends the process immediately (SIGKILL; TerminateProcess on Windows).
	Kill(pid int) error
}

// OSProcesses is the ProcessInspector of the running operating system.
type OSProcesses struct{}

// Alive reports whether the PID exists.
func (OSProcesses) Alive(pid int) bool { return pid > 0 && processAlive(pid) }

// StartTime returns the start-time identity of the process.
func (OSProcesses) StartTime(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrNoSuchProcess
	}
	return processStartTime(pid)
}

// Kill terminates the process without giving it a chance to clean up.
func (OSProcesses) Kill(pid int) error {
	if pid <= 0 {
		return ErrNoSuchProcess
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}
	if err := p.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return ErrNoSuchProcess
		}
		return err
	}
	return nil
}

// SameProcess reports whether the process with this PID is still the one that was recorded: it is alive and its
// start time equals recordedStart. An empty recordedStart (the platform cannot tell) never matches, so a recorded
// process is not killed on a PID match alone.
func SameProcess(pi ProcessInspector, pid int, recordedStart string) bool {
	if recordedStart == "" || !pi.Alive(pid) {
		return false
	}
	now, err := pi.StartTime(pid)
	return err == nil && now == recordedStart
}
