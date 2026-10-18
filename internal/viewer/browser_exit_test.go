// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
	"github.com/PacifAIst/controlsnack-local/web"
)

// fakeSession is the other side of the viewer, as the session of Phase 7 will be: it takes what the person does and answers
// through the hub. Nothing in it blocks, as the Inbound contract says.
type fakeSession struct {
	hub     *viewer.Hub
	mu      sync.Mutex
	chats   []string
	pending map[string]string // confirmation id -> tool
}

func (s *fakeSession) Chat(text string) {
	s.mu.Lock()
	s.chats = append(s.chats, text)
	n := len(s.chats)
	s.mu.Unlock()
	s.hub.Send(viewer.Chat{ID: fmt.Sprintf("u%d", n), Role: "user", Text: text, TS: ts})
	s.hub.Send(viewer.Chat{ID: fmt.Sprintf("a%d", n), Role: "assistant", Text: "I will do this: " + text, TS: ts})
}

func (s *fakeSession) Confirm(id string, approve bool, note string) {
	s.mu.Lock()
	tool, ok := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if !ok {
		return // an id the session does not know
	}
	outcome, status, summary := viewer.OutcomeRejected, "rejected", "you rejected it"
	if approve {
		outcome, status, summary = viewer.OutcomeApproved, "done", "it ran ("+note+")"
	}
	s.hub.Send(viewer.ConfirmResolved{ID: id, Outcome: outcome})
	s.hub.Send(viewer.Trace{ActionID: "act-" + id, Turn: 1, Tool: tool, Args: json.RawMessage(`{"command":"rm -r ~/work/old"}`), Reason: "clean up", Verdict: "confirm", Rules: []string{"X-RM"}, Status: status, Summary: summary})
}

func (s *fakeSession) Control(op string) {
	switch op {
	case viewer.OpPause:
		s.hub.Send(viewer.State{State: viewer.StatePaused, Reason: "you paused"})
	case viewer.OpResume:
		s.hub.Send(viewer.State{State: viewer.StateRunning, Reason: "you resumed"})
	case viewer.OpStop:
		s.hub.Send(viewer.State{State: viewer.StateReady, Reason: "you stopped the task"})
	}
}

func (s *fakeSession) ask(id, tool string) {
	s.mu.Lock()
	s.pending[id] = tool
	s.mu.Unlock()
	req := confirmReq(id, tool)
	s.hub.Send(viewer.State{State: viewer.StateAwaitingConfirm, Reason: "waiting for you"})
	s.hub.Send(req)
}

// The phase exit of PLAN.md: the viewer runs against a fake session source, with live frames, chat echo, a confirmation
// round trip and Kill.
func TestTheViewerAgainstAFakeSession(t *testing.T) {
	p := newPage(t)

	// The screen of the fake guest, which the pump looks at: it changes color when the test says so.
	var screenNo atomic.Int32
	pictures := map[int32][]byte{
		0: webp(t, solid(1280, 800, color.RGBA{R: 30, G: 90, B: 200, A: 255})),
		1: webp(t, solid(1280, 800, color.RGBA{R: 220, G: 40, B: 40, A: 255})),
	}
	agent := agentFunc(func(_ context.Context, typ string, req any, _ []byte, res any) ([]byte, error) {
		n := screenNo.Load()
		r := req.(protocol.ScreenshotReq)
		hash := fmt.Sprint("screen", n)
		out := res.(*protocol.ScreenshotRes)
		*out = protocol.ScreenshotRes{Hash: hash, Cursor: protocol.Point{X: 200 + int(n)*300, Y: 300}}
		if r.IfChangedSince == hash {
			out.Unchanged = true
			return nil, nil
		}
		out.W, out.H, out.Format = 1280, 800, "webp"
		return pictures[n], nil
	})

	session := &fakeSession{pending: map[string]string{}}
	var pump *viewer.Pump
	h := startHubWith(t, func(o *viewer.Options) { o.Static = web.Static() }, func(o *viewer.HubOptions) {
		o.Inbound = session
		prevConnect, prevGone := o.OnConnect, o.OnDisconnect
		o.OnConnect = func() { prevConnect(); pump.OnConnect() }
		o.OnDisconnect = func() { pump.OnDisconnect(); prevGone() }
	})
	session.hub = h.hub
	var err error
	pump, err = viewer.NewPump(viewer.PumpOptions{Agent: agent, Sink: h.hub, IdleFPS: 25, ActiveFPS: 25, StatsEvery: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pump.OnDisconnect)
	h.hub.Send(viewer.State{State: viewer.StateRunning, Reason: "task started"})
	p.open(h.rig)

	// Live frames: the screen the pump sees is on the canvas, and follows the guest when it changes.
	p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
	p.wait("the first frame", `(() => { const [r, g, b] = `+centerPixel+`; return b > 150 && r < 90; })()`)
	screenNo.Store(1)
	p.wait("the guest's next screen", `(() => { const [r, g, b] = `+centerPixel+`; return r > 180 && b < 90; })()`)
	p.wait("the stats of the pump", `/KB\/s/.test(document.getElementById('traffic').textContent) && !/^0\.0 KB\/s/.test(document.getElementById('traffic').textContent)`)

	// Chat echo: what the person writes comes back from the session, and so does the answer.
	p.do(chromedp.Focus("#chat-input"), chromedp.KeyEvent("open the file manager"))
	p.str(`(() => { const ev = new KeyboardEvent('keydown', {key: 'Enter', bubbles: true, cancelable: true}); document.getElementById('chat-input').dispatchEvent(ev); return ''; })()`)
	p.wait("the echo and the answer", `document.querySelectorAll('.msg').length === 2`)
	if got := p.strs(`Array.from(document.querySelectorAll('.msg-body')).map(e => e.textContent)`); got[0] != "open the file manager" || got[1] != "I will do this: open the file manager" {
		t.Errorf("chat %q", got)
	}

	// A confirmation round trip, both ways: the session asks; the person rejects with Esc; the session says what happened.
	session.ask("q1", "exec")
	p.wait("the dialog", hasDialog)
	p.wait("the state", `document.getElementById('state').textContent === 'Needs your approval'`)
	p.do(chromedp.KeyEvent(kb.Escape))
	p.wait("the dialog to close", noDialog)
	p.wait("the rejection in the actions", `Array.from(document.querySelectorAll('.t-status')).some(e => e.textContent === 'rejected')`)
	// ...and again, approved with a real click after the pause and a note.
	session.ask("q2", "exec")
	p.wait("the second dialog", hasDialog)
	p.wait("Approve to be ready", `!document.getElementById('confirm-approve').hasAttribute('aria-disabled')`)
	p.do(chromedp.Focus("#confirm-note"), chromedp.KeyEvent("my own folder"), chromedp.Click("#confirm-approve"))
	p.wait("the dialog to close", noDialog)
	p.wait("the approval in the actions", `Array.from(document.querySelectorAll('.t-summary')).some(e => e.textContent === 'it ran (my own folder)')`)

	// Pause and resume reach the session and come back as its states.
	p.do(chromedp.Click("#pause"))
	p.wait("Paused", `document.getElementById('state').textContent === 'Paused' && document.getElementById('pause').textContent === 'Resume'`)
	p.do(chromedp.Click("#pause"))
	p.wait("Working", `document.getElementById('state').textContent === 'Working'`)

	p.shot("viewer-session")

	// Kill.
	p.do(chromedp.Click("#kill"))
	res := h.waitKilled()
	if res.Source != killswitch.SourceButton && res.Source != killswitch.SourceAPI {
		t.Errorf("the kill: %+v", res)
	}
	h.hub.Send(viewer.State{State: viewer.StateKilled, Reason: "you pressed the button"})
	p.wait("the killed overlay", `document.getElementById('overlay-title').textContent === 'Session killed'`)
	p.clean()

	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.chats) != 1 || len(session.pending) != 0 {
		t.Errorf("the session heard %q and has %v open", session.chats, session.pending)
	}
}
