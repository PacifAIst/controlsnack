// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/vm"
)

// sessionOf collects the session a runner assembled, once it has.
type sessionOf struct {
	mu      sync.Mutex
	s       *Session
	changes []Change
}

func (so *sessionOf) hook(f *fullRig) {
	f.r.deps.assembled = func(s *Session) {
		so.mu.Lock()
		so.s = s
		so.mu.Unlock()
		s.Machine.OnChange(func(c Change) { so.mu.Lock(); so.changes = append(so.changes, c); so.mu.Unlock() })
	}
}

func (so *sessionOf) states() string {
	so.mu.Lock()
	defer so.mu.Unlock()
	var out []string
	for _, c := range so.changes {
		out = append(out, string(c.To))
	}
	return strings.Join(out, ",")
}

func TestTheStatesOfASessionAreThoseOfThePlanInTheirOrder(t *testing.T) {
	f := newFullRig(t)
	var so sessionOf
	so.hook(f)
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	if err := f.run(context.Background(), headlessConfig("Anything")); err != nil {
		t.Fatal(err)
	}
	if got := so.states(); got != "handshake,ready,running,ready,ending,ended" {
		t.Errorf("states %s", got)
	}
	so.mu.Lock()
	last := so.changes[len(so.changes)-1]
	so.mu.Unlock()
	if last.Reason != "task done" {
		t.Errorf("the session ended because %q", last.Reason)
	}
}

func TestAnOrderlyEndStopsTheTaskBeforeItStopsTheVmAndCleansUp(t *testing.T) {
	f := newFullRig(t)
	var canceledAt int64
	f.model.replies = []reply{{hook: func(ctx context.Context) { <-ctx.Done(); canceledAt = time.Now().UnixNano() }, err: context.Canceled}}
	cfg := headlessConfig("Wait")
	cfg.Safety.Timeout = config.Duration(100 * time.Millisecond)
	began := time.Now()
	if err := f.run(context.Background(), cfg); exitcode.Of(err) != exitcode.Timeout {
		t.Fatalf("error %v", err)
	}
	if time.Since(began) > 5*time.Second {
		t.Errorf("the end took %s: the task was not stopped, the end waited for it", time.Since(began))
	}
	if canceledAt == 0 || f.mach.closedAt.Load() == 0 || canceledAt > f.mach.closedAt.Load() {
		t.Errorf("the task was stopped at %d and the VM at %d", canceledAt, f.mach.closedAt.Load())
	}
	if !f.mach.agentStoped.Load() {
		t.Error("the connection to the agent was left open")
	}
	if entries, _ := os.ReadDir(filepath.Join(f.dirs.State, "run")); len(entries) != 0 {
		t.Errorf("the session's directory was left behind: %v", entries)
	}
}

func TestAViewerSessionNobodyUsesTellsThePageAndClosesTheDoor(t *testing.T) {
	f := newFullRig(t)
	ft := &fakeTimers{}
	f.r.deps.afterFunc = ft.after
	cfg := config.Defaults()
	cfg.Safety.IdleTimeout = config.Duration(1500 * time.Millisecond)
	pg, done, cancel := f.startWithPage(t, cfg)
	defer cancel()
	ended := pg.waitFor("the end", ofType("ended"))
	if ended["reason"] != "idle timeout" || !strings.Contains(ended["summary"].(string), "ended (idle timeout)") {
		t.Errorf("ended %+v", ended)
	}
	if err := f.waitEnd(t, done); err != nil {
		t.Errorf("error %v", err)
	}
	// Nothing answers at the viewer's address any more, and the dead-man switch has nothing left to do.
	f.mu.Lock()
	u := f.launch[0].URL
	f.mu.Unlock()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 2 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Error("the viewer's server still answers")
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	for i, tm := range ft.timers {
		if !tm.stopped {
			t.Errorf("dead-man timer %d is still running", i)
		}
	}
}

func TestAKillThatDoesNotReachTheVmIsAnError(t *testing.T) {
	f := newFullRig(t)
	f.mach.proc.killErr = errors.New("access denied")
	f.model.replies = []reply{{hook: func(ctx context.Context) { <-ctx.Done() }, err: context.Canceled}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, headlessConfig("Wait")) }()
	eventually(t, "the model to be asked", func() bool { return len(f.model.requests()) == 1 })
	cancel()
	err := f.waitEnd(t, done)
	if err == nil || !strings.Contains(err.Error(), "the VM may still be running") {
		t.Errorf("error %v", err)
	}
}

func TestASessionThatCannotBePutTogetherAfterTheBootDoesNotLeaveTheVmRunning(t *testing.T) {
	f := newFullRig(t)
	rules := filepath.Join(f.dirs.Config, "bad.yaml")
	if err := os.MkdirAll(f.dirs.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rules, []byte("version: 1\nrules: [this is not a rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := headlessConfig("Anything")
	cfg.Safety.Policy = rules
	err := f.run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "the policy rules") {
		t.Errorf("error %v", err)
	}
	if !f.mach.proc.killed.Load() {
		t.Error("QEMU was left running")
	}
}

func TestAModelThatGivesUpIsAWarningForThePerson(t *testing.T) {
	f := newFullRig(t)
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":false,"summary":"it is not there"}`))}
	pg, done, cancel := f.startWithPage(t, config.Defaults())
	defer cancel()
	pg.send(map[string]any{"type": "chat", "text": "Find it"})
	n := pg.waitFor("the notice", func(m map[string]any) bool {
		return m["type"] == "notice" && strings.Contains(m["text"].(string), "The model gave up after 1 turns: it is not there")
	})
	if n["level"] != "warn" {
		t.Errorf("notice %+v", n)
	}
	cancel()
	_ = f.waitEnd(t, done)
}

func TestASoftwareFallbackIsInTheHelloAndTheSummary(t *testing.T) {
	f := newFullRig(t)
	f.mach.fallback = &vm.Fallback{Requested: "auto", From: "kvm", Used: "tcg", Reason: "/dev/kvm is not accessible"}
	f.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	pg, done, cancel := f.startWithPage(t, config.Defaults())
	defer cancel()
	hello := pg.waitFor("the hello", ofType("hello"))
	acc, _ := hello["accel"].(map[string]any)
	if acc["used"] != "tcg" || acc["fallback_reason"] != "/dev/kvm is not accessible" {
		t.Errorf("hello accel %+v", acc)
	}
	pg.send(map[string]any{"type": "control", "op": "kill", "src": "button"})
	_ = f.waitEnd(t, done)
	if !strings.Contains(f.out.String(), "Accelerator: tcg (kvm failed: /dev/kvm is not accessible)") {
		t.Errorf("the summary:\n%s", f.out.String())
	}
}
