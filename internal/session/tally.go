// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
)

// Tally is a Recorder that counts what goes through it, for the summary at the end of a session: the model's answers, and the
// actions by what became of them.
type Tally struct {
	Next Recorder

	mu       sync.Mutex
	answers  int
	verdicts map[string]string // action id to verdict
}

// Append writes the record and, if it was written, counts it.
func (t *Tally) Append(r audit.Record) (audit.Record, error) {
	out, err := t.Next.Append(r)
	if err != nil {
		return out, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case r.Kind == audit.KindLLM && r.Event == "response":
		t.answers++
	case r.Kind == audit.KindAction && r.ActionID != "":
		if t.verdicts == nil {
			t.verdicts = map[string]string{}
		}
		if r.Event == "decision" {
			t.verdicts[r.ActionID] = r.Verdict
		} else if _, seen := t.verdicts[r.ActionID]; !seen {
			t.verdicts[r.ActionID] = r.Verdict // an action the loop did itself (done, ask_user) has only its result
		}
	}
	return out, nil
}

// Totals is what a Tally counted.
type Totals struct {
	Answers int
	Actions int
	// ByVerdict is how many actions came to each verdict.
	ByVerdict map[string]int
}

// Totals returns the counts so far.
func (t *Tally) Totals() Totals {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := Totals{Answers: t.answers, Actions: len(t.verdicts), ByVerdict: map[string]int{}}
	for _, v := range t.verdicts {
		out.ByVerdict[v]++
	}
	return out
}

// String lists the actions by verdict, most to least ("12 allowed, 2 confirmed, 1 denied"), or says there were none.
func (t Totals) String() string {
	if t.Actions == 0 {
		return "no actions"
	}
	type kv struct {
		k string
		n int
	}
	var list []kv
	for k, n := range t.ByVerdict {
		list = append(list, kv{k, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].k < list[j].k
	})
	parts := make([]string, len(list))
	for i, e := range list {
		parts[i] = fmt.Sprintf("%d %s", e.n, e.k)
	}
	return strings.Join(parts, ", ")
}
