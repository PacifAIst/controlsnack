// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// Looker reads the screen each turn (*vision.Observer).
type Looker interface {
	Look(ctx context.Context) (vision.Seen, error)
}

// Actor carries out one action under the rules (*Dispatcher).
type Actor interface {
	Run(ctx context.Context, in Input) Step
}

// Person is the one watching the session.
type Person interface {
	// Messages are what they wrote since the last call, oldest first.
	Messages() []string
	// Ask puts the model's question to them and waits for the answer. It returns the context's error if the wait is ended.
	Ask(ctx context.Context, question string) (string, error)
}

// Guard watches for a model that is not getting anywhere: the same action again with the screen unchanged, too many failures
// in a row. A non-empty answer is a reason to pause the task and ask the person.
type Guard interface {
	// Action is told what each action came to, and a fingerprint of the screen the model saw when it chose it (the picture's
	// hash, or in text-only mode a hash of the tree and the text), so that an action repeated on a screen that did not change can
	// be told from one repeated on a page that moves on.
	Action(a actions.Action, s Step, screen string) (pause string)
	// Answer is told whether each answer of the model held an action that could be run.
	Answer(usable bool) (pause string)
	// Reset forgets what it has seen: the person resumed.
	Reset()
}

// End says how a task ended.
type End string

// The ways a task ends.
const (
	// EndDone: the model said done (Outcome.Success says whether it succeeded).
	EndDone End = "done"
	// EndStopped: the person pressed Stop, or the context ended.
	EndStopped End = "stopped"
	// EndKilled: the kill switch fired.
	EndKilled End = "killed"
	// EndTimeout: the session's time is up.
	EndTimeout End = "timeout"
	// EndAudit: the audit log can no longer be written.
	EndAudit End = "audit_failure"
	// EndFailed: something that will not mend by itself (the model refuses the key, the conversation cannot fit its context).
	EndFailed End = "failed"
	// EndSession: the session went away under the task (it is ending).
	EndSession End = "session_over"
)

// Outcome is how a task came out.
type Outcome struct {
	End     End
	Success bool
	// Summary is what the model said when it finished.
	Summary string
	// Turns is how many turns the task took.
	Turns int
	Err   error
}

// Limits on what the model's words become for the viewer and the next turn.
const (
	maxQuestionChat = 1000
	maxAnswerDetail = 300
	maxSummaryText  = 2000
)

// Loop is the turn loop (PLAN.md section 11): for each turn it waits while the session is paused, spends a turn of the budget,
// looks at the screen, asks the model, reads its answer, carries out the actions one by one and tells the model how they went.
type Loop struct {
	Machine *Machine
	Limits  *limits.Limits
	Look    Looker
	Conv    *prompt.Conversation
	Model   backend.Backend
	// Set is the tools the model may name.
	Set *actions.Set
	Act Actor
	// View tells the executor what the model saw, so that the ids it names mean the same to both.
	View   func(vision.RawSeen)
	Viewer Sink
	Audit  Recorder
	Person Person
	Guard  Guard
	Killed func() bool
	// Redactor removes what the session knows to be secret from what the loop writes down and shows.
	Redactor *secret.Redactor
	// Network is what the observation says of the network: "disabled" or "enabled".
	Network string
	// MaxActions is how many actions one answer may hold; Repairs how many times a turn asks the model to answer again
	// (parse.MaxRepairs when 0).
	MaxActions int
	Repairs    int
	// Answered is told how long each call to the model took.
	Answered func(time.Duration)
	// Kept is given the picture the model is shown on each turn that has a new one (for --keep).
	Kept func(turn int, img backend.Image)
	// KeptRequest is given what the model is asked on the first attempt of each turn (for --keep).
	KeptRequest func(turn int, req backend.Request)
	Now         func() time.Time

	onResume []func()
	chatSeq  int
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loop) send(m viewer.Message) {
	if l.Viewer != nil {
		l.Viewer.Send(m)
	}
}

func (l *Loop) killed() bool { return l.Killed != nil && l.Killed() }

// record writes an audit record; an error means the log can no longer be written.
func (l *Loop) record(r audit.Record) error {
	if l.Audit == nil {
		return nil
	}
	if _, err := l.Audit.Append(r); err != nil {
		return fmt.Errorf("the audit log can no longer be written, so the session stops: %w", err)
	}
	return nil
}

// say shows what the model said in the conversation panel of the viewer (the text is already cleaned and cut).
func (l *Loop) say(role, text string) {
	if text == "" {
		return
	}
	l.chatSeq++
	l.send(viewer.Chat{ID: "l" + strconv.Itoa(l.chatSeq), Role: role, Text: text, TS: l.now().UTC().Format(time.RFC3339)})
}

func (l *Loop) notice(level, text string) { l.send(viewer.Notice{Level: level, Text: text}) }

// pause stops the task where it is and tells the person why; the task goes on when they resume, and then first runs the
// functions given, which forget what the pause was about.
func (l *Loop) pause(reason, text string, onResume ...func()) {
	l.Machine.ToIf([]State{Running}, Paused, reason)
	l.notice(viewer.LevelWarn, text)
	l.onResume = append(l.onResume, onResume...)
}

// Run carries out one task: it goes round until the model is done, the person stops it, or the session ends. The session
// must be Ready; when the task ends in any way that leaves the session going, it is Ready again.
func (l *Loop) Run(ctx context.Context, task string) Outcome {
	if err := l.Machine.To(Running, "task started"); err != nil {
		return Outcome{End: EndFailed, Err: err}
	}
	l.Limits.StartTask()
	if l.Guard != nil {
		l.Guard.Reset()
	}
	out := l.run(ctx, task)
	switch out.End {
	case EndDone, EndStopped, EndFailed:
		l.Machine.ToIf([]State{Running, AwaitingConfirm, AwaitingUser, Paused}, Ready, "task "+string(out.End))
	}
	return out
}

func (l *Loop) run(ctx context.Context, task string) Outcome {
	turns := 0
	for {
		if out, over := l.gate(ctx); over {
			out.Turns = turns
			return out
		}
		for _, f := range l.onResume {
			f()
		}
		l.onResume = nil
		if err := l.Limits.BeginTurn(); err != nil {
			var be *limits.BudgetError
			if errors.As(err, &be) {
				l.checkIn("all the turns it was allowed")
				continue
			}
			return Outcome{End: EndTimeout, Turns: turns, Err: err}
		}
		turns++
		if out, over := l.turn(ctx, task); over {
			out.Turns = turns
			return out
		}
	}
}

// gate waits while the task is paused or waiting for the person, and says whether the task is over instead.
func (l *Loop) gate(ctx context.Context) (Outcome, bool) {
	if l.killed() {
		return Outcome{End: EndKilled}, true
	}
	st, _ := l.Machine.WaitRunning(ctx) // a wait the context ended returns the state it found, which is not Running
	if st != Running || ctx.Err() != nil {
		return l.over(st), true
	}
	return Outcome{}, false
}

// over is how a task ends when the gate did not let it go on, by the state the session was found in: killed, over, or (Ready,
// or the context ended) stopped.
func (l *Loop) over(st State) Outcome {
	switch {
	case l.killed() || st == Killed:
		return Outcome{End: EndKilled}
	case st.Final():
		return Outcome{End: EndSession}
	}
	return Outcome{End: EndStopped}
}

// interrupted is the end of a task whose context ended: the kill switch, or Stop.
func (l *Loop) interrupted() Outcome {
	if l.killed() {
		return Outcome{End: EndKilled}
	}
	return Outcome{End: EndStopped}
}

// checkIn pauses a task that has used its budget: the person decides whether it gets another.
func (l *Loop) checkIn(what string) {
	l.pause("check-in: "+what, "The task has used "+what+". Resume to give it another allowance, or Stop to end it.", l.Limits.CheckIn, l.resetGuard)
}

func (l *Loop) resetGuard() {
	if l.Guard != nil {
		l.Guard.Reset()
	}
}

// turn is one turn: look, ask, carry out. It says whether the task is over.
func (l *Loop) turn(ctx context.Context, task string) (Outcome, bool) {
	seen, err := l.Look.Look(ctx)
	if err != nil {
		return l.looking(ctx, err)
	}
	if l.View != nil {
		l.View(seen.Raw)
	}
	n := l.Conv.Turn() + 1
	obs := prompt.Observation{Turn: n, Network: l.Network, Task: task}
	seen.Fill(&obs)
	a, t, s := l.Limits.Remaining()
	obs.Budget = prompt.Budget{ActionsLeft: a, TurnsLeft: t, TimeLeft: s}
	var img *backend.Image
	hash, shotSHA := "", ""
	if seen.Shot != nil {
		img, hash = &seen.Shot.Image, seen.Shot.Hash
		sum := sha256.Sum256(seen.Shot.Image.Data)
		shotSHA = hex.EncodeToString(sum[:])
	}
	if l.Person != nil {
		for _, m := range l.Person.Messages() {
			l.Conv.UserMessage(m)
		}
	}
	l.Conv.Begin(obs, img)
	if l.Kept != nil && img != nil && !seen.Shot.Reused {
		l.Kept(n, *img)
	}

	res, calls, out, over := l.answer(ctx, n)
	if over {
		return out, true
	}
	if !res.Usable() { // paused: the task goes on from the top when the person resumes
		return Outcome{}, false
	}
	return l.carryOut(ctx, n, res, calls, seen.Mapper, hash, shotSHA, screenKey(seen, hash))
}

// looking is what a turn does when the screen could not be read: stop if the task is stopped, otherwise pause and say what
// was wrong, so that the person can resume once it has mended.
func (l *Loop) looking(ctx context.Context, err error) (Outcome, bool) {
	if ctx.Err() != nil {
		return l.interrupted(), true
	}
	l.pause("the screen could not be read", "The screen could not be read: "+textclean.Line(err.Error(), 300)+". Resume to try again, or Stop to end the task.")
	return Outcome{}, false
}

// answer asks the model until it gives an answer with an action in it; after Repairs more tries it pauses the task instead.
// The result has no actions when the task was paused.
func (l *Loop) answer(ctx context.Context, n int) (parse.Result, []parse.ToolCall, Outcome, bool) {
	max := l.MaxActions
	if max < 1 {
		max = actions.DefaultMaxActions
	}
	repairs := l.Repairs
	if repairs < 1 {
		repairs = parse.MaxRepairs
	}
	for attempt := 0; ; attempt++ {
		req, _, err := l.Conv.Request(max)
		if err != nil {
			l.notice(viewer.LevelError, textclean.Line(err.Error(), 600))
			return parse.Result{}, nil, Outcome{End: EndFailed, Err: err}, true
		}
		if attempt == 0 && l.KeptRequest != nil {
			l.KeptRequest(n, req)
		}
		began := l.now()
		resp, err := l.Model.Complete(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return parse.Result{}, nil, l.interrupted(), true
			}
			out := l.modelFailed(err)
			return parse.Result{}, nil, out, out.End != ""
		}
		if l.Answered != nil {
			l.Answered(l.now().Sub(began))
		}
		if aerr := l.auditAnswer(n, resp, l.now().Sub(began)); aerr != nil {
			return parse.Result{}, nil, Outcome{End: EndAudit, Err: aerr}, true
		}
		if err := l.Conv.Assistant(resp.Reply.Text, resp.Reply.ToolCalls); err != nil {
			return parse.Result{}, nil, Outcome{End: EndFailed, Err: err}, true
		}
		res := parse.Parse(l.Set, resp.Reply, max)
		l.say("assistant", res.Thought)
		why := ""
		if l.Guard != nil {
			why = l.Guard.Answer(res.Usable())
		}
		if res.Usable() {
			return res, resp.Reply.ToolCalls, Outcome{}, false
		}
		// Nothing to run: a native call must still be answered, and then the model is asked to answer again.
		if len(resp.Reply.ToolCalls) > 0 {
			if err := l.Conv.Results(l.unrunnable(resp.Reply.ToolCalls, res), prompt.Limits{}); err != nil {
				return parse.Result{}, nil, Outcome{End: EndFailed, Err: err}, true
			}
		}
		if why != "" || attempt >= repairs {
			if why == "" {
				why = "The model's answers could not be used " + strconv.Itoa(attempt+1) + " times in a row."
			}
			if fb := parse.Feedback(res, max); fb != "" {
				l.Conv.Note("Your last answer could not be used.\n" + fb)
			}
			l.pause("the model is not giving usable answers", why+" Resume to let it try again, or Stop to end the task.", l.resetGuard)
			return parse.Result{}, nil, Outcome{}, false
		}
		msg := parse.RepairMessage(res, l.Set, max, req.Mode == backend.ModeNative)
		if resp.Truncated {
			msg += "\nYour last answer was cut off because it reached the length limit: answer more briefly."
		}
		if err := l.Conv.Notice(msg); err != nil {
			return parse.Result{}, nil, Outcome{End: EndFailed, Err: err}, true
		}
	}
}

// unrunnable answers the calls of an answer none of which could be run, each with why.
func (l *Loop) unrunnable(calls []parse.ToolCall, res parse.Result) []prompt.Result {
	outs := parse.Outcomes(calls, res)
	results := make([]prompt.Result, len(outs))
	for i, o := range outs {
		results[i] = notRunResult(o)
	}
	return results
}

// notRunResult is the result of a call that was not valid, was not run after one that was not valid, or was dropped.
func notRunResult(o parse.Outcome) prompt.Result {
	r := prompt.Result{CallID: o.Call.ID, Tool: o.Call.Name}
	switch o.Disposition {
	case parse.DispInvalid:
		r.Code = actions.CodeInvalidArgs
		r.Detail = "not valid"
		if o.Failure != nil && o.Failure.Err != nil {
			r.Code = o.Failure.Err.Code
			r.Detail = o.Failure.Err.Error()
		}
	case parse.DispDropped:
		r.Code, r.Detail = prompt.CodeDropped, "dropped: more actions than one answer may hold"
	default:
		r.Code, r.Detail = prompt.CodeNotRun, "not run: an earlier action of the answer was not valid"
	}
	return r
}

// auditAnswer writes what is worth keeping of a model's answer: who answered, how much it cost and how long it took, and a
// hash of the answer, never the answer itself (it can hold what was on the screen).
func (l *Loop) auditAnswer(n int, resp backend.Response, took time.Duration) error {
	sum := sha256.Sum256([]byte(resp.Reply.Text))
	info, _ := json.Marshal(map[string]any{
		"model": textclean.Line(resp.Model, 120), "prompt_tokens": resp.Usage.PromptTokens, "completion_tokens": resp.Usage.CompletionTokens,
		"latency_ms": took.Milliseconds(), "response_sha256": hex.EncodeToString(sum[:]), "tool_calls": len(resp.Reply.ToolCalls), "truncated": resp.Truncated,
	})
	return l.record(audit.Record{Kind: audit.KindLLM, Event: "response", Actor: "llm", Turn: n, LLM: info})
}

// modelAdvice says, for each way a call to the model can fail, what the person can do about it.
func modelAdvice(k backend.Kind) string {
	switch k {
	case backend.KindUnavailable:
		return "Is the model's server running, and is the address right?"
	case backend.KindTimeout:
		return "The model took too long: a bigger model or a slower machine may need a longer llm.timeout."
	case backend.KindRateLimited:
		return "The provider is limiting requests: wait a little."
	case backend.KindServer:
		return "The model's server reported an error of its own."
	case backend.KindProtocol:
		return "The model's server answered in a way ControlSnack does not understand."
	case backend.KindAuth:
		return "The provider refused the key: check the environment variable it is read from."
	case backend.KindBadRequest:
		return "The model refused the request, for example because the conversation is longer than its context window or it takes no pictures."
	case backend.KindNotFound:
		return "The model or the address was not found: check the model's name and the base URL."
	}
	return ""
}

// modelFailed decides what a failed call to the model comes to: a failure that will not mend by itself ends the task, any
// other pauses it so that the person can resume it once the model is back. (The client has already tried again for the failures
// worth it, with a pause between tries.)
func (l *Loop) modelFailed(err error) Outcome {
	advice := ""
	var be *backend.Error
	if errors.As(err, &be) {
		advice = modelAdvice(be.Kind)
		switch be.Kind {
		case backend.KindAuth, backend.KindBadRequest, backend.KindNotFound:
			l.notice(viewer.LevelError, words("The model cannot be used: "+textclean.Line(err.Error(), 400)+".", advice))
			return Outcome{End: EndFailed, Err: err}
		}
	}
	l.pause("the model did not answer", words("The model did not answer: "+textclean.Line(err.Error(), 400)+".", advice, "Resume to try again, or Stop to end the task."))
	return Outcome{}
}

// screenKey fingerprints what the model saw: the hash of the picture, or, with no picture, a hash of the tree and the text.
func screenKey(seen vision.Seen, hash string) string {
	if hash != "" {
		return hash
	}
	b, err := json.Marshal(struct {
		Tree *protocol.A11yTreeRes
		OCR  *protocol.OCRRes
	}{seen.Raw.Tree, seen.Raw.OCR})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// words joins sentences with a space, leaving out the empty ones.
func words(sentences ...string) string {
	var parts []string
	for _, s := range sentences {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}
