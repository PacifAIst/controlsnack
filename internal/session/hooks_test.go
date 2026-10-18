// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
)

func TestTheViewerIsToldWhenAnActionStartsAndWhenItHasRun(t *testing.T) {
	g := newDgrid(t)
	n := 0
	var during int
	var starts, stops int
	g.d.Activity = func(starting bool) {
		n++
		if starting {
			starts++
		} else {
			stops++
		}
	}
	g.agent.on("input", func(any, any) error { during = n; return nil })
	g.run(&actions.KeyArgs{Keys: "a"}, "")
	if n != 2 || during != 1 || starts != 1 || stops != 1 {
		t.Errorf("told %d times, %d before the action ran", n, during)
	}
	// An action that is refused is not activity.
	n = 0
	g.run(&actions.ExecArgs{Command: "rm -rf /", TimeoutS: 60}, "")
	if n != 0 {
		t.Errorf("told %d times of an action that did not run", n)
	}
}

func TestTheLoopSaysHowLongEachCallToTheModelTook(t *testing.T) {
	g := newLrig(t)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	g.loop.Now = func() time.Time { at = at.Add(300 * time.Millisecond); return at }
	var got []time.Duration
	g.loop.Answered = func(d time.Duration) { got = append(got, d) }
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK)}
	g.run("t")
	if len(got) != 2 || got[0] != 300*time.Millisecond {
		t.Errorf("durations %v", got)
	}
}
