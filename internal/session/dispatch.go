// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/confirm"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/safety/policy"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// Recorder is the audit log (*audit.Log).
type Recorder interface {
	Append(audit.Record) (audit.Record, error)
}

// Sink is where the viewer's messages go (*viewer.Hub). It never blocks.
type Sink interface {
	Send(viewer.Message)
}

// Stop says why the actions of one response end early.
type Stop int

// The reasons.
const (
	StopNone     Stop = iota
	StopBatch         // the action was refused, or failed: the rest of the response is not run
	StopKilled        // the kill switch fired
	StopCanceled      // Stop was pressed, or the context ended
	StopBudget        // the task's budget is used up: the loop pauses for a check-in
	StopTimeout       // the session's time is up
	StopAudit         // the audit log could not be written: the session must end
)

// Step is what one action came to.
type Step struct {
	Result prompt.Result
	// ScreenHash is the hash of the screen after an input, when the agent said.
	ScreenHash string
	Stop       Stop
	// Err is set with StopAudit.
	Err error
}

// Input is one action to carry out.
type Input struct {
	Turn int
	// ID names the action in the audit log and the viewer ("t3a1").
	ID string
	// CallID is the id of the native call the result answers.
	CallID string
	// Action is what the model said, in the coordinates it works in.
	Action actions.Action
	// Mapper turns the model's coordinates into the guest's.
	Mapper vision.Mapper
	// ScreenSHA is the hash of the picture the model acted on, for the audit log.
	ScreenSHA string
}

// Dispatcher carries out the actions of a model's response, one by one, under the rules. For each action, in this order: it
// has not been killed; the coordinates are the guest's; the rules judge it (the policy on guest coordinates); the profile turns
// the judgement into what is enforced; a refusal ends there; a confirmation waits for the person; the budgets and the rate
// admit it; its decision is written to the audit log; the viewer is shown the plan; the executor does it; its result is
// written and shown. An action that is refused or not approved is never executed and never charged to the budget.
type Dispatcher struct {
	Exec    *Executor
	Policy  *policy.Engine
	Profile safety.Profile
	Broker  *confirm.Broker
	Limits  *limits.Limits
	Audit   Recorder
	Viewer  Sink
	Machine *Machine
	Killed  func() bool
	// Redactor masks what must not reach the viewer or the log (secrets the session knows).
	Redactor *secret.Redactor
	Now      func() time.Time
}

var reWID = regexp.MustCompile(`^w[0-9]{1,5}$`)

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Dispatcher) send(m viewer.Message) {
	if d.Viewer != nil {
		d.Viewer.Send(m)
	}
}

// judged is the verdict and what it rested on.
type judged struct {
	verdict    safety.Verdict // as enforced, after the profile
	rules      []policy.RuleHit
	redactText bool
}

// judge asks the rules about an action in the guest's coordinates.
func (d *Dispatcher) judge(ctx context.Context, a actions.Action) judged {
	switch args := a.Args.(type) {
	case *actions.ExecArgs:
		dec := d.Policy.EvaluateExec(ctx, args.Command, args.Cwd)
		return judged{verdict: d.Profile.Apply(dec.Verdict), rules: dec.Rules}
	}
	g, ok := guiAction(a)
	if !ok {
		return judged{verdict: d.Profile.Apply(safety.Allow)} // reading and waiting have no rules; --step still asks
	}
	dec := d.Policy.EvaluateGUI(g, d.Exec.Snapshot())
	return judged{verdict: d.Profile.Apply(dec.Verdict), rules: dec.Rules, redactText: dec.RedactText}
}

// guiAction is the part of an action the GUI rules look at, or false for a tool that has no GUI rules.
func guiAction(a actions.Action) (policy.GUIAction, bool) {
	switch args := a.Args.(type) {
	case *actions.ClickElementArgs:
		return policy.GUIAction{Tool: a.Tool, ID: args.ID}, true
	case *actions.SetTextArgs:
		return policy.GUIAction{Tool: a.Tool, ID: args.ID, Text: args.Text}, true
	case *actions.MouseArgs:
		g := policy.GUIAction{Tool: a.Tool, Op: args.Op, X: args.X, Y: args.Y}
		if args.ToX != nil {
			g.ToX = *args.ToX
		}
		if args.ToY != nil {
			g.ToY = *args.ToY
		}
		return g, true
	case *actions.KeyArgs:
		return policy.GUIAction{Tool: a.Tool, Keys: args.Keys}, true
	case *actions.TypeTextArgs:
		return policy.GUIAction{Tool: a.Tool, Text: args.Text}, true
	case *actions.BrowserArgs:
		g := policy.GUIAction{Tool: a.Tool, Op: args.Op, Text: args.Text, URL: args.URL}
		if reWID.MatchString(args.Target) {
			g.ID = args.Target
		} else {
			g.Target = args.Target
		}
		return g, true
	}
	return policy.GUIAction{}, false
}

// maskArgs is the arguments as the viewer and the log show them: the text typed into a password field is masked, and what
// the redactor knows to be secret is removed.
func (d *Dispatcher) maskArgs(raw json.RawMessage, hideText bool) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	if hideText {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			if s, ok := m["text"].(string); ok {
				m["text"] = secret.MaskPassword(s)
			}
			if b, err := json.Marshal(m); err == nil {
				raw = b
			}
		}
	}
	if d.Redactor != nil {
		raw = d.Redactor.RedactBytes(raw)
	}
	return raw
}

// ruleInfos are the rules as the person is shown them.
func ruleInfos(hits []policy.RuleHit) []confirm.RuleInfo {
	seen := map[string]bool{}
	var out []confirm.RuleInfo
	for _, h := range hits {
		if !seen[h.ID] {
			seen[h.ID] = true
			out = append(out, confirm.RuleInfo{ID: h.ID, Explanation: h.Explanation})
		}
	}
	return out
}

func ruleIDs(hits []policy.RuleHit) []string { return policy.Decision{Rules: hits}.RuleIDs() }

// record writes an audit record, and says whether the log can no longer be written.
func (d *Dispatcher) record(r audit.Record) error {
	if d.Audit == nil {
		return nil
	}
	if _, err := d.Audit.Append(r); err != nil {
		return fmt.Errorf("the audit log can no longer be written, so the session stops: %w", err)
	}
	return nil
}

// refusal words, for the model.
func refusedWords(hits []policy.RuleHit) string {
	var parts []string
	seen := map[string]bool{}
	for _, h := range hits {
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		parts = append(parts, fmt.Sprintf("%s (%s)", h.ID, h.Explanation))
	}
	if len(parts) == 0 {
		return "Refused by the safety rules"
	}
	return "Refused by the safety rules: " + strings.Join(parts, "; ")
}

// Run carries out one action under the rules.
func (d *Dispatcher) Run(ctx context.Context, in Input) Step {
	tool := in.Action.Tool
	res := prompt.Result{CallID: in.CallID, Tool: tool, Action: prompt.Describe(in.Action)}
	done := func(r prompt.Result, stop Stop) Step {
		r.CallID, r.Tool, r.Action = res.CallID, tool, res.Action
		if !r.OK {
			// What to do instead is the same sentence wherever the failure came from.
			r.Detail = strings.TrimRight(r.Detail, " .") + prompt.Hint(r.Code)
		}
		return Step{Result: r, Stop: stop}
	}

	if d.Killed != nil && d.Killed() {
		return done(prompt.Result{Code: actions.CodeInternal, Detail: "The session is being killed."}, StopKilled)
	}
	if ctx.Err() != nil {
		return done(prompt.Result{Code: actions.CodeInternal, Detail: "The action was stopped."}, StopCanceled)
	}

	// The model's numbers are the picture's; everything from here on is the guest's.
	ga, err := in.Mapper.Guest(in.Action)
	if err != nil {
		var ae *actions.Error
		detail := guestWords(err.Error())
		if errors.As(err, &ae) {
			detail = guestWords(ae.Error())
		}
		step := done(prompt.Result{Code: actions.CodeInvalidArgs, Detail: detail}, StopBatch)
		if aerr := d.record(audit.Record{Kind: audit.KindAction, Event: "decision", Actor: "llm", Turn: in.Turn, ActionID: in.ID, Tool: tool, Args: d.maskArgs(in.Action.Raw, false), Reason: in.Action.Reason, Verdict: "invalid", ScreenshotSHA256: in.ScreenSHA}); aerr != nil {
			step.Stop, step.Err = StopAudit, aerr
		}
		return step
	}

	j := d.judge(ctx, ga)
	args := d.maskArgs(in.Action.Raw, j.redactText)
	rec := audit.Record{Kind: audit.KindAction, Event: "decision", Actor: "llm", Turn: in.Turn, ActionID: in.ID, Tool: tool, Args: args, Reason: in.Action.Reason,
		Rules: ruleIDs(j.rules), ScreenshotSHA256: in.ScreenSHA}
	trace := viewer.Trace{ActionID: in.ID, Turn: in.Turn, Tool: tool, Args: args, Reason: in.Action.Reason, Verdict: j.verdict.String(), Rules: ruleIDs(j.rules)}
	finish := func(verdict string, r prompt.Result, stop Stop, status string, ms int64) Step {
		rec.Verdict = verdict
		step := done(r, stop)
		if aerr := d.record(rec); aerr != nil {
			step.Stop, step.Err = StopAudit, aerr
		}
		trace.Status, trace.DurationMS, trace.Summary = status, ms, r.Detail
		d.send(trace)
		return step
	}

	if j.verdict == safety.Deny {
		return finish("denied", prompt.Result{Code: actions.CodePolicyDenied, Detail: refusedWords(j.rules)}, StopBatch, "denied", 0)
	}

	verdictWord := "allowed"
	note := ""
	if j.verdict == safety.Confirm {
		verdictWord = "confirmed"
		d.Machine.ToIf([]State{Running}, AwaitingConfirm, "needs your approval: "+tool)
		cres := d.Broker.Ask(ctx, confirm.Request{ActionID: in.ID, Tool: tool, Args: json.RawMessage(args), Rules: ruleInfos(j.rules), Reason: in.Action.Reason})
		d.Machine.ToIf([]State{AwaitingConfirm}, Running, "answered")
		note = cres.Note
		if !cres.Approved() {
			code, detail, stop := actions.CodeUserRejected, "The user rejected this action", StopBatch
			word, status := "rejected", "rejected"
			switch cres.Outcome {
			case confirm.Timeout:
				code, detail, word, status = actions.CodeConfirmTimeout, "Nobody answered in time, so the action was not approved", "confirm_timeout", "expired"
			case confirm.NoViewer:
				detail, word, status = "Nobody is watching, so the action could not be approved", "no_viewer", "rejected"
			case confirm.Failed:
				detail, word, status = "The request could not be shown to the user, so the action was not approved", "confirm_failed", "rejected"
			case confirm.Canceled:
				code, detail, stop, word, status = actions.CodeInternal, "The request was canceled", StopCanceled, "canceled", "canceled"
			}
			if cres.Note != "" {
				detail += ". The user wrote: " + guestWords(cres.Note)
			}
			return finish(word, prompt.Result{Code: code, Detail: detail}, stop, status, 0)
		}
	}

	if d.Killed != nil && d.Killed() {
		return finish("killed", prompt.Result{Code: actions.CodeInternal, Detail: "The session is being killed."}, StopKilled, "canceled", 0)
	}
	if d.Limits != nil {
		if err := d.Limits.Admit(ctx); err != nil {
			var be *limits.BudgetError
			switch {
			case errors.Is(err, limits.ErrRateLimited):
				return finish("rate_limited", prompt.Result{Code: actions.CodeRateLimited, Detail: "Too many actions too quickly"}, StopBatch, "failed", 0)
			case errors.Is(err, limits.ErrSessionTimeout):
				return finish("session_timeout", prompt.Result{Code: actions.CodeBudgetExhausted, Detail: "The session's time is up"}, StopTimeout, "failed", 0)
			case errors.As(err, &be):
				// The allowance of one response is spent by that response alone: the next one has its own. The task's is not.
				stop := StopBudget
				if be.Kind == "response" {
					stop = StopBatch
				}
				return finish("budget", prompt.Result{Code: actions.CodeBudgetExhausted, Detail: err.Error()}, stop, "failed", 0)
			default: // the wait was canceled
				return finish("canceled", prompt.Result{Code: actions.CodeInternal, Detail: "The action was stopped."}, StopCanceled, "canceled", 0)
			}
		}
	}

	// The decision is on record before the action is done: an action that was started and never finished still shows.
	rec.Verdict = verdictWord
	if aerr := d.record(rec); aerr != nil {
		return Step{Result: res, Stop: StopAudit, Err: aerr}
	}
	d.send(d.planOf(in.ID, ga))
	trace.Status = "running"
	d.send(trace)

	began := d.now()
	out := d.Exec.Do(ctx, ga)
	ms := d.now().Sub(began).Milliseconds()
	r := out.Result
	if note != "" {
		r.Detail += ". The user approved it and wrote: " + guestWords(note)
	}
	status := "done"
	if !r.OK {
		status = "failed"
	}
	step := done(r, StopNone)
	step.ScreenHash = out.ScreenHash
	if !r.OK {
		step.Stop = StopBatch
	}
	if ctx.Err() != nil && !r.OK {
		step.Stop = StopCanceled
	}
	result, _ := json.Marshal(map[string]any{"ok": r.OK, "code": r.Code, "detail": textclean.Line(r.Detail, 400), "duration_ms": ms})
	if aerr := d.record(audit.Record{Kind: audit.KindAction, Event: "result", Actor: "llm", Turn: in.Turn, ActionID: in.ID, Tool: tool, Verdict: verdictWord, Result: result}); aerr != nil {
		step.Stop, step.Err = StopAudit, aerr
	}
	trace.Status, trace.DurationMS, trace.Summary = status, ms, textclean.Line(r.Detail, 400)
	d.send(trace)
	return step
}

// planOf is what the viewer is told is about to happen, with where on the screen when the action has a place.
func (d *Dispatcher) planOf(id string, a actions.Action) viewer.Plan {
	p := viewer.Plan{ActionID: id, Tool: a.Tool}
	pt := func(x, y int) { p.X, p.Y = &x, &y }
	switch args := a.Args.(type) {
	case *actions.MouseArgs:
		pt(args.X, args.Y)
	case *actions.ScrollArgs:
		pt(args.X, args.Y)
	case *actions.ClickElementArgs:
		if snap := d.Exec.Snapshot(); snap != nil {
			if n, ok := snap.Lookup(args.ID); ok {
				p.Bounds = &viewer.Bounds{X: n.Bounds[0], Y: n.Bounds[1], W: n.Bounds[2], H: n.Bounds[3]}
			}
		}
	case *actions.SetTextArgs:
		if snap := d.Exec.Snapshot(); snap != nil {
			if n, ok := snap.Lookup(args.ID); ok {
				p.Bounds = &viewer.Bounds{X: n.Bounds[0], Y: n.Bounds[1], W: n.Bounds[2], H: n.Bounds[3]}
			}
		}
	}
	return p
}
