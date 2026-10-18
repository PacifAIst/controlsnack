// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/confirm"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/safety/policy"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// log is a fake audit log that keeps what it is given, and can be made to fail.
type log struct {
	mu     sync.Mutex
	recs   []audit.Record
	failAt int // the number of the append that fails (1 is the first); 0 never
}

func (l *log) Append(r audit.Record) (audit.Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failAt > 0 && len(l.recs)+1 == l.failAt {
		return audit.Record{}, errors.New("disk full")
	}
	l.recs = append(l.recs, r)
	return r, nil
}

func (l *log) all() []audit.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]audit.Record(nil), l.recs...)
}

// events are the viewer's messages, in order.
type events struct {
	mu   sync.Mutex
	msgs []viewer.Message
}

func (e *events) Send(m viewer.Message) { e.mu.Lock(); e.msgs = append(e.msgs, m); e.mu.Unlock() }

func (e *events) traces() []viewer.Trace {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []viewer.Trace
	for _, m := range e.msgs {
		if t, ok := m.(viewer.Trace); ok {
			out = append(out, t)
		}
	}
	return out
}

func (e *events) plans() []viewer.Plan {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []viewer.Plan
	for _, m := range e.msgs {
		if p, ok := m.(viewer.Plan); ok {
			out = append(out, p)
		}
	}
	return out
}

// asker is the viewer's side of a confirmation: it keeps the requests and answers them as told.
type asker struct {
	broker  *confirm.Broker
	mu      sync.Mutex
	reqs    []confirm.Request
	approve bool
	note    string
	answer  bool // answer at once; otherwise the test answers when it chooses
	err     error
}

func (a *asker) Present(_ context.Context, r confirm.Request) error {
	a.mu.Lock()
	a.reqs = append(a.reqs, r)
	answer, approve, note, err := a.answer, a.approve, a.note, a.err
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if answer {
		go func() { _ = a.broker.Resolve(r.ID, approve, note) }()
	}
	return nil
}

func (a *asker) Resolved(string, confirm.Outcome) {}

func (a *asker) requests() []confirm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]confirm.Request(nil), a.reqs...)
}

// dgrid is a dispatcher with the real rules, broker and limits and fake guest, log and viewer.
type dgrid struct {
	t       *testing.T
	d       *Dispatcher
	agent   *fakeAgent
	log     *log
	view    *events
	asker   *asker
	machine *Machine
	changes []Change
	killed  bool
	clock   *limits.FakeClock
}

func newDgrid(t *testing.T, mod ...func(*config.Config)) *dgrid {
	t.Helper()
	cfg := config.Defaults()
	for _, m := range mod {
		m(&cfg)
	}
	prof := safety.ProfileFor(cfg)
	rs, err := policy.DefaultRuleSet()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := policy.NewEngine(rs, policy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := &dgrid{t: t, agent: newAgent(), log: &log{}, view: &events{}, clock: limits.NewFakeClock()}
	r.agent.on(protocol.TypeExec, func(req any, out any) error {
		*out.(*protocol.ExecRes) = protocol.ExecRes{ExitCode: 0, DurationMS: 4, Stdout: "ok\n"}
		return nil
	}).on(protocol.TypeInput, func(req any, out any) error {
		*out.(*protocol.InputRes) = protocol.InputRes{ScreenHashAfter: "after"}
		return nil
	}).on(protocol.TypeA11yAction, func(req any, out any) error {
		*out.(*protocol.A11yActionRes) = protocol.A11yActionRes{Method: "action:click"}
		return nil
	})
	r.machine = NewMachine(nil)
	for _, s := range []State{Handshake, Ready, Running} {
		if err := r.machine.To(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	r.machine.OnChange(func(c Change) { r.changes = append(r.changes, c) })
	exec := NewExecutor(r.agent)
	exec.SetView(vision.RawSeen{Tree: &protocol.A11yTreeRes{Generation: "g1", Nodes: []protocol.A11yNode{
		{ID: "a1", Role: "push button", Name: "Open", Bounds: protocol.Rect{100, 200, 80, 24}, Actions: []string{"click"}},
		{ID: "a2", Role: "push button", Name: "Delete", Bounds: protocol.Rect{960, 540, 100, 40}, Actions: []string{"click"}},
		{ID: "a3", Role: "password text", Name: "Password", Bounds: protocol.Rect{300, 300, 200, 24}, States: []string{"focused", "editable"}},
	}}})
	br := confirm.New(confirm.Options{Timeout: 5 * time.Second})
	r.asker = &asker{broker: br}
	br.SetPresenter(r.asker)
	r.d = &Dispatcher{
		Exec: exec, Policy: eng, Profile: prof, Broker: br, Limits: limits.New(prof, r.clock), Audit: r.log, Viewer: r.view, Machine: r.machine,
		Killed: func() bool { return r.killed }, Redactor: secret.NewRedactor(),
	}
	return r
}

// guestMapper is a picture of 1280 by 720 of a guest screen of 1920 by 1080.
func guestMapper(t *testing.T) vision.Mapper {
	t.Helper()
	m, err := vision.NewMapper(prompt.Size{W: 1920, H: 1080}, prompt.Size{W: 1280, H: 720})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (r *dgrid) run(a actions.Args, reason string) Step {
	r.t.Helper()
	raw, _ := json.Marshal(a)
	in := Input{Turn: 3, ID: "t3a1", CallID: "call_1", Action: actions.Action{Tool: a.Tool(), Args: a, Reason: reason, Raw: raw}, Mapper: guestMapper(r.t), ScreenSHA: strings.Repeat("ab", 32)}
	return r.d.Run(context.Background(), in)
}

func rec(t *testing.T, l *log, n int) audit.Record {
	t.Helper()
	all := l.all()
	if len(all) <= n {
		t.Fatalf("only %d records: %+v", len(all), all)
	}
	return all[n]
}

func TestAnAllowedActionIsOnRecordBeforeItRunsAndAfter(t *testing.T) {
	r := newDgrid(t)
	var atExec []audit.Record
	r.agent.on(protocol.TypeExec, func(req any, out any) error {
		atExec = r.log.all() // what the log held at the moment the command ran
		*out.(*protocol.ExecRes) = protocol.ExecRes{ExitCode: 0, DurationMS: 4, Stdout: "6.1.0\n"}
		return nil
	})
	step := r.run(&actions.ExecArgs{Command: "uname -r", TimeoutS: 60}, "check the kernel")
	if step.Stop != StopNone || !step.Result.OK || step.Result.Detail != "exit=0 4ms" || step.Result.CallID != "call_1" || step.Result.Tool != "exec" || step.Result.Action == "" {
		t.Fatalf("step %+v", step)
	}
	// The intent was written before the command ran; the result after.
	if len(atExec) != 1 || atExec[0].Event != "decision" || atExec[0].Verdict != "allowed" {
		t.Fatalf("at the moment of the command the log held %+v", atExec)
	}
	all := r.log.all()
	if len(all) != 2 {
		t.Fatalf("%d records", len(all))
	}
	d0, d1 := all[0], all[1]
	if d0.Kind != audit.KindAction || d0.Event != "decision" || d0.Actor != "llm" || d0.Turn != 3 || d0.ActionID != "t3a1" || d0.Tool != "exec" || d0.Verdict != "allowed" ||
		d0.Reason != "check the kernel" || d0.ScreenshotSHA256 != strings.Repeat("ab", 32) || !strings.Contains(string(d0.Args), "uname -r") {
		t.Errorf("decision record %+v", d0)
	}
	if d1.Event != "result" || d1.ActionID != "t3a1" || d1.Verdict != "allowed" || !strings.Contains(string(d1.Result), `"ok":true`) || !strings.Contains(string(d1.Result), `"duration_ms"`) {
		t.Errorf("result record %+v (%s)", d1, d1.Result)
	}
	// The viewer is told the plan, then that it runs, then how it ended.
	tr := r.view.traces()
	if len(tr) != 2 || tr[0].Status != "running" || tr[1].Status != "done" || tr[1].Verdict != "allow" || tr[1].Tool != "exec" || tr[1].Summary != "exit=0 4ms" || tr[1].Turn != 3 {
		t.Errorf("traces %+v", tr)
	}
	if len(r.view.plans()) != 1 || r.view.plans()[0].ActionID != "t3a1" {
		t.Errorf("plans %+v", r.view.plans())
	}
	if a, _, _ := r.d.Limits.Remaining(); a != safety.ProfileFor(config.Defaults()).ActionsPerTask-1 {
		t.Errorf("%d actions left: the action was not charged", a)
	}
	if len(r.changes) != 0 {
		t.Errorf("the state changed for an allowed action: %+v", r.changes)
	}
}

func TestADeniedActionIsNeverRunNeverChargedAndAlwaysRecorded(t *testing.T) {
	r := newDgrid(t)
	step := r.run(&actions.ExecArgs{Command: "rm -rf /", TimeoutS: 60}, "clean up")
	if step.Stop != StopBatch || step.Result.OK || step.Result.Code != actions.CodePolicyDenied || !strings.Contains(step.Result.Detail, "Refused by the safety rules") || !strings.Contains(step.Result.Detail, "Do not try to get around this") {
		t.Fatalf("step %+v", step)
	}
	if r.agent.count() != 0 {
		t.Error("a refused command reached the guest")
	}
	all := r.log.all()
	if len(all) != 1 || all[0].Verdict != "denied" || all[0].Event != "decision" || len(all[0].Rules) == 0 || all[0].Tool != "exec" {
		t.Fatalf("records %+v", all)
	}
	tr := r.view.traces()
	if len(tr) != 1 || tr[0].Status != "denied" || tr[0].Verdict != "deny" || len(tr[0].Rules) == 0 {
		t.Errorf("traces %+v", tr)
	}
	if len(r.view.plans()) != 0 {
		t.Error("the viewer was shown a plan for a refused action")
	}
	if a, _, _ := r.d.Limits.Remaining(); a != safety.ProfileFor(config.Defaults()).ActionsPerTask {
		t.Errorf("a refused action was charged: %d left", a)
	}
	if len(r.asker.requests()) != 0 {
		t.Error("the person was asked about a refused action")
	}
}

func TestAnActionThatNeedsConfirmationWaitsForThePersonAndTakesTheirNote(t *testing.T) {
	r := newDgrid(t)
	r.asker.answer, r.asker.approve, r.asker.note = true, true, "it is my own folder"
	var during State
	r.agent.on(protocol.TypeExec, func(req any, out any) error {
		during = r.machine.State()
		*out.(*protocol.ExecRes) = protocol.ExecRes{ExitCode: 0, DurationMS: 4}
		return nil
	})
	step := r.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "clean up")
	if step.Stop != StopNone || !step.Result.OK || !strings.Contains(step.Result.Detail, "The user approved it and wrote: it is my own folder") {
		t.Fatalf("step %+v", step)
	}
	reqs := r.asker.requests()
	if len(reqs) != 1 || reqs[0].Tool != "exec" || reqs[0].ActionID != "t3a1" || reqs[0].Reason != "clean up" || len(reqs[0].Rules) == 0 || reqs[0].Rules[0].Explanation == "" {
		t.Fatalf("requests %+v", reqs)
	}
	if args, ok := reqs[0].Args.(json.RawMessage); !ok || !strings.Contains(string(args), "rm -r ~/work/old") {
		t.Errorf("request args %#v", reqs[0].Args)
	}
	// The state was awaiting the person while the question was open, and back to running when the command ran.
	if len(r.changes) != 2 || r.changes[0].To != AwaitingConfirm || r.changes[1].To != Running || during != Running {
		t.Errorf("state changes %+v, during the command %s", r.changes, during)
	}
	all := r.log.all()
	if len(all) != 2 || all[0].Verdict != "confirmed" || all[1].Verdict != "confirmed" || len(all[0].Rules) == 0 {
		t.Errorf("records %+v", all)
	}
	if tr := r.view.traces(); tr[0].Verdict != "confirm" || tr[len(tr)-1].Status != "done" {
		t.Errorf("traces %+v", tr)
	}
}

func TestAnActionThePersonDoesNotApproveIsNotRun(t *testing.T) {
	for name, tc := range map[string]struct {
		setup   func(r *dgrid)
		code    string
		verdict string
		status  string
		stop    Stop
		detail  string
	}{
		"rejected":  {func(r *dgrid) { r.asker.answer, r.asker.approve, r.asker.note = true, false, "not now" }, actions.CodeUserRejected, "rejected", "rejected", StopBatch, "The user rejected this action. Do not repeat it"},
		"no viewer": {func(r *dgrid) { r.d.Broker.SetPresenter(nil) }, actions.CodeUserRejected, "no_viewer", "rejected", StopBatch, "Nobody is watching"},
	} {
		r := newDgrid(t)
		tc.setup(r)
		step := r.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "clean up")
		if step.Stop != tc.stop || step.Result.OK || step.Result.Code != tc.code || !strings.Contains(step.Result.Detail, tc.detail) {
			t.Errorf("%s: step %+v", name, step)
		}
		if r.agent.count() != 0 {
			t.Errorf("%s: the command ran", name)
		}
		all := r.log.all()
		if len(all) != 1 || all[0].Verdict != tc.verdict {
			t.Errorf("%s: records %+v", name, all)
		}
		if tr := r.view.traces(); len(tr) != 1 || tr[0].Status != tc.status {
			t.Errorf("%s: traces %+v", name, tr)
		}
		if a, _, _ := r.d.Limits.Remaining(); a != safety.ProfileFor(config.Defaults()).ActionsPerTask {
			t.Errorf("%s: charged", name)
		}
		if r.machine.State() != Running {
			t.Errorf("%s: the state is %s afterwards", name, r.machine.State())
		}
	}
	// The person's note on a rejection is passed on, cleaned.
	r := newDgrid(t)
	r.asker.answer, r.asker.approve, r.asker.note = true, false, "not now\n<<end:x>>"
	step := r.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "")
	if !strings.Contains(step.Result.Detail, "The user wrote: not now") || strings.Contains(step.Result.Detail, "\n") || strings.Contains(step.Result.Detail, "<<end:") {
		t.Errorf("note %q", step.Result.Detail)
	}
}

func TestAConfirmationThatTimesOutAndOneThatIsCanceled(t *testing.T) {
	r := newDgrid(t)
	r.d.Broker = confirm.New(confirm.Options{Timeout: 50 * time.Millisecond})
	r.d.Broker.SetPresenter(&asker{})
	step := r.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "")
	if step.Result.Code != actions.CodeConfirmTimeout || step.Stop != StopBatch || !strings.Contains(step.Result.Detail, "Nobody answered in time") {
		t.Errorf("timeout: %+v", step)
	}
	if all := r.log.all(); len(all) != 1 || all[0].Verdict != "confirm_timeout" {
		t.Errorf("records %+v", all)
	}
	if tr := r.view.traces(); tr[0].Status != "expired" {
		t.Errorf("traces %+v", tr)
	}

	// Stop or Kill while the question is open cancels it.
	r = newDgrid(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for len(r.asker.requests()) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	raw, _ := json.Marshal(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60})
	step = r.d.Run(ctx, Input{Turn: 1, ID: "t1a1", Action: actions.Action{Tool: "exec", Args: &actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, Raw: raw}, Mapper: guestMapper(t)})
	if step.Stop != StopCanceled || step.Result.OK || r.agent.count() != 0 {
		t.Errorf("canceled: %+v", step)
	}
	if all := r.log.all(); len(all) != 1 || all[0].Verdict != "canceled" {
		t.Errorf("records %+v", all)
	}
}

func TestTheProfileDecidesWhatTheVerdictOfTheRulesMeans(t *testing.T) {
	// Unsafe: what asked for confirmation runs, and what was refused is asked about.
	r := newDgrid(t, func(c *config.Config) { c.Safety.Unsafe = true })
	step := r.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "")
	if !step.Result.OK || len(r.asker.requests()) != 0 {
		t.Errorf("unsafe, a confirm-class action: %+v, asked %d", step, len(r.asker.requests()))
	}
	r = newDgrid(t, func(c *config.Config) { c.Safety.Unsafe = true })
	r.asker.answer, r.asker.approve = true, false
	step = r.run(&actions.ExecArgs{Command: "rm -rf /", TimeoutS: 60}, "")
	if step.Result.OK || step.Result.Code != actions.CodeUserRejected || len(r.asker.requests()) != 1 || r.agent.count() != 0 {
		t.Errorf("unsafe, a deny-class action: %+v", step)
	}
	// Step: even what is allowed asks.
	r = newDgrid(t, func(c *config.Config) { c.Safety.Step = true })
	r.asker.answer, r.asker.approve = true, true
	step = r.run(&actions.ExecArgs{Command: "uname -r", TimeoutS: 60}, "")
	if !step.Result.OK || len(r.asker.requests()) != 1 || r.log.all()[0].Verdict != "confirmed" {
		t.Errorf("step mode: %+v, asked %d", step, len(r.asker.requests()))
	}
}

func TestThePolicySeesTheGuestsCoordinatesAndTheLogKeepsTheModelsOwn(t *testing.T) {
	// The model clicks at (640, 360) of a 1280 by 720 picture: that is (960, 540) of the 1920 by 1080 guest, where the
	// button "Delete" is, so the click needs confirmation; at (100, 100) of the picture it is on nothing.
	r := newDgrid(t)
	r.asker.answer, r.asker.approve = true, true
	step := r.run(&actions.MouseArgs{Op: "click", X: 640, Y: 360}, "press the button")
	if !step.Result.OK || len(r.asker.requests()) != 1 {
		t.Fatalf("step %+v, asked %d", step, len(r.asker.requests()))
	}
	if ev := inputOf(t, r.agent.last()); ev.Type != "click" || ev.X != 960 || ev.Y != 540 {
		t.Errorf("the guest was clicked at %+v", ev)
	}
	if p := r.view.plans(); len(p) != 1 || p[0].X == nil || *p[0].X != 960 || *p[0].Y != 540 {
		t.Errorf("plan %+v", p)
	}
	dec := rec(t, r.log, 0)
	if !strings.Contains(string(dec.Args), `"x":640`) || strings.Contains(string(dec.Args), "960") {
		t.Errorf("the log should keep the model's coordinates: %s", dec.Args)
	}

	r = newDgrid(t)
	step = r.run(&actions.MouseArgs{Op: "click", X: 100, Y: 100}, "")
	if !step.Result.OK || len(r.asker.requests()) != 0 {
		t.Errorf("a click on nothing: %+v, asked %d", step, len(r.asker.requests()))
	}
}

func TestACoordinateOutsideThePictureIsTheModelsToCorrect(t *testing.T) {
	r := newDgrid(t)
	step := r.run(&actions.MouseArgs{Op: "click", X: 5000, Y: 100}, "")
	if step.Result.OK || step.Result.Code != actions.CodeInvalidArgs || step.Stop != StopBatch || !strings.Contains(step.Result.Detail, "args.x") {
		t.Fatalf("step %+v", step)
	}
	if r.agent.count() != 0 || len(r.asker.requests()) != 0 {
		t.Error("an action with a coordinate outside the picture went further")
	}
	if all := r.log.all(); len(all) != 1 || all[0].Verdict != "invalid" {
		t.Errorf("records %+v", all)
	}
}

func TestAKilledSessionDoesNothingMore(t *testing.T) {
	r := newDgrid(t)
	r.killed = true
	step := r.run(&actions.ExecArgs{Command: "uname -r", TimeoutS: 60}, "")
	if step.Stop != StopKilled || step.Result.OK || r.agent.count() != 0 || len(r.log.all()) != 0 {
		t.Errorf("step %+v, %d calls, %d records", step, r.agent.count(), len(r.log.all()))
	}
	// A kill that comes while the person is being asked: the approval does not run the action.
	r = newDgrid(t)
	r.asker.answer, r.asker.approve = false, true
	go func() {
		for len(r.asker.requests()) == 0 {
			time.Sleep(time.Millisecond)
		}
		r.killed = true
		_ = r.d.Broker.Resolve(r.asker.requests()[0].ID, true, "")
	}()
	step = r.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "")
	if step.Stop != StopKilled || r.agent.count() != 0 {
		t.Errorf("approved during a kill: %+v, %d calls", step, r.agent.count())
	}
	if all := r.log.all(); len(all) != 1 || all[0].Verdict != "killed" {
		t.Errorf("records %+v", all)
	}
}

func TestTheRateAndTheBudgetsStopWhatIsOverTheLimit(t *testing.T) {
	// Rate: with the profile's rate, a second action at once would wait for longer than the limiter allows.
	r := newDgrid(t, func(c *config.Config) { c.Safety.ActionsPerSec = 0.1 })
	if step := r.run(&actions.KeyArgs{Keys: "a"}, ""); !step.Result.OK {
		t.Fatalf("the first: %+v", step)
	}
	step := r.run(&actions.KeyArgs{Keys: "b"}, "")
	if step.Result.Code != actions.CodeRateLimited || step.Stop != StopBatch || step.Result.OK {
		t.Errorf("the second: %+v", step)
	}
	if all := r.log.all(); all[len(all)-1].Verdict != "rate_limited" {
		t.Errorf("records %+v", all)
	}
	// Budget: the task's allowance.
	r = newDgrid(t, func(c *config.Config) { c.Safety.ActionsPerTask = 1 })
	if step := r.run(&actions.KeyArgs{Keys: "a"}, ""); !step.Result.OK {
		t.Fatalf("the first: %+v", step)
	}
	step = r.run(&actions.KeyArgs{Keys: "b"}, "")
	if step.Result.Code != actions.CodeBudgetExhausted || step.Stop != StopBudget || r.agent.count() != 1 {
		t.Errorf("the second: %+v", step)
	}
	if all := r.log.all(); all[len(all)-1].Verdict != "budget" {
		t.Errorf("records %+v", all)
	}
	// Time: the session's own.
	r = newDgrid(t)
	r.clock.Advance(safety.ProfileFor(config.Defaults()).SessionTimeout + time.Second)
	step = r.run(&actions.KeyArgs{Keys: "a"}, "")
	if step.Stop != StopTimeout || step.Result.Code != actions.CodeBudgetExhausted || r.agent.count() != 0 {
		t.Errorf("after the session's time: %+v", step)
	}
}

func TestAnActionIsNotRunUnlessItsDecisionIsOnRecord(t *testing.T) {
	r := newDgrid(t)
	r.log.failAt = 1
	step := r.run(&actions.ExecArgs{Command: "uname -r", TimeoutS: 60}, "")
	if step.Stop != StopAudit || step.Err == nil || !strings.Contains(step.Err.Error(), "the session stops") {
		t.Fatalf("step %+v", step)
	}
	if r.agent.count() != 0 {
		t.Error("the command ran although its decision could not be written")
	}
	// A refusal that cannot be recorded is also the end.
	r = newDgrid(t)
	r.log.failAt = 1
	if step := r.run(&actions.ExecArgs{Command: "rm -rf /", TimeoutS: 60}, ""); step.Stop != StopAudit || step.Err == nil {
		t.Errorf("an unrecorded refusal: %+v", step)
	}
	// If the result cannot be written the action did run, and the session must end.
	r = newDgrid(t)
	r.log.failAt = 2
	step = r.run(&actions.ExecArgs{Command: "uname -r", TimeoutS: 60}, "")
	if step.Stop != StopAudit || step.Err == nil || r.agent.count() != 1 {
		t.Errorf("an unrecorded result: %+v, %d calls", step, r.agent.count())
	}
}

func TestWhatIsTypedIntoAPasswordFieldNeverReachesTheViewerOrTheLog(t *testing.T) {
	r := newDgrid(t)
	r.asker.answer, r.asker.approve = true, true
	// The model sets the text of the focused password field.
	step := r.run(&actions.SetTextArgs{ID: "a3", Text: "hunter2-secret"}, "log in")
	if !step.Result.OK {
		t.Fatalf("step %+v", step)
	}
	for _, rec := range r.log.all() {
		if strings.Contains(string(rec.Args), "hunter2-secret") || strings.Contains(string(rec.Result), "hunter2-secret") {
			t.Errorf("the password is in the log: %s", rec.Args)
		}
	}
	for _, tr := range r.view.traces() {
		if strings.Contains(string(tr.Args), "hunter2-secret") {
			t.Errorf("the password is in the viewer: %s", tr.Args)
		}
	}
	for _, q := range r.asker.requests() {
		if strings.Contains(string(q.Args.(json.RawMessage)), "hunter2-secret") {
			t.Errorf("the password is in the question: %s", q.Args)
		}
	}
	// And the guest did receive it.
	if req := r.agent.calls[0].req.(protocol.A11yActionReq); req.Text != "hunter2-secret" {
		t.Errorf("the guest got %+v", req)
	}
	if !strings.Contains(string(rec(t, r.log, 0).Args), "len 14") {
		t.Errorf("the text was removed, not masked: %s", rec(t, r.log, 0).Args)
	}
}

func TestSecretsTheSessionKnowsAreRemovedFromWhatIsShown(t *testing.T) {
	r := newDgrid(t)
	r.d.Redactor.AddSecret("sk-live-1234567890")
	step := r.run(&actions.ExecArgs{Command: "echo sk-live-1234567890", TimeoutS: 60}, "")
	if !step.Result.OK {
		t.Fatal(step.Result)
	}
	for _, rc := range r.log.all() {
		if strings.Contains(string(rc.Args), "sk-live-1234567890") {
			t.Errorf("the secret is in the log: %s", rc.Args)
		}
	}
	for _, tr := range r.view.traces() {
		if strings.Contains(string(tr.Args), "sk-live-1234567890") {
			t.Errorf("the secret is in the viewer: %s", tr.Args)
		}
	}
	if got := r.agent.calls[0].req.(protocol.ExecReq).Command; got != "echo sk-live-1234567890" {
		t.Errorf("the command the guest ran was changed: %q", got)
	}
}

func TestAnActionThatFailsInTheGuestEndsTheBatchAndSaysSo(t *testing.T) {
	r := newDgrid(t)
	r.agent.on(protocol.TypeInput, func(any, any) error { return remote(protocol.CodeInternal, "no display") })
	step := r.run(&actions.KeyArgs{Keys: "a"}, "")
	if step.Result.OK || step.Stop != StopBatch || step.Result.Code != actions.CodeInternal {
		t.Fatalf("step %+v", step)
	}
	all := r.log.all()
	if len(all) != 2 || !strings.Contains(string(all[1].Result), `"ok":false`) || !strings.Contains(string(all[1].Result), "no display") {
		t.Errorf("records %+v", all)
	}
	if tr := r.view.traces(); tr[len(tr)-1].Status != "failed" || tr[len(tr)-1].Summary != "no display" {
		t.Errorf("traces %+v", tr)
	}
	if a, _, _ := r.d.Limits.Remaining(); a != safety.ProfileFor(config.Defaults()).ActionsPerTask-1 {
		t.Error("a failed action still cost the budget: it reached the guest")
	}
}

func TestAContextThatEndsDuringTheActionIsAStop(t *testing.T) {
	r := newDgrid(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.agent.on(protocol.TypeInput, func(any, any) error { cancel(); return context.Canceled })
	raw, _ := json.Marshal(&actions.KeyArgs{Keys: "a"})
	step := r.d.Run(ctx, Input{Turn: 1, ID: "t1a1", Action: actions.Action{Tool: "key", Args: &actions.KeyArgs{Keys: "a"}, Raw: raw}, Mapper: guestMapper(t)})
	if step.Stop != StopCanceled || step.Result.OK {
		t.Errorf("step %+v", step)
	}
	// And one that has ended before the action begins does nothing.
	n, recs, traces := r.agent.count(), len(r.log.all()), len(r.view.traces())
	step = r.d.Run(ctx, Input{Turn: 1, ID: "t1a2", Action: actions.Action{Tool: "key", Args: &actions.KeyArgs{Keys: "a"}, Raw: raw}, Mapper: guestMapper(t)})
	if step.Stop != StopCanceled || r.agent.count() != n {
		t.Errorf("an action begun after Stop: %+v", step)
	}
	if len(r.log.all()) != recs || len(r.view.traces()) != traces {
		t.Error("an action that was never begun left records")
	}
}

func TestADispatcherNeedsNeitherAViewerNorALog(t *testing.T) {
	r := newDgrid(t)
	r.d.Viewer, r.d.Audit = nil, nil
	if step := r.run(&actions.ExecArgs{Command: "uname -r", TimeoutS: 60}, ""); !step.Result.OK {
		t.Errorf("step %+v", step)
	}
}

func TestPlansShowWhereAnActionWillAct(t *testing.T) {
	r := newDgrid(t)
	r.run(&actions.ClickElementArgs{ID: "a1"}, "")
	p := r.view.plans()
	if len(p) != 1 || p[0].Bounds == nil || *p[0].Bounds != (viewer.Bounds{X: 100, Y: 200, W: 80, H: 24}) {
		t.Errorf("click_element plan %+v", p)
	}
	r.run(&actions.ScrollArgs{X: 640, Y: 360, DY: -3}, "")
	if p := r.view.plans(); len(p) != 2 || p[1].X == nil || *p[1].X != 960 || *p[1].Y != 540 {
		t.Errorf("scroll plan %+v", p)
	}
	r.run(&actions.KeyArgs{Keys: "a"}, "")
	if p := r.view.plans(); len(p) != 3 || p[2].X != nil || p[2].Bounds != nil {
		t.Errorf("key plan %+v", p)
	}
}
