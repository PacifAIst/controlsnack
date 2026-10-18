// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package viewer

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobProc is a program and everything it starts, held in a job object. A browser starts a tree of processes, and Edge's first
// process hands the work to another and exits at once, so ending "the process" would leave the window and its profile behind.
// The job is made to kill whatever is in it when it is closed, so the window does not outlive this program either.
type jobProc struct {
	job  windows.Handle
	done chan struct{}
	once sync.Once
}

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobAccounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
}

func startProcess(path string, args []string) (Process, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("making a job object: %w", err)
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil { //nolint:gosec // the Windows API takes a pointer to its own structure and its size
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("setting up the job object: %w", err)
	}
	app, err := windows.UTF16PtrFromString(path)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{path}, args...)))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	// Started suspended and put in the job before it runs a single instruction, so that nothing it starts can be outside it.
	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(app, cmdline, nil, nil, false, windows.CREATE_SUSPENDED|windows.CREATE_NEW_PROCESS_GROUP, nil, nil, &si, &pi); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(pi.Thread); _ = windows.CloseHandle(pi.Process) }()
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("putting the process in its job: %w", err)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("starting the process: %w", err)
	}
	p := &jobProc{job: job, done: make(chan struct{})}
	go p.watch()
	return p, nil
}

func (p *jobProc) active() (uint32, error) {
	var info jobAccounting
	err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil) //nolint:gosec // the Windows API takes a pointer to its own structure and its size
	return info.ActiveProcesses, err
}

func (p *jobProc) finish() {
	p.once.Do(func() {
		close(p.done)
		_ = windows.CloseHandle(p.job)
	})
}

func (p *jobProc) watch() {
	for {
		n, err := p.active()
		if err != nil || n == 0 {
			p.finish()
			return
		}
		select {
		case <-p.done:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Kill ends everything in the job.
func (p *jobProc) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := windows.TerminateJobObject(p.job, 1); err != nil && !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return err
	}
	return nil
}

func (p *jobProc) Done() <-chan struct{} { return p.done }
