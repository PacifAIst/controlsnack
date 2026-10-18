// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// startWithPage runs a session with a viewer in a goroutine and returns the page, once it has logged in and the session is ready.
func (f *fullRig) startWithPage(t *testing.T, cfg config.Config) (*page, <-chan error, context.CancelFunc) {
	t.Helper()
	cfg.Viewer.Mode = viewer.ModeBrowser
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, cfg) }()
	eventually(t, "the viewer to be launched", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.launch) == 1 })
	f.mu.Lock()
	opts := f.launch[0]
	f.mu.Unlock()
	pg := openPage(t, opts.URL)
	pg.waitFor("the session to be ready", func(m map[string]any) bool { return m["type"] == "state" && m["state"] == "ready" })
	return pg, done, cancel
}

func (f *fullRig) waitEnd(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
		return nil
	}
}

func TestAnActionThatNeedsApprovalGoesToThePageAndTheAnswerComesBack(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{
		calls(tc("c1", "exec", `{"command":"rm -r ~/work/old","timeout_s":30}`)),
		calls(tc("c2", "done", `{"success":true,"summary":"removed"}`)),
	}
	pg, done, cancel := f.startWithPage(t, config.Defaults())
	defer cancel()
	pg.send(map[string]any{"type": "chat", "text": "Clean up the old work"})
	req := pg.waitFor("the question", ofType("confirm_request"))
	if req["tool"] != "exec" || !strings.Contains(string(mustJSON(req["args"])), "rm -r ~/work/old") || req["id"] == "" {
		t.Fatalf("question %+v", req)
	}
	rules, _ := req["rules"].([]any)
	if len(rules) == 0 {
		t.Errorf("the page is not told which rule asked: %+v", req)
	}
	pg.waitFor("the session to wait for the person", func(m map[string]any) bool { return m["type"] == "state" && m["state"] == "awaiting_confirm" })
	pg.send(map[string]any{"type": "confirm", "id": req["id"], "approve": true, "note": "yes, it is old"})
	pg.waitFor("the question to be closed", func(m map[string]any) bool { return m["type"] == "confirm_resolved" && m["outcome"] == "approved" })
	pg.waitFor("the end of the task", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "Task finished in 2 turns: removed")
	})
	if tools := toolTexts(f.model.requests()[1]); len(tools) != 1 || !strings.Contains(tools[0], "The user approved it and wrote: yes, it is old") {
		t.Errorf("the model was not told: %q", tools)
	}
	pg.send(map[string]any{"type": "control", "op": "kill", "src": "button"})
	if err := f.waitEnd(t, done); exitcode.Of(err) != exitcode.Killed {
		t.Errorf("error %v", err)
	}
	recs := f.auditRecords()
	if len(recordsOf(recs, "user", "confirm")) != 1 {
		t.Error("the person's answer is not in the log")
	}
	var verdicts []string
	for _, r := range recordsOf(recs, "action", "") {
		verdicts = append(verdicts, r.Event+":"+r.Verdict)
	}
	if got := strings.Join(verdicts, ","); got != "decision:confirmed,result:confirmed,result:allowed" {
		t.Errorf("verdicts %s", got)
	}
	if !strings.Contains(f.out.String(), "2 actions (1 confirmed, 1 allowed)") && !strings.Contains(f.out.String(), "2 actions (1 allowed, 1 confirmed)") {
		t.Errorf("the summary:\n%s", f.out.String())
	}
}

func TestStopFromThePageEndsTheTaskAndLeavesTheSessionReady(t *testing.T) {
	f := newFullRig(t)
	started := make(chan struct{})
	f.model.replies = []reply{{hook: func(ctx context.Context) { close(started); <-ctx.Done() }, err: context.Canceled}}
	pg, done, cancel := f.startWithPage(t, config.Defaults())
	defer cancel()
	pg.send(map[string]any{"type": "chat", "text": "Wait forever"})
	<-started
	pg.send(map[string]any{"type": "control", "op": "stop"})
	pg.waitFor("the notice that the task stopped", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "Task stopped")
	})
	// The session is ready again: the next message is a new task.
	pg.waitFor("ready again", func(m map[string]any) bool {
		return m["type"] == "state" && m["state"] == "ready" && m["reason"] == "task stopped"
	})
	f.model.mu.Lock()
	f.model.replies = append(f.model.replies, calls(tc("c1", "done", `{"success":true,"summary":"second"}`)))
	f.model.mu.Unlock()
	pg.send(map[string]any{"type": "chat", "text": "Something else"})
	pg.waitFor("the second task", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "Task finished in 1 turns: second")
	})
	// The terminal carries the notices; the words and the actions are for the page.
	if out := f.out.String(); !strings.Contains(out, "NOTE: Task finished in 1 turns: second") || strings.Contains(out, "you: ") || strings.Contains(out, "model: ") || strings.Contains(out, "  turn ") {
		t.Errorf("the terminal with a viewer:\n%s", out)
	}
	cancel()
	if err := f.waitEnd(t, done); exitcode.Of(err) != exitcode.Killed {
		t.Errorf("error %v", err)
	}
}

func TestWhenThePageIsNotThereNothingCanBeApproved(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{
		calls(tc("c1", "exec", `{"command":"rm -r ~/work/old","timeout_s":30}`)),
		calls(tc("c2", "done", `{"success":false,"summary":"not allowed"}`)),
	}
	cfg := config.Defaults()
	cfg.Viewer.Mode = viewer.ModeBrowser
	cfg.Session.Task = "Clean up"
	// The window is "opened" but nobody ever logs in.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, cfg) }()
	eventually(t, "the model to be asked twice", func() bool { return len(f.model.requests()) >= 2 })
	cancel()
	_ = f.waitEnd(t, done)
	if tools := toolTexts(f.model.requests()[1]); len(tools) != 1 || !strings.Contains(tools[0], "user_rejected") || !strings.Contains(tools[0], "Nobody is watching") {
		t.Errorf("results %q", tools)
	}
}

func TestWhatThePageIsToldBeforeItConnectsIsThereWhenItDoes(t *testing.T) {
	f := newFullRig(t)
	f.finder.target.Local, f.finder.target.BaseURL = false, "https://api.example.org/v1"
	f.finder.caps.Vision = false
	pg, done, cancel := f.startWithPage(t, config.Defaults())
	defer cancel()
	hello := pg.waitFor("the hello", ofType("hello"))
	if hello["remote_llm"] != true || hello["mode"] != "text_only" {
		t.Errorf("hello %+v", hello)
	}
	pg.waitFor("the notice about the other machine", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "not on this machine")
	})
	pg.waitFor("the notice about the missing eyes", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "TEXT-ONLY")
	})
	cancel()
	_ = f.waitEnd(t, done)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestTheHeaderFiguresCountTheActionsOnceAndTimeTheModel(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{
		{hook: func(context.Context) { time.Sleep(40 * time.Millisecond) }, calls: []parse.ToolCall{tc("c1", "exec", `{"command":"uname -r","timeout_s":30}`)}},
		{hook: func(context.Context) { time.Sleep(40 * time.Millisecond) }, calls: []parse.ToolCall{tc("c2", "done", `{"success":true,"summary":"ok"}`)}},
	}
	pg, done, cancel := f.startWithPage(t, config.Defaults())
	defer cancel()
	pg.send(map[string]any{"type": "chat", "text": "Print the kernel version"})
	pg.waitFor("the end of the task", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "Task finished")
	})
	// Stats come every two seconds; the one after the task counts one action (the other, done, is not run by the guest).
	st := pg.waitFor("the figures after the task", func(m map[string]any) bool {
		a, _ := m["actions"].(float64)
		return m["type"] == "stats" && a >= 1
	})
	if ms, _ := st["llm_ms"].(float64); st["actions"] != float64(1) || ms < 40 {
		t.Errorf("stats %+v", st)
	}
	cancel()
	_ = f.waitEnd(t, done)
}

func TestASessionNobodyUsesEndsByItself(t *testing.T) {
	f := newFullRig(t)
	cfg := config.Defaults()
	cfg.Viewer.Mode = viewer.ModeBrowser
	cfg.Safety.IdleTimeout = config.Duration(50 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- f.run(context.Background(), cfg) }()
	if err := f.waitEnd(t, done); err != nil {
		t.Errorf("error %v: being idle is not a failure", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "Nobody did anything for 50ms, so the session ends.") || !strings.Contains(out, "ended (idle timeout)") {
		t.Errorf("the output:\n%s", out)
	}
	if !f.mach.closed.Load() {
		t.Error("the VM was not stopped in order")
	}
}

func TestAPageIsToldWhetherStepModeIsOnWhenItConnects(t *testing.T) {
	f := newFullRig(t)
	cfg := config.Defaults()
	cfg.Safety.Step = true
	pg, done, cancel := f.startWithPage(t, cfg)
	defer cancel()
	if h := pg.waitFor("the hello", ofType("hello")); h["step"] != true {
		t.Errorf("--step is on and the page is told %v", h["step"])
	}
	// Someone switches it off; a window that connects again is told so, and not what it started with.
	pg.send(map[string]any{"type": "control", "op": "step_off"})
	pg.waitFor("the notice that step mode is off", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "Step mode is off")
	})
	again := pg.again()
	if h := again.waitFor("the hello of the second connection", ofType("hello")); h["step"] != false {
		t.Errorf("step mode was switched off and the page is told %v", h["step"])
	}
	again.send(map[string]any{"type": "control", "op": "step_on"})
	again.waitFor("the notice that step mode is on", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "Step mode is on")
	})
	third := again.again()
	if h := third.waitFor("the hello of the third connection", ofType("hello")); h["step"] != true {
		t.Errorf("step mode was switched on again and the page is told %v", h["step"])
	}
	cancel()
	_ = f.waitEnd(t, done)
}
