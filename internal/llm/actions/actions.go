// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"encoding/json"
	"strings"
)

// Error codes of a tool result (PLAN.md section 5.3): what the model is told when an action did not do what it asked.
const (
	CodeInvalidArgs     = "invalid_args"
	CodeUnknownTool     = "unknown_tool"
	CodePolicyDenied    = "policy_denied"
	CodeUserRejected    = "user_rejected"
	CodeConfirmTimeout  = "confirm_timeout"
	CodeRateLimited     = "rate_limited"
	CodeBudgetExhausted = "budget_exhausted"
	CodeStaleElement    = "stale_element"
	CodeNotFound        = "not_found"
	CodeTimeout         = "timeout"
	CodeAgentUnavail    = "agent_unavailable"
	CodeUnsupported     = "unsupported"
	CodeInternal        = "internal"
)

// Action is one validated action of a model's response.
type Action struct {
	// Tool is the tool's name.
	Tool string
	// Args is a pointer to the tool's argument struct (*ExecArgs, ...), validated against the tool's schema and with the
	// defaults filled in.
	Args Args
	// Reason is the model's note on why it does this (at most MaxReasonRunes characters); empty when it gave none.
	Reason string
	// Raw is the arguments as JSON after validation, without the reason and with integers written as integers; it is
	// what the audit log and the viewer show (after redaction).
	Raw json.RawMessage
}

// Problem is one thing wrong with an action, in words a model can act on.
type Problem struct {
	// Path names the argument: "args" for the object itself, "args.timeout_s" for a field, "args.region[2]" for an
	// element of an array.
	Path    string
	Message string
}

func (p Problem) String() string { return p.Path + ": " + p.Message }

// Error is why an action was refused before it was done: its tool is unknown or its arguments are not valid. It lists
// every problem found, so the model can put them all right at once.
type Error struct {
	// Code is CodeUnknownTool or CodeInvalidArgs.
	Code     string
	Tool     string
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Tool)
	b.WriteString(": ")
	for i, p := range e.Problems {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(p.String())
	}
	return b.String()
}

// NewError makes an Error whose texts are safe to show and to log: the tool and every problem are cleaned and cut. It is
// for the code that finds a fault of its own in what a model sent (the parser), before any schema is looked at.
func NewError(code, tool string, problems ...Problem) *Error {
	return newError(code, tool, problems...)
}
