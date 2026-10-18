// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
)

// The reasons a connection is ended by the host, each with the status the page is told.
var (
	errReplaced = errors.New("replaced by another window")
	errOverflow = errors.New("the page does not keep up")
	errBinary   = errors.New("the page sent a binary message")
	errAbuse    = errors.New("too many messages that break the rules")
	errPing     = errors.New("the page does not answer pings")
	errStalled  = errors.New("the page does not take what is written to it")
)

// abrupt says whether a connection is dropped without the closing handshake: a page that does not take what is written
// to it would not answer a close frame either, and waiting for it would hold the session's slot.
func abrupt(cause error) bool {
	return errors.Is(cause, errOverflow) || errors.Is(cause, errStalled) || errors.Is(cause, errPing)
}

func closeStatus(cause error) (websocket.StatusCode, string) {
	switch {
	case errors.Is(cause, errBinary):
		return websocket.StatusUnsupportedData, "text messages only"
	case errors.Is(cause, errAbuse):
		return websocket.StatusPolicyViolation, "too many messages that break the rules"
	case errors.Is(cause, errReplaced), errors.Is(cause, context.Canceled):
		return websocket.StatusGoingAway, "going away"
	}
	return websocket.StatusNormalClosure, ""
}

// maxInvalid is how many messages that break the rules a connection may send before it is cut off.
const maxInvalid = 50

// peer is the page that is connected.
type peer struct {
	hub  *Hub
	ws   *websocket.Conn
	ctx  context.Context
	stop context.CancelCauseFunc

	initial      [][]byte // the hello message and the snapshot, written before anything else
	initialFrame []byte

	mu     sync.Mutex
	queue  [][]byte // ordered messages that wait to be written
	cursor []byte   // the newest cursor, if not yet written
	stats  []byte   // the newest stats, if not yet written
	frame  []byte   // the newest frame, if not yet written
	wake   wakeFlag
}

// push puts a message in the page's queue. It never blocks.
func (p *peer) push(c class, kind string, b []byte) {
	p.mu.Lock()
	switch c {
	case classLatest:
		if kind == "cursor" {
			p.cursor = b
		} else {
			p.stats = b
		}
	case classDroppable:
		if len(p.queue) >= p.hub.opts.Queue/2 {
			p.mu.Unlock()
			p.hub.stats.dropped.Add(1)
			return
		}
		p.queue = append(p.queue, b)
	default:
		if len(p.queue) >= p.hub.opts.Queue {
			p.mu.Unlock()
			p.hub.stats.overflows.Add(1)
			p.stop(errOverflow)
			return
		}
		p.queue = append(p.queue, b)
	}
	p.mu.Unlock()
	p.wake.signal()
}

func (p *peer) pushFrame(b []byte) {
	p.mu.Lock()
	if p.frame != nil {
		p.hub.stats.framesDropped.Add(1)
	}
	p.frame = b
	p.mu.Unlock()
	p.wake.signal()
}

// next takes the next thing to write: the ordered messages first, then the newest cursor, stats and frame. It waits
// when there is nothing, and reports false when the connection is over.
func (p *peer) next() (b []byte, binary, ok bool) {
	for {
		p.mu.Lock()
		switch {
		case len(p.queue) > 0:
			b, p.queue = p.queue[0], p.queue[1:]
		case p.cursor != nil:
			b, p.cursor = p.cursor, nil
		case p.stats != nil:
			b, p.stats = p.stats, nil
		case p.frame != nil:
			b, p.frame, binary = p.frame, nil, true
		}
		p.mu.Unlock()
		if b != nil {
			return b, binary, true
		}
		select {
		case <-p.wake:
		case <-p.ctx.Done():
			return nil, false, false
		}
	}
}

func (p *peer) write(typ websocket.MessageType, b []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), p.hub.opts.WriteTimeout)
	defer cancel()
	if err := p.ws.Write(ctx, typ, b); err != nil {
		p.stop(fmt.Errorf("%w: %w", errStalled, err))
		return false
	}
	p.hub.stats.bytes.Add(int64(len(b)))
	return true
}

func (p *peer) writeLoop() {
	for _, b := range p.initial {
		if !p.write(websocket.MessageText, b) {
			return
		}
	}
	if p.initialFrame != nil && !p.write(websocket.MessageBinary, p.initialFrame) {
		return
	}
	for {
		b, binary, ok := p.next()
		if !ok {
			return
		}
		typ := websocket.MessageText
		if binary {
			typ = websocket.MessageBinary
		}
		if !p.write(typ, b) {
			return
		}
		if binary {
			p.hub.stats.framesSent.Add(1)
		}
	}
}

// pingLoop finds a page that has vanished without closing: a page that reads answers a ping at once.
func (p *peer) pingLoop() {
	t := time.NewTicker(p.hub.opts.PingEvery)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), p.hub.opts.PingTimeout)
			err := p.ws.Ping(ctx)
			cancel()
			if err != nil {
				p.stop(errPing)
				return
			}
		}
	}
}

// readLoop reads what the person sends, checks it, and passes it on, until the connection ends.
func (p *peer) readLoop() {
	h := p.hub
	lim := rate.NewLimiter(rate.Limit(h.opts.Rate), h.opts.Burst)
	var invalid int
	var lastNotice time.Time
	for {
		typ, data, err := p.ws.Read(context.Background())
		if err != nil {
			p.stop(err)
			return
		}
		if typ != websocket.MessageText {
			p.stop(errBinary)
			return
		}
		msg, err := ParseClient(data)
		switch {
		case errors.Is(err, ErrUnknownType):
			h.stats.unknown.Add(1) // a type this host does not take is dropped
			continue
		case err != nil:
			h.stats.invalid.Add(1)
			if invalid++; invalid > maxInvalid {
				p.stop(errAbuse)
				return
			}
			continue
		}
		if c, ok := msg.(ControlIn); !ok || c.Op != OpKill { // the kill is never held back by a flood of chat
			if !lim.Allow() {
				h.stats.limited.Add(1)
				if now := h.opts.Now(); now.Sub(lastNotice) > 5*time.Second || now.Before(lastNotice) {
					lastNotice = now
					if b, e := Encode(Notice{Level: LevelWarn, Text: "Too many messages at once; some were left out."}); e == nil {
						p.push(classDroppable, "notice", b)
					}
				}
				continue
			}
		}
		p.dispatch(msg)
	}
}

func (p *peer) dispatch(msg any) {
	h := p.hub
	switch m := msg.(type) {
	case ChatIn:
		h.opts.Inbound.Chat(m.Text)
	case ConfirmIn:
		h.opts.Inbound.Confirm(m.ID, m.Approve, m.Note)
	case ControlIn:
		if m.Op == OpKill {
			src := killswitch.SourceButton
			if m.Src == SrcHotkey {
				src = killswitch.SourceHotkey
			}
			h.opts.Killer.TriggerAsync(src, "kill from the viewer")
			return
		}
		h.opts.Inbound.Control(m.Op)
	case PingIn:
		if b, err := Encode(Pong(m)); err == nil {
			p.push(classDroppable, "pong", b)
		}
	}
}

// ServeHTTP takes the one WebSocket of the viewer. The Server has already checked the cookie and the origin and made
// sure that no other connection is open.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil { // Accept has answered the request
		return
	}
	ws.SetReadLimit(MaxClientBytes)
	hello := h.opts.Hello()
	hello.Proto, hello.CSRF = ProtoVersion, h.opts.CSRF()
	hello.Now = h.opts.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	hb, err := Encode(hello)
	if err != nil {
		_ = ws.Close(websocket.StatusInternalError, "no hello")
		return
	}
	ctx, stop := context.WithCancelCause(r.Context())
	defer stop(nil)
	p := &peer{hub: h, ws: ws, ctx: ctx, stop: stop, wake: make(wakeFlag, 1)}
	h.attach(p, hb)
	if h.opts.OnConnect != nil {
		h.opts.OnConnect()
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); p.writeLoop() }()
	go func() { defer wg.Done(); p.pingLoop() }()
	go func() { defer wg.Done(); p.closer() }()

	p.readLoop()
	stop(nil) // the cause, if there is one, is already set
	wg.Wait()
	if h.opts.OnDisconnect != nil {
		h.opts.OnDisconnect()
	}
}

// closer ends the connection when the context ends: with the closing handshake and a status the page can read, or at
// once when the page is stalled. The reads and writes do not use that context, because the library drops a connection
// without a close frame when the context of a call ends.
func (p *peer) closer() {
	<-p.ctx.Done()
	p.hub.detach(p)
	cause := context.Cause(p.ctx)
	if abrupt(cause) {
		_ = p.ws.CloseNow()
		return
	}
	code, reason := closeStatus(cause)
	_ = p.ws.Close(code, reason)
}
