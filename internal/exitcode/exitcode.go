// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package exitcode

import (
	"context"
	"errors"
	"fmt"
)

// The exit codes. 70 follows sysexits' EX_SOFTWARE spirit for "this command is not built yet".
const (
	OK             = 0  // success
	Failure        = 1  // any other error
	Usage          = 2  // bad flags, arguments or configuration
	Environment    = 3  // QEMU or the accelerator is missing or unusable
	ImageMissing   = 4  // no base image: run `controlsnack image build`
	Killed         = 5  // the kill switch fired, or the process was interrupted
	Timeout        = 6  // the session timeout expired
	NotImplemented = 70 // the command exists but its implementation has not landed yet
)

// ErrNotImplemented marks functionality that a later task of PLAN.md section 12 delivers.
var ErrNotImplemented = errors.New("not implemented yet")

// Error is an error that carries the exit code the process should end with.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }

// Unwrap lets errors.Is and errors.As see through the code wrapper.
func (e *Error) Unwrap() error { return e.Err }

// New attaches an exit code to err. A nil err stays nil.
func New(code int, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

// Usagef builds a usage error (exit code 2).
func Usagef(format string, args ...any) error {
	return &Error{Code: Usage, Err: fmt.Errorf(format, args...)}
}

// Pending builds the error returned by stubs: it names the PLAN.md task that will implement the command.
func Pending(task string) error {
	return &Error{Code: NotImplemented, Err: fmt.Errorf("%w (planned for %s)", ErrNotImplemented, task)}
}

// Of maps an error to the exit code the process should end with.
func Of(err error) int {
	var coded *Error
	switch {
	case err == nil:
		return OK
	case errors.As(err, &coded):
		return coded.Code
	case errors.Is(err, ErrNotImplemented):
		return NotImplemented
	case errors.Is(err, context.DeadlineExceeded):
		return Timeout
	case errors.Is(err, context.Canceled):
		return Killed
	default:
		return Failure
	}
}
