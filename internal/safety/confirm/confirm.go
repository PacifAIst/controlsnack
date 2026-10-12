// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package confirm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode"
)

// Outcome says how a confirmation request ended. Only Approved lets the action run.
type Outcome string

// The outcomes. Everything except Approved is a refusal.
const (
	Approved Outcome = "approved" // the user clicked Approve
	Rejected Outcome = "rejected" // the user clicked Reject (or pressed Esc)
	Timeout  Outcome = "timeout"  // nobody answered in time
	NoViewer Outcome = "no_viewer"
	Canceled Outcome = "canceled" // Stop, Kill or the end of the session
	Failed   Outcome = "failed"   // the request could not be shown
)

// RuleInfo is a matched rule as the user sees it in the modal.
type RuleInfo struct {
	ID          string `json:"id"`
	Explanation string `json:"explanation"`
}

// Request is one thing the user is asked to decide.
type Request struct {
	// ID is assigned by the broker; callers leave it empty.
	ID       string
	ActionID string
	Tool     string
	// Args are the action's arguments with secrets already redacted.
	Args   any
	Rules  []RuleInfo
	Reason string
	// ExpiresAt is set by the broker: the moment an unanswered request turns into a rejection.
	ExpiresAt time.Time
}

// Result is the answer to Ask.
type Result struct {
	Outcome Outcome
	// Note is what the user typed next to their decision. It is trusted text from the user and is passed on to the
	// model as such; it is trimmed and length-limited here.
	Note string
}

// Approved reports whether the action may run.
func (r Result) Approved() bool { return r.Outcome == Approved }

// MaxNoteLength bounds the user's note.
const MaxNoteLength = 500

// Presenter shows requests to the user; in a session it is the viewer's WebSocket.
type Presenter interface {
	// Present shows the request. If it returns an error the request is rejected as Failed.
	Present(ctx context.Context, r Request) error
	// Resolved tells the user interface that a request is over (answered, timed out, canceled) so it can close the
	// modal. It must not block.
	Resolved(id string, outcome Outcome)
}

// Options configure a Broker.
type Options struct {
	// Timeout is how long a request waits for an answer (default 120 s).
	Timeout time.Duration
	// Now and NewTimer are injectable for tests; they default to the wall clock.
	Now      func() time.Time
	NewTimer func(d time.Duration) (fired <-chan time.Time, stop func())
}

// Broker brokers confirmations. It never approves by itself: no viewer attached, a timeout, a cancellation or an
// error in showing the request all end as a refusal (PLAN.md section 6.7). It is safe for concurrent use.
type Broker struct {
	opts Options

	mu        sync.Mutex
	presenter Presenter
	pending   map[string]*pendingRequest
}

type pendingRequest struct {
	req  Request
	done chan Result // buffered; written once
}

// New creates a broker with no viewer attached.
func New(o Options) *Broker {
	if o.Timeout <= 0 {
		o.Timeout = 120 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewTimer == nil {
		o.NewTimer = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTimer(d)
			return t.C, func() { t.Stop() }
		}
	}
	return &Broker{opts: o, pending: map[string]*pendingRequest{}}
}

// SetPresenter attaches the viewer, or detaches it with nil. Detaching rejects every pending request at once:
// with nobody watching, nothing can be approved.
func (b *Broker) SetPresenter(p Presenter) {
	b.mu.Lock()
	b.presenter = p
	var gone []*pendingRequest
	if p == nil {
		for _, pr := range b.pending {
			gone = append(gone, pr)
		}
	}
	b.mu.Unlock()
	for _, pr := range gone {
		b.finish(pr.req.ID, Result{Outcome: NoViewer})
	}
}

// Ask shows the request to the user and waits for the answer. It returns early with a refusal when no viewer is
// attached, when ctx ends (Stop or Kill), or when the timeout passes.
func (b *Broker) Ask(ctx context.Context, req Request) Result {
	b.mu.Lock()
	p := b.presenter
	if p == nil {
		b.mu.Unlock()
		return Result{Outcome: NoViewer}
	}
	id, err := newID()
	if err != nil {
		b.mu.Unlock()
		return Result{Outcome: Failed}
	}
	req.ID = id
	req.ExpiresAt = b.opts.Now().Add(b.opts.Timeout)
	pr := &pendingRequest{req: req, done: make(chan Result, 1)}
	b.pending[id] = pr
	b.mu.Unlock()

	// Present outside the lock: it may talk to a slow network peer.
	if err := p.Present(ctx, req); err != nil {
		b.finish(id, Result{Outcome: Failed})
		return <-pr.done
	}

	fired, stop := b.opts.NewTimer(b.opts.Timeout)
	defer stop()
	select {
	case res := <-pr.done:
		return res
	case <-fired:
		b.finish(id, Result{Outcome: Timeout})
	case <-ctx.Done():
		b.finish(id, Result{Outcome: Canceled})
	}
	return <-pr.done
}

// Resolve records the user's decision. It is called by the viewer when the user clicks Approve or Reject. An
// unknown or already finished request is an error and changes nothing, so a late or replayed click cannot approve
// anything.
func (b *Broker) Resolve(id string, approve bool, note string) error {
	outcome := Rejected
	if approve {
		outcome = Approved
	}
	if !b.finish(id, Result{Outcome: outcome, Note: cleanNote(note)}) {
		return fmt.Errorf("confirmation %q is not pending", id)
	}
	return nil
}

// CancelAll rejects every pending request, for Stop and Kill.
func (b *Broker) CancelAll() {
	b.mu.Lock()
	ids := make([]string, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.finish(id, Result{Outcome: Canceled})
	}
}

// Pending returns the requests waiting for an answer.
func (b *Broker) Pending() []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Request, 0, len(b.pending))
	for _, pr := range b.pending {
		out = append(out, pr.req)
	}
	return out
}

// finish ends a request exactly once. It reports whether this call was the one that ended it.
func (b *Broker) finish(id string, res Result) bool {
	b.mu.Lock()
	pr, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	p := b.presenter
	b.mu.Unlock()
	if !ok {
		return false
	}
	pr.done <- res // buffered and written once: never blocks
	if p != nil {
		p.Resolved(id, res.Outcome)
	}
	return true
}

func newID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.New("no randomness available for a confirmation id")
	}
	return "c_" + hex.EncodeToString(raw[:]), nil
}

// cleanNote trims a user note, drops control characters and limits its length.
func cleanNote(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		out = append(out, r)
		if len(out) >= MaxNoteLength {
			break
		}
	}
	return string(out)
}
