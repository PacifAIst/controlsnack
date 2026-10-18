// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// usesCoordinates reports whether an action says where on the screen it acts: it was planned for the screen as the model saw
// it, and means something else on a screen that has changed.
func usesCoordinates(a actions.Action) bool {
	switch a.Args.(type) {
	case *actions.MouseArgs, *actions.ScrollArgs:
		return true
	}
	return false
}

// notRun is the result of an action that was valid and was not carried out, with the reason.
func notRun(it parse.Item, why string) prompt.Result {
	return prompt.Result{CallID: it.CallID, Tool: it.Action.Tool, Action: prompt.Describe(it.Action), Code: prompt.CodeNotRun,
		Detail: "not run: " + strings.TrimRight(why, " .") + prompt.Hint(prompt.CodeNotRun)}
}

// carryOut carries out the actions of an answer, one by one (PLAN.md section 11), and tells the model how each went. The
// answer ends early, and the actions after it are not run, when an action is refused or fails, is done, ask_user or a
// screenshot (the next observation shows the screen), when the task is paused, stopped or killed, and when the screen changed
// under an action that was planned for the old one and says where to click.
func (l *Loop) carryOut(ctx context.Context, n int, res parse.Result, calls []parse.ToolCall, mapper vision.Mapper, hash, shotSHA string) (Outcome, bool) {
	run, _ := l.Limits.BeginResponse(len(res.Actions))
	outs := parse.Outcomes(calls, res)
	var results []prompt.Result
	at := map[int]int{} // the place of an action among the answer's results, by its index in the answer
	if outs != nil {
		results = make([]prompt.Result, len(outs))
		for i, o := range outs {
			if o.Disposition == parse.DispValid {
				at[o.Item.Index] = i
				results[i] = prompt.Result{CallID: o.Call.ID, Tool: o.Call.Name}
			} else {
				results[i] = notRunResult(o)
			}
		}
	} else {
		for _, it := range res.Actions {
			at[it.Index] = len(results)
			results = append(results, prompt.Result{Tool: it.Action.Tool})
		}
		if fb := parse.Feedback(res, l.MaxActions); fb != "" {
			l.Conv.Note(fb)
		}
	}

	var (
		out     Outcome
		over    bool
		checkIn bool
		pause   string
		halt    string // why the actions after this one are not run; empty while the answer goes on
		changed bool
		cur     = hash
	)
	for i, it := range res.Actions {
		slot := at[it.Index]
		if i >= run {
			results[slot] = prompt.Result{CallID: it.CallID, Tool: it.Action.Tool, Code: prompt.CodeDropped, Detail: "dropped: more actions than one answer may run"}
			continue
		}
		if halt != "" {
			results[slot] = notRun(it, halt)
			continue
		}
		was := l.Machine.State()
		st, _ := l.Machine.WaitRunning(ctx)
		if st != Running || l.killed() {
			out, over, halt = l.over(st), true, "the task was stopped"
			results[slot] = notRun(it, halt)
			continue
		}
		if was != Running {
			halt = "the task was paused meanwhile, and the screen may have changed"
			results[slot] = notRun(it, halt)
			continue
		}
		if changed && usesCoordinates(it.Action) {
			halt = "the screen changed after an earlier action of the answer, and this one says where to click on the old screen: look at the screen again"
			results[slot] = notRun(it, halt)
			continue
		}

		id := "t" + strconv.Itoa(n) + "a" + strconv.Itoa(it.Index)
		var step Step
		own := false // an action the loop carries out itself
		switch it.Action.Tool {
		case actions.ToolDone:
			var done Outcome
			own = true
			step, done = l.finish(n, id, it, shotSHA)
			if step.Stop == StopNone {
				out, over = done, true
			}
		case actions.ToolAskUser:
			own = true
			step = l.ask(ctx, n, id, it, shotSHA)
		default:
			step = l.Act.Run(ctx, Input{Turn: n, ID: id, CallID: it.CallID, Action: it.Action, Mapper: mapper, ScreenSHA: shotSHA})
		}
		results[slot] = step.Result
		if step.ScreenHash != "" {
			if cur != "" && step.ScreenHash != cur {
				changed = true
			}
			cur = step.ScreenHash
		}

		switch step.Stop {
		case StopKilled:
			out, over, halt = Outcome{End: EndKilled}, true, "the session is being killed"
		case StopCanceled:
			out, over, halt = l.interrupted(), true, "the task was stopped"
		case StopTimeout:
			out, over, halt = Outcome{End: EndTimeout, Err: limits.ErrSessionTimeout}, true, "the session's time is up"
		case StopAudit:
			out, over, halt = Outcome{End: EndAudit, Err: step.Err}, true, "the audit log can no longer be written"
		case StopBudget:
			checkIn, halt = true, "the task's budget is used up"
		case StopBatch:
			halt = "an earlier action of the answer did not work"
		}
		if halt != "" {
			continue
		}
		if l.Guard != nil && !own {
			if why := l.Guard.Action(it.Action, step); why != "" {
				pause, halt = why, "the task was paused"
				continue
			}
		}
		switch {
		case over:
			halt = "the task is finished"
		case it.Action.Tool == actions.ToolAskUser:
			halt = "the user answered, and the screen may have changed meanwhile"
		case it.Action.Tool == actions.ToolScreenshot:
			halt = "a screenshot ends the answer: the next observation shows the screen"
		}
	}

	if err := l.Conv.Results(results, prompt.Limits{}); err != nil && !over {
		return Outcome{End: EndFailed, Err: err}, true
	}
	if over {
		return out, true
	}
	if checkIn {
		l.checkIn("all the actions it was allowed")
	}
	if pause != "" {
		l.pause("not getting anywhere", pause+" Resume to let the task go on, or Stop to end it.", l.resetGuard)
	}
	return Outcome{}, false
}

// mask removes what the session knows to be secret from arguments on their way to the log or the viewer.
func (l *Loop) mask(raw json.RawMessage) json.RawMessage {
	if l.Redactor != nil {
		return l.Redactor.RedactBytes(raw)
	}
	return raw
}

// localDone writes down and shows an action the loop carries out itself (done, ask_user) that went well: the log has its
// result, the viewer its trace.
func (l *Loop) localDone(n int, id string, it parse.Item, shotSHA string, res prompt.Result, ms int64) error {
	args := l.mask(it.Action.Raw)
	result, _ := json.Marshal(map[string]any{"ok": res.OK, "detail": textclean.Line(res.Detail, 400)})
	err := l.record(audit.Record{Kind: audit.KindAction, Event: "result", Actor: "llm", Turn: n, ActionID: id, Tool: it.Action.Tool, Args: args,
		Reason: it.Action.Reason, Verdict: "allowed", Result: result, ScreenshotSHA256: shotSHA})
	l.send(viewer.Trace{ActionID: id, Turn: n, Tool: it.Action.Tool, Args: args, Reason: it.Action.Reason, Verdict: "allow", Status: "done", DurationMS: ms,
		Summary: textclean.Line(res.Detail, 400)})
	return err
}

// unreadable is the step of a local action whose arguments are not the kind its tool has: a bug, said to the model as a failure.
func unreadable(it parse.Item) Step {
	return Step{Result: prompt.Result{CallID: it.CallID, Tool: it.Action.Tool, Action: prompt.Describe(it.Action), Code: actions.CodeInternal, Detail: "the arguments could not be read"}, Stop: StopBatch}
}

// finish ends the task with the model's own verdict on it.
func (l *Loop) finish(n int, id string, it parse.Item, shotSHA string) (Step, Outcome) {
	args, ok := it.Action.Args.(*actions.DoneArgs)
	if !ok {
		return unreadable(it), Outcome{}
	}
	summary := textclean.Block(args.Summary, maxSummaryText)
	res := prompt.Result{CallID: it.CallID, Tool: it.Action.Tool, Action: prompt.Describe(it.Action), OK: true, Detail: "the task is finished"}
	step := Step{Result: res}
	if err := l.localDone(n, id, it, shotSHA, res, 0); err != nil {
		step.Stop, step.Err = StopAudit, err
		return step, Outcome{}
	}
	l.say("assistant", summary)
	return step, Outcome{End: EndDone, Success: args.Success, Summary: summary}
}

// ask puts the model's question to the person and waits for the answer.
func (l *Loop) ask(ctx context.Context, n int, id string, it parse.Item, shotSHA string) Step {
	args, ok := it.Action.Args.(*actions.AskUserArgs)
	if !ok {
		return unreadable(it)
	}
	res := prompt.Result{CallID: it.CallID, Tool: it.Action.Tool, Action: prompt.Describe(it.Action)}
	if l.Person == nil {
		res.Code, res.Detail = actions.CodeUnsupported, "there is nobody to ask"
		return Step{Result: res, Stop: StopBatch}
	}
	question := textclean.Block(args.Question, maxQuestionChat)
	l.Machine.ToIf([]State{Running}, AwaitingUser, "the model asked: "+textclean.Line(question, 80))
	l.say("assistant", question)
	began := l.now()
	answer, err := l.Person.Ask(ctx, question)
	l.Machine.ToIf([]State{AwaitingUser}, Running, "answered")
	if err != nil {
		res.Code, res.Detail = actions.CodeInternal, "the question was not answered"
		if ctx.Err() != nil {
			return Step{Result: res, Stop: StopCanceled}
		}
		return Step{Result: res, Stop: StopBatch}
	}
	res.OK, res.Detail = true, "The user answered: "+textclean.Line(answer, maxAnswerDetail)
	if len([]rune(answer)) > maxAnswerDetail {
		// What does not fit in a result goes in the next observation, under the person's own words.
		l.Conv.UserMessage(answer)
	}
	step := Step{Result: res}
	if aerr := l.localDone(n, id, it, shotSHA, res, l.now().Sub(began).Milliseconds()); aerr != nil {
		step.Stop, step.Err = StopAudit, aerr
	}
	return step
}
