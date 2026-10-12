// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package qemu

import (
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// prepareCmd gives the child its own console process group (a Ctrl+C aimed at the controller does not reach it) and
// no console window.
func prepareCmd(cmd *exec.Cmd, _ bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

// adopt puts the process in a Job Object that kills everything in it when its last handle closes. The controller
// holds the only handle, so the OS closes it when the controller dies, however it dies, and QEMU goes with it.
// The returned function closes the handle once the process has exited.
func adopt(pid int) (release func(), err error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("creating the job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { //nolint:gosec // the documented calling convention
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configuring the job object: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid)) //nolint:gosec // a pid fits 32 bits
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("opening the process for the job object: %w", err)
	}
	defer func() { _ = windows.CloseHandle(proc) }()
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("assigning the process to the job object: %w", err)
	}
	return func() { _ = windows.CloseHandle(job) }, nil
}

// killTree terminates the process; the job object takes anything it started.
func killTree(_ int, proc *os.Process, reaped *atomic.Bool) error {
	if reaped.Load() {
		return os.ErrProcessDone
	}
	return proc.Kill()
}
