// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/execx"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

func runnerReturning(res execx.Result, err error, seen *execx.Options) Runner {
	return func(_ context.Context, o execx.Options) (execx.Result, error) {
		if seen != nil {
			*seen = o
		}
		return res, err
	}
}

func execFrame(t *testing.T, body any) protocol.Frame {
	t.Helper()
	return frame(t, protocol.TypeExec, body)
}

func TestExecReportsWhatTheCommandDid(t *testing.T) {
	var seen execx.Options
	e := &Exec{Run: runnerReturning(execx.Result{ExitCode: 3, Duration: 1500 * time.Millisecond, Stdout: "out", Stderr: "err", Truncated: true}, nil, &seen)}
	res, blob, err := e.Handle(context.Background(), execFrame(t, protocol.ExecReq{Command: "make test", Cwd: "/home/cs/work/app",
		TimeoutMS: 30000, Stdin: "input", MaxOutput: 4096}))
	if err != nil || blob != nil {
		t.Fatalf("%v %v", err, blob)
	}
	got := res.(protocol.ExecRes)
	if got != (protocol.ExecRes{ExitCode: 3, DurationMS: 1500, Stdout: "out", Stderr: "err", Truncated: true}) {
		t.Errorf("%+v", got)
	}
	if seen.Command != "make test" || seen.Dir != "/home/cs/work/app" || seen.Stdin != "input" || seen.Timeout != 30*time.Second || seen.MaxOutput != 4096 {
		t.Errorf("the request reaches the runner: %+v", seen)
	}
	// Defaults are the runner's business: zero values pass through.
	_, _, _ = e.Handle(context.Background(), execFrame(t, protocol.ExecReq{Command: "ls"}))
	if seen.Dir != "" || seen.Timeout != 0 || seen.MaxOutput != 0 {
		t.Errorf("%+v", seen)
	}
}

func TestExecReportsATimeout(t *testing.T) {
	e := &Exec{Run: runnerReturning(execx.Result{ExitCode: 124, Duration: 2 * time.Second, TimedOut: true, Stdout: "partial"}, nil, nil)}
	res, _, err := e.Handle(context.Background(), execFrame(t, protocol.ExecReq{Command: "sleep 100", TimeoutMS: 2000}))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.(protocol.ExecRes); !got.TimedOut || got.ExitCode != 124 || got.Stdout != "partial" {
		t.Errorf("a timeout is a result, not an error: %+v", got)
	}
}

func TestExecRefusals(t *testing.T) {
	tests := []struct {
		name string
		req  any
		want string
	}{
		{"empty command", protocol.ExecReq{}, "command is empty"},
		{"blank command", protocol.ExecReq{Command: " \n\t "}, "command is empty"},
		{"a command over 16 KiB", protocol.ExecReq{Command: strings.Repeat("a", 16<<10+1)}, "longer than 16 KiB"},
		{"a NUL in the command", protocol.ExecReq{Command: "echo a\x00b"}, "NUL"},
		{"a NUL in the cwd", protocol.ExecReq{Command: "ls", Cwd: "/tmp\x00x"}, "cwd"},
		{"a cwd that is too long", protocol.ExecReq{Command: "ls", Cwd: "/" + strings.Repeat("a", 5000)}, "cwd"},
		{"stdin over 64 KiB", protocol.ExecReq{Command: "cat", Stdin: strings.Repeat("a", 64<<10+1)}, "stdin"},
		{"timeout under 100 ms", protocol.ExecReq{Command: "ls", TimeoutMS: 99}, "timeout_ms"},
		{"negative timeout", protocol.ExecReq{Command: "ls", TimeoutMS: -5}, "timeout_ms"},
		{"timeout over 10 minutes", protocol.ExecReq{Command: "ls", TimeoutMS: 600001}, "timeout_ms"},
		{"max_output under 1 KiB", protocol.ExecReq{Command: "ls", MaxOutput: 1000}, "max_output"},
		{"max_output over 128 KiB", protocol.ExecReq{Command: "ls", MaxOutput: 128<<10 + 1}, "max_output"},
		{"negative max_output", protocol.ExecReq{Command: "ls", MaxOutput: -1}, "max_output"},
		{"an unknown field", map[string]any{"command": "ls", "sudo": true}, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			e := &Exec{Run: func(context.Context, execx.Options) (execx.Result, error) { ran = true; return execx.Result{}, nil }}
			_, _, err := e.Handle(context.Background(), execFrame(t, tt.req))
			if code(err) != protocol.CodeInvalidArgs || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
			if ran {
				t.Error("a refused request runs nothing")
			}
		})
	}
	// The edges are fine.
	for _, ok := range []protocol.ExecReq{
		{Command: "ls", TimeoutMS: 100}, {Command: "ls", TimeoutMS: 600000}, {Command: "ls", MaxOutput: 1024}, {Command: "ls", MaxOutput: 128 << 10},
		{Command: strings.Repeat("a", 16<<10)}, {Command: "cat", Stdin: strings.Repeat("a", 64<<10)},
	} {
		e := &Exec{Run: runnerReturning(execx.Result{}, nil, nil)}
		if _, _, err := e.Handle(context.Background(), execFrame(t, ok)); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
}

func TestExecErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		want string
	}{
		{"bad directory", &execx.ErrBadDir{Dir: "/nope", Reason: "it does not exist"}, protocol.CodeInvalidArgs, "cwd"},
		{"no process groups", execx.ErrUnsupported, protocol.CodeUnsupported, "Unix"},
		{"anything else", errors.New("fork: resource temporarily unavailable"), protocol.CodeInternal, "resource temporarily"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Exec{Run: runnerReturning(execx.Result{}, tt.err, nil)}
			_, _, err := e.Handle(context.Background(), execFrame(t, protocol.ExecReq{Command: "ls"}))
			if code(err) != tt.code || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestExecCancellationStaysACancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Exec{Run: func(ctx context.Context, _ execx.Options) (execx.Result, error) {
		cancel()
		return execx.Result{Stdout: "partial"}, ctx.Err()
	}}
	_, _, err := e.Handle(ctx, execFrame(t, protocol.ExecReq{Command: "sleep 100"}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the dispatcher answers a canceled request itself: %v", err)
	}
}

func TestNewExecUsesTheRealRunner(t *testing.T) {
	if NewExec().Run == nil {
		t.Error("no runner")
	}
}
