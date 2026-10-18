// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
)

// Inbound receives what the person does in the page, after it has been checked. The methods run on the goroutine that
// reads the connection, so they must not block: the session queues the request and returns.
type Inbound interface {
	// Chat is a message to the model.
	Chat(text string)
	// Confirm is the answer to a confirmation request. The id may be one the session does not know (a stale page, or a
	// request that has already ended); the session ignores it.
	Confirm(id string, approve bool, note string)
	// Control is pause, resume, stop, step_on or step_off. Kill does not come here: the hub fires the kill switch itself.
	Control(op string)
}

// HubOptions configure a Hub.
type HubOptions struct {
	// CSRF returns the value the page must send with POST /api/kill (Server.CSRF); the hub puts it in the hello message.
	CSRF func() string
	// Hello returns the facts of the session when a page connects.
	Hello func() Hello
	// Inbound receives the person's messages.
	Inbound Inbound
	// Killer is the kill switch that a kill control fires.
	Killer *killswitch.Killer
	// OnConnect and OnDisconnect are called when a page connects and when it is gone (the frame pump and the dead-man
	// timer hang on these). They run on the connection's goroutine.
	OnConnect, OnDisconnect func()
	// Rate and Burst limit the messages from the page, other than kill (default 10 a second, burst 10).
	Rate  float64
	Burst int
	// Queue is how many ordered messages can wait for a page that does not keep up before it is cut off (default 256).
	Queue int
	// History is how many chat, trace and notice messages, and how many audit lines, are kept for a page that connects
	// later (default 200 of each).
	History int
	// WriteTimeout bounds each write to the page (default 10 s).
	WriteTimeout time.Duration
	// PingEvery is the time between pings that find a page that has vanished (default 5 s), and PingTimeout how long the pong
	// may take (default 5 s): a window that closes is noticed at once, one that vanishes without closing in about ten seconds.
	PingEvery, PingTimeout time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// HubStats count what the hub did, for the stats message and the tests.
type HubStats struct {
	Connections   int64 // pages that connected
	FramesSent    int64
	FramesDropped int64 // frames replaced by a newer one before they were sent
	Dropped       int64 // droppable messages (audit lines, plans, pongs) left out for a page that was behind
	Overflows     int64 // pages cut off because essential messages piled up
	Invalid       int64 // messages of the page that broke the rules
	Unknown       int64 // messages of the page with a type that is not one
	Limited       int64 // messages of the page left out by the rate limit
	BytesSent     int64 // bytes written to the page, of every message and frame
}

// Hub is the connection to the page: it takes the session's messages and frames, never blocks the session whatever the
// page does, and hands the page's messages to the session.
type Hub struct {
	opts HubOptions

	mu      sync.Mutex
	cur     *peer
	state   []byte
	agent   []byte
	ended   []byte
	cursor  []byte
	frame   []byte
	hist    *ring
	audit   *ring
	pending map[string][]byte // confirm_request by id, until it is resolved
	order   []string          // the ids in the order they came

	stats struct {
		connections, framesSent, framesDropped, dropped, overflows, invalid, unknown, limited, bytes atomic.Int64
	}
}

// NewHub checks the options and makes a Hub. Its ServeHTTP is the handler for Options.WebSocket of the Server.
func NewHub(o HubOptions) (*Hub, error) {
	switch {
	case o.CSRF == nil:
		return nil, errors.New("viewer: the hub needs the CSRF value")
	case o.Hello == nil:
		return nil, errors.New("viewer: the hub needs the hello message")
	case o.Inbound == nil:
		return nil, errors.New("viewer: the hub needs a receiver for what the person does")
	case o.Killer == nil:
		return nil, errors.New("viewer: the hub needs the kill switch")
	}
	if o.Rate <= 0 {
		o.Rate = 10
	}
	if o.Burst <= 0 {
		o.Burst = 10
	}
	if o.Queue <= 0 {
		o.Queue = 256
	}
	if o.History <= 0 {
		o.History = 200
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.PingEvery <= 0 {
		o.PingEvery = 5 * time.Second
	}
	if o.PingTimeout <= 0 {
		o.PingTimeout = 5 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Hub{
		opts: o, hist: newRing(o.History, replayBytes), audit: newRing(o.History, replayBytes), pending: map[string][]byte{},
	}, nil
}

// replayBytes bounds what is kept of each ring for a page that connects later.
const replayBytes = 2 << 20

// Stats returns the counters.
func (h *Hub) Stats() HubStats {
	return HubStats{
		Connections: h.stats.connections.Load(), FramesSent: h.stats.framesSent.Load(), FramesDropped: h.stats.framesDropped.Load(),
		Dropped: h.stats.dropped.Load(), Overflows: h.stats.overflows.Load(), Invalid: h.stats.invalid.Load(),
		Unknown: h.stats.unknown.Load(), Limited: h.stats.limited.Load(), BytesSent: h.stats.bytes.Load(),
	}
}

// Connected reports whether a page is connected.
func (h *Hub) Connected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur != nil
}

// class says what becomes of a message when the page cannot take it at once.
type class int

const (
	classEssential class = iota // kept in order; a page that falls too far behind is cut off
	classDroppable              // kept in order while the page is not far behind, else left out
	classLatest                 // only the newest of its kind is kept
)

func classOf(m Message) class {
	switch m.(type) {
	case Audit, Plan, Pong:
		return classDroppable
	case Cursor, Stats:
		return classLatest
	}
	return classEssential
}

// Send hands a message to the page. It never blocks. A message too large to send is replaced by a notice. With no page
// connected, what a page connecting later should see is kept: the state, the conversation and trace, the audit tail,
// the confirmations that are still open, the last cursor.
func (h *Hub) Send(m Message) {
	b, err := Encode(m)
	if err != nil {
		b, _ = Encode(Notice{Level: LevelWarn, Text: "A message of type " + m.MessageType() + " was too large to show."})
		m = Notice{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch v := m.(type) {
	case Agent:
		h.agent = b
	case State:
		h.state = b
	case Ended:
		h.ended = b
	case Cursor:
		h.cursor = b
	case Chat, Trace, Notice:
		h.hist.add(b)
	case Audit:
		h.audit.add(b)
	case ConfirmRequest:
		if _, ok := h.pending[v.ID]; !ok {
			h.order = append(h.order, v.ID)
		}
		h.pending[v.ID] = b
	case ConfirmResolved:
		delete(h.pending, v.ID)
		for i, id := range h.order {
			if id == v.ID {
				h.order = append(h.order[:i], h.order[i+1:]...)
				break
			}
		}
	}
	if h.cur != nil {
		h.cur.push(classOf(m), m.MessageType(), b)
	}
}

// SendFrame hands a screenshot to the page. It never blocks; a frame that has not been sent when the next one comes is
// dropped, so a page that is behind sees the newest picture and not a queue of old ones.
func (h *Hub) SendFrame(seq uint32, w, ht int, webp []byte) error {
	b, err := EncodeFrame(seq, w, ht, webp)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frame = b
	if h.cur != nil {
		h.cur.pushFrame(b)
	}
	return nil
}

// snapshot is what a page that connects is shown first, in this order.
func (h *Hub) snapshot() (texts [][]byte, frame []byte) {
	add := func(b []byte) {
		if b != nil {
			texts = append(texts, b)
		}
	}
	add(h.state)
	add(h.agent)
	texts = append(texts, h.audit.items...)
	texts = append(texts, h.hist.items...)
	for _, id := range h.order {
		add(h.pending[id])
	}
	add(h.cursor)
	add(h.ended)
	return texts, h.frame
}

// ring keeps the newest messages, by count and by size.
type ring struct {
	max      int
	maxBytes int
	items    [][]byte
	bytes    int
}

func newRing(max, maxBytes int) *ring { return &ring{max: max, maxBytes: maxBytes} }

func (r *ring) add(b []byte) {
	r.items = append(r.items, b)
	r.bytes += len(b)
	drop := 0
	for len(r.items)-drop > r.max || (r.bytes > r.maxBytes && len(r.items)-drop > 1) {
		r.bytes -= len(r.items[drop])
		drop++
	}
	if drop > 0 {
		r.items = append([][]byte(nil), r.items[drop:]...)
	}
}

// attach makes p the page that is connected. What it must be shown first (the hello message and the snapshot) is put
// in front of everything that comes after, with no gap and no repetition, because both happen under the lock that Send
// takes.
func (h *Hub) attach(p *peer, hello []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cur != nil {
		h.cur.stop(errReplaced)
	}
	texts, frame := h.snapshot()
	p.initial = append([][]byte{hello}, texts...)
	p.initialFrame = frame
	h.cur = p
	h.stats.connections.Add(1)
}

func (h *Hub) detach(p *peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cur == p {
		h.cur = nil
	}
}

// wakeFlag is a channel of room for one signal.
type wakeFlag chan struct{}

func (w wakeFlag) signal() {
	select {
	case w <- struct{}{}:
	default:
	}
}
