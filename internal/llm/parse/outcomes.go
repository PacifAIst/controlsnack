// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

// Disposition is what became of one native call of an answer.
type Disposition int

// The four things that can become of a call.
const (
	// DispValid: the call was valid and is to be run.
	DispValid Disposition = iota
	// DispInvalid: the call was not valid; the Failure says why.
	DispInvalid
	// DispNotRun: the call was valid, but an earlier call of the answer was not, and an action may depend on the ones before it.
	DispNotRun
	// DispDropped: the call was beyond the limit on actions in one answer and was not even looked at.
	DispDropped
)

// Outcome is a call of an answer with what became of it.
type Outcome struct {
	Call ToolCall
	Disposition
	// Item is set for a valid call, Failure for an invalid one.
	Item    *Item
	Failure *Failure
}

// Outcomes pairs each native call of an answer with what became of it, in the order the model made them. The APIs want
// every call answered with a result, and the session answers a valid call with its result, an invalid one with the
// problems found, a call that was not run with that, and a dropped one with that. For an answer that did not use native
// calls (calls is empty) there is nothing to answer, and the result is nil.
func Outcomes(calls []ToolCall, res Result) []Outcome {
	if len(calls) == 0 {
		return nil
	}
	items := make(map[int]*Item, len(res.Actions))
	for i := range res.Actions {
		items[res.Actions[i].Index] = &res.Actions[i]
	}
	failures := make(map[int]*Failure, len(res.Failures))
	for i := range res.Failures {
		failures[res.Failures[i].Index] = &res.Failures[i]
	}
	looked := len(calls) - res.Dropped
	out := make([]Outcome, len(calls))
	for i, c := range calls {
		idx := i + 1
		o := Outcome{Call: c}
		switch {
		case idx > looked:
			o.Disposition = DispDropped
		case items[idx] != nil:
			o.Disposition, o.Item = DispValid, items[idx]
		case failures[idx] != nil:
			o.Disposition, o.Failure = DispInvalid, failures[idx]
		default:
			o.Disposition = DispNotRun
		}
		out[i] = o
	}
	return out
}
