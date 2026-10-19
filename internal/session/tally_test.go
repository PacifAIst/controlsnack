// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/safety/audit"
)

// numbering is a Recorder that gives each record the next number, as the log does, or refuses it.
type numbering struct {
	n    uint64
	fail bool
}

func (r *numbering) Append(rec audit.Record) (audit.Record, error) {
	if r.fail {
		return rec, errors.New("the disk is full")
	}
	r.n++
	rec.Seq = r.n
	return rec, nil
}

func TestTheTailOfTheLogIsToldEachRecordThatWasWrittenAndOnlyThose(t *testing.T) {
	next := &numbering{}
	tally := &Tally{Next: next}
	var told []audit.Record
	tally.OnRecord(func(r audit.Record) { told = append(told, r) })

	tally.Append(audit.Record{Kind: audit.KindAction, Event: "decision", Actor: "llm", Tool: "exec", Verdict: "allowed", ActionID: "t1a1"}) //nolint:errcheck // the fake never fails here
	next.fail = true
	if _, err := tally.Append(audit.Record{Kind: audit.KindLLM, Event: "response"}); err == nil {
		t.Fatal("the record that could not be written was taken")
	}
	next.fail = false
	tally.Append(audit.Record{Kind: audit.KindLLM, Event: "response"}) //nolint:errcheck // as above

	if len(told) != 2 || told[0].Seq != 1 || told[1].Seq != 2 {
		t.Fatalf("the tail was told %+v: it should be the two records that were written, with the numbers the log gave them", told)
	}
	line := audit.Summary(told[0])
	for _, want := range []string{"   1 ", "action", "decision", "[llm]", "exec", "-> allowed"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line of the record lacks %q: %q", want, line)
		}
	}
	if tally.Totals().Answers != 1 {
		t.Errorf("counting still works with a tail: %+v", tally.Totals())
	}
}

func TestATallyWithNoTailStillCounts(t *testing.T) {
	tally := &Tally{Next: &numbering{}}
	if _, err := tally.Append(audit.Record{Kind: audit.KindLLM, Event: "response"}); err != nil {
		t.Fatal(err)
	}
	if tally.Totals().Answers != 1 {
		t.Errorf("%+v", tally.Totals())
	}
}
