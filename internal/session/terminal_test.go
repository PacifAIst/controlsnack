// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

func TestTheTerminalIsToldWhatTheViewerWouldShow(t *testing.T) {
	var out syncBuf
	all := &terminalSink{out: &out, all: true}
	all.Send(viewer.Notice{Level: viewer.LevelInfo, Text: "fyi"})
	all.Send(viewer.Notice{Level: viewer.LevelWarn, Text: "careful"})
	all.Send(viewer.Notice{Level: viewer.LevelError, Text: "broken"})
	all.Send(viewer.Chat{Role: "assistant", Text: "I will look"})
	all.Send(viewer.Chat{Role: "user", Text: "please"})
	all.Send(viewer.Trace{Turn: 2, Tool: "exec", ActionID: "t2a1", Status: "running", Summary: "starting"})
	all.Send(viewer.Trace{Turn: 2, Tool: "exec", ActionID: "t2a1", Status: "done", Summary: "exit=0"})
	all.Send(viewer.Ended{Reason: "x"})
	want := "NOTE: fyi\nWARNING: careful\nERROR: broken\nmodel: I will look\nyou: please\n  turn 2, exec t2a1 (done): exit=0\n"
	if got := out.String(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	// With a viewer only the notices are written.
	var quiet syncBuf
	only := &terminalSink{out: &quiet}
	only.Send(viewer.Chat{Role: "assistant", Text: "words"})
	only.Send(viewer.Trace{Status: "done"})
	only.Send(viewer.Notice{Level: viewer.LevelWarn, Text: "careful"})
	if got := quiet.String(); got != "WARNING: careful\n" {
		t.Errorf("got %q", got)
	}
	// A tee sends to each.
	var a, b syncBuf
	teeSink{&terminalSink{out: &a}, &terminalSink{out: &b}}.Send(viewer.Notice{Level: viewer.LevelInfo, Text: "both"})
	if a.String() != "NOTE: both\n" || b.String() != "NOTE: both\n" {
		t.Errorf("tee: %q %q", a.String(), b.String())
	}
}

func TestAHeadlessTaskThatPausesEndsInsteadOfWaitingForNobody(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{{text: "words"}, {text: "more words"}, {text: "still words"}}
	err := f.run(context.Background(), headlessConfig("Anything"))
	if exitcode.Of(err) != exitcode.Failure || err == nil || !strings.Contains(err.Error(), "the task paused (the model is not giving usable answers) and nobody is there to resume it") {
		t.Errorf("error %v", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "WARNING: The model's answers could not be used 3 times in a row") {
		t.Errorf("the person is not told on the terminal why:\n%s", out)
	}
	if !f.mach.closed.Load() {
		t.Error("the VM was left running")
	}
}

func TestAHeadlessTaskThatIsStoppedWithoutPausingIsAFailureToo(t *testing.T) {
	f := newFullRig(t)
	var sess atomic.Pointer[Session]
	f.r.deps.assembled = func(s *Session) { sess.Store(s) }
	f.model.replies = []reply{{hook: func(ctx context.Context) { sess.Load().Console.Stop(); <-ctx.Done() }, err: context.Canceled}}
	err := f.run(context.Background(), headlessConfig("Anything"))
	if exitcode.Of(err) != exitcode.Failure || err == nil || err.Error() != "the task was stopped" {
		t.Errorf("error %v", err)
	}
	if !f.mach.closed.Load() {
		t.Error("the VM was left running")
	}
}
