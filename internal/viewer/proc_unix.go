// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package viewer

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// groupProc is a program and everything it starts, which is one process group: a browser starts a tree of processes, and a
// launcher that hands the work to another process and exits must not leave the window behind when it is ended.
type groupProc struct {
	pgid int
	done chan struct{}
	once sync.Once
}

func startProcess(path string, args []string) (Process, error) {
	cmd := exec.CommandContext(context.Background(), path, args...) //nolint:gosec // the browser found on this machine, with arguments made here
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &groupProc{pgid: cmd.Process.Pid, done: make(chan struct{})}
	go func() { _ = cmd.Wait() }() // reaps the first process, which may be gone long before the browser
	go p.watch()
	return p, nil
}

// alive reports whether any process of the group is left.
func (p *groupProc) alive() bool {
	err := syscall.Kill(-p.pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (p *groupProc) finish() { p.once.Do(func() { close(p.done) }) }

func (p *groupProc) watch() {
	for p.alive() {
		select {
		case <-p.done:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	p.finish()
}

// Kill ends the whole group. Once the group is empty the number could be used by another, so nothing is sent then.
func (p *groupProc) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := syscall.Kill(-p.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (p *groupProc) Done() <-chan struct{} { return p.done }
