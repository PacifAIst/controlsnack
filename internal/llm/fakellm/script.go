// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package fakellm

import (
	"encoding/json"
	"time"
)

// Reply is what the fake model answers.
type Reply struct {
	// Text is the words of the answer; Calls are native tool calls made next to them.
	Text  string
	Calls []Call
	// Finish is the finish_reason ("stop" when empty; "length" makes a client treat the answer as cut off).
	Finish string
	// Status, when not 0, is an HTTP status sent instead of an answer, with Body as its body (a plausible error body when
	// empty).
	Status int
	Body   string
	// Delay is how long the server waits before it answers.
	Delay time.Duration
	// Hang makes the server never answer: the request ends when the client gives up.
	Hang bool
	// Garbage, when not empty, is sent as the body of a good answer in place of the JSON, for a client to meet a server that
	// is not one.
	Garbage string
}

// Step is one answer in a script, with what the request must be for the answer to be given.
type Step struct {
	// When, if set, must be true of the request. A request it is false for is a divergence from the script: the test is
	// told (Server.Errors) and the client gets a 409.
	When func(*Request) bool
	// Reply makes the answer from the request, which lets a script read the observation (see NodeID).
	Reply func(*Request) Reply
}

// Say answers in words.
func Say(text string) Step { return Fn(func(*Request) Reply { return Reply{Text: text} }) }

// Act answers with native tool calls.
func Act(calls ...Call) Step { return Fn(func(*Request) Reply { return Reply{Calls: calls} }) }

// Fn answers with whatever the function makes of the request.
func Fn(f func(*Request) Reply) Step { return Step{Reply: f} }

// Fail answers with an HTTP error.
func Fail(status int, body string) Step {
	return Fn(func(*Request) Reply { return Reply{Status: status, Body: body} })
}

// Hang never answers.
func Hang() Step { return Fn(func(*Request) Reply { return Reply{Hang: true} }) }

// Expect adds a condition to a step.
func (s Step) Expect(when func(*Request) bool) Step {
	s.When = when
	return s
}

// After delays the answer of a step.
func (s Step) After(d time.Duration) Step {
	f := s.Reply
	s.Reply = func(r *Request) Reply {
		rep := f(r)
		rep.Delay = d
		return rep
	}
	return s
}

// Tool makes a call of a tool with arguments given as a Go value (marshaled) or as JSON text (a string or []byte).
func Tool(name string, args any) Call {
	switch a := args.(type) {
	case nil:
		return Call{Name: name, Args: "{}"}
	case string:
		return Call{Name: name, Args: a}
	case []byte:
		return Call{Name: name, Args: string(a)}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return Call{Name: name, Args: "{}"}
	}
	return Call{Name: name, Args: string(raw)}
}

// Action is an action of an answer in JSON mode.
type Action struct {
	Tool   string
	Args   any
	Reason string
}

// Actions answers in JSON mode: the object of the response schema, as text.
func Actions(thought string, actions ...Action) Step {
	type item struct {
		Tool   string `json:"tool"`
		Args   any    `json:"args"`
		Reason string `json:"reason,omitempty"`
	}
	items := make([]item, len(actions))
	for i, a := range actions {
		args := a.Args
		if args == nil {
			args = map[string]any{}
		}
		items[i] = item{Tool: a.Tool, Args: args, Reason: a.Reason}
	}
	raw, _ := json.Marshal(struct {
		Thought string `json:"thought,omitempty"`
		Actions []item `json:"actions"`
	}{thought, items})
	return Say(string(raw))
}
