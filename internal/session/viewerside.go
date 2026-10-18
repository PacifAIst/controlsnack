// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"errors"
	"sync"

	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
	"github.com/PacifAIst/controlsnack-local/web"
)

// lateInbound is the receiver of what the person does in the page, given to the hub before the console that handles it exists
// (the console is made with the rest of the session, which needs the hub to speak to). Until it is set, the page has nobody
// to talk to, and nothing is lost: no page is connected yet.
type lateInbound struct {
	mu sync.Mutex
	in viewer.Inbound
}

func (l *lateInbound) set(in viewer.Inbound) { l.mu.Lock(); l.in = in; l.mu.Unlock() }

func (l *lateInbound) get() viewer.Inbound { l.mu.Lock(); defer l.mu.Unlock(); return l.in }

func (l *lateInbound) Chat(text string) {
	if in := l.get(); in != nil {
		in.Chat(text)
	}
}

func (l *lateInbound) Confirm(id string, approve bool, note string) {
	if in := l.get(); in != nil {
		in.Confirm(id, approve, note)
	}
}

func (l *lateInbound) Control(op string) {
	if in := l.get(); in != nil {
		in.Control(op)
	}
}

// presence is who is told when the page connects and when it goes: the frame pump, the console and the dead-man switch, which
// are made after the hub that calls them.
type presence struct {
	mu         sync.Mutex
	up, down   []func()
	connected_ bool
}

func (p *presence) add(up, down func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.up, p.down = append(p.up, up), append(p.down, down)
}

func (p *presence) connected() {
	p.mu.Lock()
	fs := append([]func(){}, p.up...)
	p.connected_ = true
	p.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

func (p *presence) gone() {
	p.mu.Lock()
	// The pump goes first and the console and dead-man after, in the order they were added reversed: the pump stops asking
	// for pictures before the rest hears there is nobody to show them to.
	fs := append([]func(){}, p.down...)
	p.connected_ = false
	p.mu.Unlock()
	for i := len(fs) - 1; i >= 0; i-- {
		fs[i]()
	}
}

// viewerSide is the viewer's server and hub.
type viewerSide struct {
	srv  *viewer.Server
	hub  *viewer.Hub
	in   *lateInbound
	pres *presence
}

// viewerArgs are what the viewer is made from.
type viewerArgs struct {
	Killer *killswitch.Killer
	Token  string
	Hello  func() viewer.Hello
	// Audit receives what the door saw, as records.
	Audit func(audit.Record)
}

// newViewerSide makes the viewer's server and hub. The hub speaks to the person; what the person does goes to whatever is given
// to in.set, and the presence callbacks are added with pres.add.
func newViewerSide(a viewerArgs) (*viewerSide, error) {
	if a.Killer == nil || a.Hello == nil {
		return nil, errors.New("the viewer needs the kill switch and the facts of the session")
	}
	vs := &viewerSide{in: &lateInbound{}, pres: &presence{}}
	hub, err := viewer.NewHub(viewer.HubOptions{
		CSRF: func() string { return vs.srv.CSRF() }, Hello: a.Hello, Inbound: vs.in, Killer: a.Killer,
		OnConnect: vs.pres.connected, OnDisconnect: vs.pres.gone,
	})
	if err != nil {
		return nil, err
	}
	vs.hub = hub
	srv, err := viewer.New(viewer.Options{Static: web.Static(), Killer: a.Killer, KillToken: a.Token, WebSocket: hub, Event: func(e viewer.Event) {
		if a.Audit == nil {
			return
		}
		kind := audit.KindUser
		if e.Kind == viewer.EventTripwire || e.Kind == viewer.EventRefused {
			kind = audit.KindError
		}
		a.Audit(audit.Record{Kind: kind, Event: e.Kind, Actor: "system", Data: audit.ToRaw(map[string]any{"detail": e.Detail, "suppressed": e.Suppressed})})
	}})
	if err != nil {
		return nil, err
	}
	vs.srv = srv
	return vs, nil
}
