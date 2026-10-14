// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
)

// ToolCall is a native tool call as a backend reports it. Args is the arguments: a JSON object, or, as OpenAI-compatible
// servers send them, a JSON string that holds the object (an empty string or null means none).
type ToolCall struct {
	// ID is the backend's id of the call, which the result of the action is sent back under (empty when it has none).
	ID   string
	Name string
	Args json.RawMessage
}

// Reply is what a backend returns for one model turn: the words the model wrote, and the tool calls it made.
type Reply struct {
	Text      string
	ToolCalls []ToolCall
}

// Source says where the actions of a reply were found.
type Source string

// The places an answer's actions are looked for, in this order (PLAN.md section 9.5).
const (
	// SourceNative: the backend reported tool calls.
	SourceNative Source = "native"
	// SourceJSON: the text holds a JSON answer, as it is, in a fenced block, or as the first object in some prose.
	SourceJSON Source = "json"
	// SourceNone: neither.
	SourceNone Source = "none"
)

// Limits on what a model says besides its actions.
const (
	maxThoughtRunes = 600
	// MaxRepairs is how many times a turn asks the model to answer again before the session pauses.
	MaxRepairs = 2
)

// Item is a valid action of a reply.
type Item struct {
	// Index is the position of the action among those the model gave, from 1.
	Index int
	// CallID is the id of the native call it came from, or empty.
	CallID string
	Action actions.Action
}

// Failure is an action of a reply that could not be used.
type Failure struct {
	// Index is the position of the action among those the model gave, from 1; 0 for the answer as a whole.
	Index  int
	CallID string
	Err    *actions.Error
}

// Result is what a reply holds.
type Result struct {
	Source Source
	// Thought is what the model said besides its actions: the "thought" of a JSON answer, the words next to native
	// calls. Cleaned, at most 600 characters.
	Thought string
	// Actions are the actions to run: the valid ones before the first one that is not, at most the limit.
	Actions []Item
	// Failures are the actions that were not valid (among the first of the limit), or the answer as a whole when it
	// held no action; each lists every problem.
	Failures []Failure
	// Skipped counts the valid actions after the first invalid one: an action may depend on the ones before it, so they
	// are not run.
	Skipped int
	// Dropped counts the actions beyond the limit; they are not even looked at.
	Dropped int
}

// Usable reports whether there is an action to run.
func (r Result) Usable() bool { return len(r.Actions) > 0 }

// Parse finds the actions in a model's reply and checks each against its tool's schema (PLAN.md section 9.5). Native tool
// calls come first; without any the text is read as a JSON answer (the whole text, else a fenced block, else the first
// object that holds actions), with <think> blocks taken out first and commas before a closing bracket forgiven. max is
// the limit on actions in one response: the actions after it are dropped. The words a model writes next to native calls
// become the reason of the first action that has none.
func Parse(set *actions.Set, reply Reply, max int) Result {
	if max < 1 {
		max = actions.DefaultMaxActions
	}
	text := StripReasoning(reply.Text)
	if len(reply.ToolCalls) > 0 {
		return native(set, reply.ToolCalls, text, max)
	}
	return fromText(set, text, max)
}

// pending is an action found in a reply, not yet checked.
type pending struct {
	index  int
	callID string
	tool   string
	args   json.RawMessage
	reason string
	fault  *actions.Problem // already known to be wrong
}

func native(set *actions.Set, calls []ToolCall, text string, max int) Result {
	res := Result{Source: SourceNative, Thought: textclean.Block(text, maxThoughtRunes)}
	if len(calls) > max {
		res.Dropped = len(calls) - max
		calls = calls[:max]
	}
	items := make([]pending, len(calls))
	for i, c := range calls {
		items[i] = pending{index: i + 1, callID: c.ID, tool: c.Name}
		items[i].args, items[i].fault = argumentsOf(c.Args)
	}
	evaluate(set, items, &res)
	if len(res.Actions) > 0 && res.Actions[0].Action.Reason == "" && text != "" {
		res.Actions[0].Action.Reason = textclean.Line(text, actions.MaxReasonRunes)
	}
	return res
}

// argumentsOf reads the arguments of a call: an object, or a string holding one. Empty means none.
func argumentsOf(raw json.RawMessage) (json.RawMessage, *actions.Problem) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}"), nil
	}
	if s[0] == '"' {
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return nil, &actions.Problem{Path: "args", Message: "is not valid JSON text"}
		}
		if s = strings.TrimSpace(inner); s == "" || s == "null" {
			return json.RawMessage("{}"), nil
		}
	}
	v := decode(s)
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, &actions.Problem{Path: "args", Message: "must be a JSON object of the tool's arguments"}
	}
	// A reason that runs on is cut, as it is in a JSON answer: it is a note for the user, not worth a turn.
	if r, isString := obj["reason"].(string); isString {
		obj["reason"] = cut(r, actions.MaxReasonRunes)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, &actions.Problem{Path: "args", Message: "is not valid JSON"}
	}
	return out, nil
}

func fromText(set *actions.Set, text string, max int) Result {
	res := Result{Source: SourceNone}
	for _, piece := range candidates(text) {
		raw, thought, ok := shapeOf(decode(piece))
		if !ok {
			continue
		}
		res.Source = SourceJSON
		res.Thought = thought
		if len(raw) == 0 {
			res.Failures = []Failure{{Err: actions.NewError(actions.CodeInvalidArgs, "", actions.Problem{Path: "actions", Message: "is empty: give at least one action"})}}
			return res
		}
		if len(raw) > max {
			res.Dropped = len(raw) - max
			raw = raw[:max]
		}
		items := make([]pending, len(raw))
		for i, r := range raw {
			items[i] = pendingOf(i+1, r)
		}
		evaluate(set, items, &res)
		return res
	}
	res.Thought = textclean.Block(text, maxThoughtRunes)
	res.Failures = []Failure{{Err: actions.NewError(actions.CodeInvalidArgs, "", noAction(text))}}
	return res
}

// noAction says what was wrong with an answer that held nothing to do.
func noAction(text string) actions.Problem {
	if strings.TrimSpace(text) == "" {
		return actions.Problem{Path: "answer", Message: "is empty: it has no tool call and no JSON with actions"}
	}
	return actions.Problem{Path: "answer", Message: "has no tool call and no JSON with actions in it"}
}

// shapeOf recognizes the JSON of an answer: an object with "actions" (the envelope), an object that is one action (it has
// a "tool"), or an array of actions. It returns the actions as the model wrote them and the model's thought.
func shapeOf(v any) (items []any, thought string, ok bool) {
	switch x := v.(type) {
	case map[string]any:
		if list, isList := x["actions"].([]any); isList {
			if s, isString := x["thought"].(string); isString {
				thought = textclean.Block(s, maxThoughtRunes)
			}
			return list, thought, true
		}
		if _, hasTool := x["tool"]; hasTool {
			return []any{x}, "", true
		}
	case []any:
		if len(x) > 0 {
			if first, isObject := x[0].(map[string]any); isObject {
				if _, hasTool := first["tool"]; hasTool {
					return x, "", true
				}
			}
		}
	}
	return nil, "", false
}

// pendingOf reads one action of a JSON answer: {"tool": ..., "args": ..., "reason": ...}.
func pendingOf(index int, v any) pending {
	p := pending{index: index}
	obj, ok := v.(map[string]any)
	if !ok {
		p.fault = &actions.Problem{Path: "action", Message: "must be an object with a tool and its args"}
		return p
	}
	tool, isString := obj["tool"].(string)
	if !isString || strings.TrimSpace(tool) == "" {
		p.fault = &actions.Problem{Path: "tool", Message: "is missing: every action names a tool"}
		return p
	}
	p.tool = strings.TrimSpace(tool)
	if reason, isReason := obj["reason"].(string); isReason {
		p.reason = cut(textclean.Line(reason, actions.MaxReasonRunes), actions.MaxReasonRunes)
	}
	args, hasArgs := obj["args"]
	if !hasArgs {
		p.args = json.RawMessage("{}")
		return p
	}
	raw, err := json.Marshal(args)
	if err != nil {
		p.fault = &actions.Problem{Path: "args", Message: "is not valid JSON"}
		return p
	}
	p.args, p.fault = argumentsOf(raw)
	return p
}

// cut shortens a text to at most n characters.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// evaluate checks the actions one by one. The valid ones before the first that is not are to be run; the rest of the
// valid ones are skipped.
func evaluate(set *actions.Set, items []pending, res *Result) {
	for _, it := range items {
		var a actions.Action
		var err error
		if it.fault != nil {
			err = actions.NewError(actions.CodeInvalidArgs, it.tool, *it.fault)
		} else {
			a, err = set.Parse(it.tool, it.args)
		}
		if err != nil {
			var e *actions.Error
			if asError(err, &e) {
				res.Failures = append(res.Failures, Failure{Index: it.index, CallID: it.callID, Err: e})
				continue
			}
			res.Failures = append(res.Failures, Failure{Index: it.index, CallID: it.callID, Err: actions.NewError(actions.CodeInternal, it.tool, actions.Problem{Path: "action", Message: fmt.Sprint(err)})})
			continue
		}
		if a.Reason == "" {
			a.Reason = it.reason
		}
		if len(res.Failures) > 0 {
			res.Skipped++
			continue
		}
		res.Actions = append(res.Actions, Item{Index: it.index, CallID: it.callID, Action: a})
	}
}
