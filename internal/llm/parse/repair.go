// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
)

func asError(err error, target **actions.Error) bool { return errors.As(err, target) }

func was(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// problemLines writes what was wrong with the failed actions, one line for each.
func problemLines(failures []Failure) string {
	var b strings.Builder
	for _, f := range failures {
		switch {
		case f.Index == 0:
			b.WriteString("- the answer")
		case f.Err.Tool != "":
			fmt.Fprintf(&b, "- action %d (%s)", f.Index, f.Err.Tool)
		default:
			fmt.Fprintf(&b, "- action %d", f.Index)
		}
		for i, p := range f.Err.Problems {
			if i == 0 {
				b.WriteString(": ")
			} else {
				b.WriteString("; ")
			}
			b.WriteString(p.String())
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// RepairMessage is what a model is told when its answer held no action that could be run: what was wrong, every
// problem of every action, and how to answer. native says the backend takes tool calls; otherwise the answer is a JSON
// object. A turn asks at most MaxRepairs times.
func RepairMessage(res Result, set *actions.Set, max int, native bool) string {
	if max < 1 {
		max = actions.DefaultMaxActions
	}
	var b strings.Builder
	b.WriteString("Your last answer could not be used, so nothing was done.\n")
	b.WriteString(problemLines(res.Failures))
	if res.Skipped > 0 {
		fmt.Fprintf(&b, "The %s after the first mistake %s not run either: an action may depend on the ones before it.\n", plural(res.Skipped, "valid action", "valid actions"), was(res.Skipped))
	}
	if res.Dropped > 0 {
		fmt.Fprintf(&b, "%s beyond the limit %s dropped.\n", plural(res.Dropped, "action", "actions"), was(res.Dropped))
	}
	if native {
		fmt.Fprintf(&b, "Answer again with tool calls, at most %s in one answer.\n", plural(max, "action", "actions"))
	} else {
		fmt.Fprintf(&b, "Answer again with one JSON object and nothing else, with at most %s: {\"thought\": \"...\", \"actions\": [{\"tool\": \"<name>\", \"args\": {...}, \"reason\": \"...\"}]}\n", plural(max, "action", "actions"))
	}
	fmt.Fprintf(&b, "The tools are: %s.", strings.Join(set.Names(), ", "))
	return b.String()
}

// Feedback is what a model is told next to the results of the actions that were run, when its answer held more than they
// were: an action that was not valid and what was wrong with it, the actions after it that were not run, the actions
// beyond the limit. It is empty when there is nothing to say.
func Feedback(res Result, max int) string {
	if max < 1 {
		max = actions.DefaultMaxActions
	}
	var parts []string
	if len(res.Failures) > 0 {
		parts = append(parts, "Not run, because it was not valid:\n"+strings.TrimRight(problemLines(res.Failures), "\n"))
	}
	if res.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%s after the first mistake %s not run: an action may depend on the ones before it.", plural(res.Skipped, "valid action", "valid actions"), was(res.Skipped)))
	}
	if res.Dropped > 0 {
		parts = append(parts, fmt.Sprintf("%s beyond the limit of %d in one answer %s dropped.", plural(res.Dropped, "action", "actions"), max, was(res.Dropped)))
	}
	return strings.Join(parts, "\n")
}
