// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"fmt"
	"io"
	"sync"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

// terminalSink writes to the terminal what the viewer would show. Without a viewer it is all the person sees of a session: what
// the model says, each action and how it went, and every notice. With one it carries only the notices, so that a warning is not
// lost when the window is behind others.
type terminalSink struct {
	mu  sync.Mutex
	out io.Writer
	// all says whether to write the model's words and the actions too.
	all bool
}

func (t *terminalSink) line(format string, a ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.out, format+"\n", a...)
}

// Send implements Sink.
func (t *terminalSink) Send(m viewer.Message) {
	switch v := m.(type) {
	case viewer.Notice:
		t.line("%s: %s", noticeWord(v.Level), textclean.Line(v.Text, 400))
	case viewer.Chat:
		if t.all {
			who := "model"
			if v.Role == "user" {
				who = "you"
			}
			t.line("%s: %s", who, textclean.Line(v.Text, 400))
		}
	case viewer.Trace:
		if t.all && v.Status != "running" {
			t.line("  turn %d, %s %s (%s): %s", v.Turn, v.Tool, v.ActionID, v.Status, textclean.Line(v.Summary, 200))
		}
	}
}

func noticeWord(level string) string {
	switch level {
	case viewer.LevelError:
		return "ERROR"
	case viewer.LevelWarn:
		return "WARNING"
	}
	return "NOTE"
}

// teeSink sends to several sinks.
type teeSink []Sink

// Send implements Sink.
func (t teeSink) Send(m viewer.Message) {
	for _, s := range t {
		s.Send(m)
	}
}
