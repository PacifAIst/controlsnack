// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/confirm"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// Limits on what the person's words become.
const (
	maxChatText   = 2000
	maxNoteText   = 500
	maxQueued     = 20
	timeFormatMS  = "2006-01-02T15:04:05.000Z07:00"
	chatIDPrefixU = "u"
)

// Stepper is what step mode is switched on and off through (*Dispatcher).
type Stepper interface{ SetStep(on bool) }

// Console is the person's side of a session. It is what the viewer's hub calls when the person writes, answers a question or
// presses a button (viewer.Inbound); what the confirmation broker shows its questions through (confirm.Presenter); and what
// the turn loop asks about the person (Person). Its methods run on the hub's goroutine and never block.
type Console struct {
	Machine *Machine
	Broker  *confirm.Broker
	// Limits hears of every word from the person: it postpones the idle timeout.
	Limits *limits.Limits
	Viewer Sink
	Audit  Recorder
	// Redactor removes what the session knows to be secret from what the person's words become in the log.
	Redactor *secret.Redactor
	Step     Stepper
	// StartTask is called with the text of a chat message that arrives while the session is Ready: it is a new task. It must
	// not block.
	StartTask func(text string)
	// Fatal is called when the audit log can no longer be written: the session must end.
	Fatal func(error)
	Now   func() time.Time

	mu     sync.Mutex
	queue  []string
	asking chan string // while the model's question waits for the person's answer
	cancel context.CancelFunc
	seq    int
}

func (c *Console) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Console) send(m viewer.Message) {
	if c.Viewer != nil {
		c.Viewer.Send(m)
	}
}

func (c *Console) touch() {
	if c.Limits != nil {
		c.Limits.Touch()
	}
}

// record writes what the person did into the audit log.
func (c *Console) record(event string, data any) {
	if c.Audit == nil {
		return
	}
	b, err := json.Marshal(data)
	if err == nil && c.Redactor != nil {
		b = c.Redactor.RedactBytes(b)
	}
	if err != nil {
		b = nil
	}
	if _, err := c.Audit.Append(audit.Record{Kind: audit.KindUser, Event: event, Actor: "user", Data: b}); err != nil && c.Fatal != nil {
		c.Fatal(fmt.Errorf("the audit log can no longer be written, so the session stops: %w", err))
	}
}

// Attach makes the viewer hear of every change of state.
func (c *Console) Attach() {
	c.Machine.OnChange(func(ch Change) { c.send(viewer.State{State: string(ch.To), Reason: ch.Reason}) })
}

// Chat is what the person wrote. It is shown back to them (so the page's list is the session's, not the page's own), written
// to the log, and then goes where it is meant for: the answer to the model's question if the model asked one; the start of a
// task if the session is ready for one; otherwise the model's next observation.
func (c *Console) Chat(text string) {
	text = textclean.Block(text, maxChatText)
	if strings.TrimSpace(text) == "" {
		return
	}
	c.touch()
	c.mu.Lock()
	c.seq++
	id := chatIDPrefixU + strconv.Itoa(c.seq)
	c.mu.Unlock()
	c.send(viewer.Chat{ID: id, Role: "user", Text: text, TS: c.now().UTC().Format(time.RFC3339)})
	c.record("chat", map[string]string{"text": text})

	c.mu.Lock()
	if ch := c.asking; ch != nil {
		c.asking = nil
		c.mu.Unlock()
		ch <- text // buffered: one answer
		return
	}
	if c.Machine.State() == Ready && c.StartTask != nil {
		c.mu.Unlock()
		c.StartTask(text)
		return
	}
	if c.Machine.State().Final() {
		c.mu.Unlock()
		return
	}
	c.queue = append(c.queue, text)
	if len(c.queue) > maxQueued {
		c.queue = c.queue[len(c.queue)-maxQueued:]
	}
	c.mu.Unlock()
}

// Confirm is the person's answer to a question about an action. An id the broker does not know (a page that was open when the
// question ended) is ignored.
func (c *Console) Confirm(id string, approve bool, note string) {
	c.touch()
	note = textclean.Block(note, maxNoteText)
	if c.Broker == nil || c.Broker.Resolve(id, approve, note) != nil {
		return
	}
	c.record("confirm", map[string]any{"id": id, "approve": approve, "note": note})
}

// Control is a button of the page: pause, resume, stop, step_on or step_off. (Kill never comes here: the hub fires the kill
// switch itself.)
func (c *Console) Control(op string) {
	c.touch()
	switch op {
	case viewer.OpPause:
		c.Machine.ToIf([]State{Running}, Paused, "paused by the person")
	case viewer.OpResume:
		c.Machine.ToIf([]State{Paused}, Running, "resumed by the person")
	case viewer.OpStop:
		c.stop()
	case viewer.OpStepOn, viewer.OpStepOff:
		if c.Step == nil {
			return
		}
		on := op == viewer.OpStepOn
		c.Step.SetStep(on)
		if on {
			c.send(viewer.Notice{Level: viewer.LevelInfo, Text: "Step mode is on: every action needs your approval."})
		} else {
			c.send(viewer.Notice{Level: viewer.LevelInfo, Text: "Step mode is off: actions the rules allow run without asking."})
		}
	default:
		return
	}
	c.record("control", map[string]string{"op": op})
}

// stop ends the task that is running, if one is: its context is canceled, so that the model call, the action, the wait for an
// answer and the pause all end, and the questions that are open are canceled. The session is Ready again when the loop returns.
func (c *Console) stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c.Broker != nil {
		c.Broker.CancelAll()
	}
}

// PauseFor pauses the task for a reason of the session's own (the viewer went away); it resumes only when the person says so.
func (c *Console) PauseFor(reason string) {
	c.Machine.ToIf([]State{Running}, Paused, reason)
}

// Begin makes the context of a task: Stop cancels it. The function returned releases the context when the task has ended.
func (c *Console) Begin(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	return ctx, cancel // Stop after the task has ended cancels a context that is already canceled: nothing
}

// Messages are what the person wrote since the model last looked (Person).
func (c *Console) Messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.queue
	c.queue = nil
	return q
}

// Ask waits for the person's next message and returns it as the answer to the model's question (Person). It ends with the
// context's error if the context ends first.
func (c *Console) Ask(ctx context.Context, _ string) (string, error) {
	ch := make(chan string, 1)
	c.mu.Lock()
	c.asking = ch
	c.mu.Unlock()
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		c.mu.Lock()
		if c.asking == ch {
			c.asking = nil
		}
		c.mu.Unlock()
		return "", ctx.Err()
	}
}

// Connected is told that the page is there: from now on questions are put to it.
func (c *Console) Connected() {
	if c.Broker != nil {
		c.Broker.SetPresenter(c)
	}
}

// Disconnected is told that the page is gone: the questions that are open are refused, and nothing can be approved until it
// comes back.
func (c *Console) Disconnected() {
	if c.Broker != nil {
		c.Broker.SetPresenter(nil)
	}
}

// Present shows a question about an action to the person (confirm.Presenter).
func (c *Console) Present(_ context.Context, r confirm.Request) error {
	args, ok := r.Args.(json.RawMessage)
	if !ok {
		b, err := json.Marshal(r.Args)
		if err != nil {
			return fmt.Errorf("the arguments of the action cannot be shown: %w", err)
		}
		args = b
	}
	rules := make([]viewer.Rule, len(r.Rules))
	for i, ri := range r.Rules {
		rules[i] = viewer.Rule{ID: ri.ID, Explanation: ri.Explanation}
	}
	c.send(viewer.ConfirmRequest{ID: r.ID, ActionID: r.ActionID, Tool: r.Tool, Args: args, Rules: rules, Reason: r.Reason,
		ExpiresAt: r.ExpiresAt.UTC().Format(timeFormatMS)})
	return nil
}

// Resolved tells the page that a question is over, so that it closes it (confirm.Presenter).
func (c *Console) Resolved(id string, o confirm.Outcome) {
	out := viewer.OutcomeCanceled
	switch o {
	case confirm.Approved:
		out = viewer.OutcomeApproved
	case confirm.Rejected:
		out = viewer.OutcomeRejected
	case confirm.Timeout:
		out = viewer.OutcomeExpired
	}
	c.send(viewer.ConfirmResolved{ID: id, Outcome: out})
}
