// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"sync"
	"time"
)

// The kinds of Event.
const (
	// EventAuth: a login link was used and a window got its cookie.
	EventAuth = "viewer_auth"
	// EventAuthDenied: a login link that is unknown or has expired.
	EventAuthDenied = "viewer_auth_denied"
	// EventTripwire: a login link that was already used was used again. The session is ended.
	EventTripwire = "viewer_tripwire"
	// EventRefused: a request that failed a check of the door (Host, Origin, cookie, CSRF, fetch metadata, a second
	// connection).
	EventRefused = "viewer_refused"
)

// Event is something about the viewer's door that the session writes to the audit log. Detail is one clean line made by
// this package; it never holds a token, a cookie or anything else the other side sent.
type Event struct {
	Kind   string
	Detail string
	// Suppressed is how many events of this kind were left out since the last one that was delivered: another process
	// that hammers the door must not fill the audit log (and a log that cannot be written ends the session).
	Suppressed int
}

// eventGap is the least time between two events of one kind.
const eventGap = time.Second

// events delivers events to the session, at most one of each kind per eventGap, except the ones that matter on their
// own (the tripwire and a successful login, which are rare by construction).
type events struct {
	mu   sync.Mutex
	now  func() time.Time
	emit func(Event)
	last map[string]time.Time
	lost map[string]int
}

func newEvents(now func() time.Time, emit func(Event)) *events {
	return &events{now: now, emit: emit, last: map[string]time.Time{}, lost: map[string]int{}}
}

func (e *events) send(kind, detail string) {
	if e.emit == nil {
		return
	}
	e.mu.Lock()
	t := e.now()
	if kind != EventTripwire && kind != EventAuth {
		if last, ok := e.last[kind]; ok && t.Sub(last) < eventGap && t.Sub(last) >= 0 {
			e.lost[kind]++
			e.mu.Unlock()
			return
		}
	}
	e.last[kind] = t
	lost := e.lost[kind]
	e.lost[kind] = 0
	e.mu.Unlock()
	e.emit(Event{Kind: kind, Detail: detail, Suppressed: lost})
}
