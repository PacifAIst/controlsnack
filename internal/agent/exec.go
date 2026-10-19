// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/agent/execx"
	"github.com/PacifAIst/controlsnack/internal/agent/server"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// Limits of an `exec` request. The host's tool allows 4000 characters of command; the agent is more generous, because
// the policy and the limits of the tool are the host's business, and refuses only what it cannot safely carry.
const (
	maxCommandBytes = 16 << 10
	maxCwdBytes     = 4096
	maxStdinBytes   = 64 << 10
)

// Runner runs one command; execx.Run is the real one.
type Runner func(ctx context.Context, o execx.Options) (execx.Result, error)

// Exec serves `exec` requests: bash -lc as the guest user (PLAN.md section 8). Whether a command may run at all is the
// host's decision (the exec policy, section 6.3); the agent only runs what the host sent, within limits.
type Exec struct {
	Run Runner
}

// NewExec returns the handler with the real runner.
func NewExec() *Exec { return &Exec{Run: execx.Run} }

// Handle runs the command and reports its exit code and output.
func (e *Exec) Handle(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.ExecReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	o, err := execOptions(r)
	if err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	res, err := e.Run(ctx, o)
	if err != nil {
		var bad *execx.ErrBadDir
		switch {
		case ctx.Err() != nil:
			return nil, nil, ctx.Err() // the dispatcher answers a canceled request itself
		case errors.As(err, &bad):
			return nil, nil, server.Fail(protocol.CodeInvalidArgs, "cwd: %v", err)
		case errors.Is(err, execx.ErrUnsupported):
			return nil, nil, server.Fail(protocol.CodeUnsupported, "%v", err)
		}
		return nil, nil, server.Fail(protocol.CodeInternal, "running the command: %v", err)
	}
	return protocol.ExecRes{ExitCode: res.ExitCode, DurationMS: int(res.Duration / time.Millisecond), Stdout: res.Stdout,
		Stderr: res.Stderr, Truncated: res.Truncated, TimedOut: res.TimedOut}, nil, nil
}

// execOptions validates a request and turns it into the runner's options.
func execOptions(r protocol.ExecReq) (execx.Options, error) {
	var o execx.Options
	switch {
	case strings.TrimSpace(r.Command) == "":
		return o, errors.New("command is empty")
	case len(r.Command) > maxCommandBytes:
		return o, errors.New("command is longer than 16 KiB")
	case !utf8.ValidString(r.Command):
		return o, errors.New("command is not valid UTF-8")
	case strings.ContainsRune(r.Command, 0):
		return o, errors.New("command contains a NUL byte")
	case len(r.Cwd) > maxCwdBytes || strings.ContainsRune(r.Cwd, 0):
		return o, errors.New("cwd is not a usable path")
	case len(r.Stdin) > maxStdinBytes:
		return o, errors.New("stdin is longer than 64 KiB")
	case r.TimeoutMS != 0 && (time.Duration(r.TimeoutMS)*time.Millisecond < execx.MinTimeout || time.Duration(r.TimeoutMS)*time.Millisecond > execx.MaxTimeout):
		return o, errors.New("timeout_ms is 100 to 600000 (0 means 60000)")
	case r.MaxOutput != 0 && (r.MaxOutput < execx.MinMaxOutput || r.MaxOutput > execx.MaxMaxOutput):
		return o, errors.New("max_output is 1024 to 131072 bytes per stream (0 means 65536)")
	}
	o.Command, o.Dir, o.Stdin = r.Command, r.Cwd, r.Stdin
	o.Timeout = time.Duration(r.TimeoutMS) * time.Millisecond
	o.MaxOutput = r.MaxOutput
	return o, nil
}
