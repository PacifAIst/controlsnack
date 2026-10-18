// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
)

type nobody struct{}

func (nobody) Chat(string)                  {}
func (nobody) Confirm(string, bool, string) {}
func (nobody) Control(string)               {}

func newTestHub(t *testing.T, mod ...func(*HubOptions)) *Hub {
	t.Helper()
	o := HubOptions{CSRF: func() string { return "c" }, Hello: func() Hello { return Hello{} }, Inbound: nobody{}, Killer: killswitch.New(killswitch.Options{})}
	for _, m := range mod {
		m(&o)
	}
	h, err := NewHub(o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// newTestPeer is a page with no connection behind it, to look at what is queued for it.
func newTestPeer(h *Hub) *peer {
	ctx, stop := context.WithCancelCause(context.Background())
	return &peer{hub: h, ctx: ctx, stop: stop, wake: make(wakeFlag, 1)}
}

// drain takes everything that is ready, in the order next gives it.
func drain(p *peer) (out []string) {
	for {
		p.mu.Lock()
		empty := len(p.queue) == 0 && p.cursor == nil && p.stats == nil && p.frame == nil
		p.mu.Unlock()
		if empty {
			return out
		}
		b, binary, _ := p.next()
		if binary {
			out = append(out, "frame:"+string(b))
		} else {
			out = append(out, string(b))
		}
	}
}

func TestOrderedMessagesKeepTheirOrderAndComeBeforeTheNewestOfTheRest(t *testing.T) {
	h := newTestHub(t)
	p := newTestPeer(h)
	p.pushFrame([]byte("f1"))
	p.push(classLatest, "stats", []byte("s1"))
	p.push(classEssential, "state", []byte("e1"))
	p.push(classLatest, "cursor", []byte("c1"))
	p.push(classDroppable, "audit", []byte("d1"))
	p.push(classEssential, "chat", []byte("e2"))
	got := drain(p)
	want := []string{"e1", "d1", "e2", "c1", "s1", "frame:f1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestOnlyTheNewestCursorStatsAndFrameAreKept(t *testing.T) {
	h := newTestHub(t)
	p := newTestPeer(h)
	for i := range 100 {
		p.push(classLatest, "cursor", []byte(fmt.Sprint("c", i)))
		p.push(classLatest, "stats", []byte(fmt.Sprint("s", i)))
		p.pushFrame([]byte(fmt.Sprint("f", i)))
	}
	got := drain(p)
	if fmt.Sprint(got) != "[c99 s99 frame:f99]" {
		t.Errorf("%v", got)
	}
	if st := h.Stats(); st.FramesDropped != 99 {
		t.Errorf("%d frames dropped", st.FramesDropped)
	}
}

func TestDroppableMessagesAreLeftOutWhenThePageFallsBehind(t *testing.T) {
	h := newTestHub(t, func(o *HubOptions) { o.Queue = 8 })
	p := newTestPeer(h)
	for i := range 20 {
		p.push(classDroppable, "audit", []byte(fmt.Sprint("d", i)))
	}
	if got := drain(p); len(got) != 4 || got[0] != "d0" || got[3] != "d3" {
		t.Errorf("kept %v", got)
	}
	if st := h.Stats(); st.Dropped != 16 {
		t.Errorf("%d dropped", st.Dropped)
	}
	// What matters is still taken while the droppable ones are being left out.
	for i := range 4 {
		p.push(classDroppable, "audit", []byte("d"))
		_ = i
	}
	p.push(classEssential, "state", []byte("e"))
	p.push(classDroppable, "audit", []byte("late"))
	got := drain(p)
	if fmt.Sprint(got) != "[d d d d e]" {
		t.Errorf("%v", got)
	}
	if p.ctx.Err() != nil {
		t.Error("the page was cut off for droppable messages")
	}
}

func TestAPageThatFallsTooFarBehindOnWhatMattersIsCutOff(t *testing.T) {
	h := newTestHub(t, func(o *HubOptions) { o.Queue = 4 })
	p := newTestPeer(h)
	for i := range 4 {
		p.push(classEssential, "chat", []byte(fmt.Sprint("e", i)))
	}
	if p.ctx.Err() != nil {
		t.Fatal("cut off at the limit")
	}
	p.push(classEssential, "chat", []byte("e4"))
	if !errors.Is(context.Cause(p.ctx), errOverflow) || h.Stats().Overflows != 1 {
		t.Errorf("cause %v, overflows %d", context.Cause(p.ctx), h.Stats().Overflows)
	}
	if got := drain(p); len(got) != 4 {
		t.Errorf("kept %v", got)
	}
	if !abrupt(context.Cause(p.ctx)) {
		t.Error("a page that does not keep up is closed with a handshake it will not answer")
	}
}

func TestNextWaitsForSomethingAndEndsWithTheConnection(t *testing.T) {
	h := newTestHub(t)
	p := newTestPeer(h)
	got := make(chan string, 1)
	go func() {
		b, _, ok := p.next()
		if !ok {
			got <- "over"
			return
		}
		got <- string(b)
	}()
	select {
	case v := <-got:
		t.Fatalf("returned %q with nothing to give", v)
	case <-time.After(50 * time.Millisecond):
	}
	p.push(classEssential, "chat", []byte("hello"))
	if v := <-got; v != "hello" {
		t.Errorf("got %q", v)
	}
	go func() {
		_, _, ok := p.next()
		if !ok {
			got <- "over"
		}
	}()
	time.Sleep(20 * time.Millisecond)
	p.stop(errors.New("gone"))
	select {
	case v := <-got:
		if v != "over" {
			t.Errorf("got %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("next did not return when the connection ended")
	}
}

func TestTheRingKeepsTheNewestByCountAndBySize(t *testing.T) {
	r := newRing(3, 100)
	for i := range 5 {
		r.add([]byte(fmt.Sprint(i)))
	}
	if len(r.items) != 3 || string(r.items[0]) != "2" || string(r.items[2]) != "4" || r.bytes != 3 {
		t.Errorf("%q %d", r.items, r.bytes)
	}
	r = newRing(100, 10)
	for range 5 {
		r.add([]byte("abcd")) // 4 bytes each: two fit in 10
	}
	if len(r.items) != 2 || r.bytes != 8 {
		t.Errorf("%d items, %d bytes", len(r.items), r.bytes)
	}
	r.add(make([]byte, 50)) // one larger than the whole limit is still kept, alone
	if len(r.items) != 1 || r.bytes != 50 {
		t.Errorf("%d items, %d bytes", len(r.items), r.bytes)
	}
}

func TestNewHubWantsEverythingItNeeds(t *testing.T) {
	good := func() HubOptions {
		return HubOptions{CSRF: func() string { return "c" }, Hello: func() Hello { return Hello{} }, Inbound: nobody{}, Killer: killswitch.New(killswitch.Options{})}
	}
	for name, mod := range map[string]func(*HubOptions){
		"no CSRF": func(o *HubOptions) { o.CSRF = nil }, "no hello": func(o *HubOptions) { o.Hello = nil },
		"no inbound": func(o *HubOptions) { o.Inbound = nil }, "no killer": func(o *HubOptions) { o.Killer = nil },
	} {
		o := good()
		mod(&o)
		if _, err := NewHub(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	h, err := NewHub(good())
	if err != nil {
		t.Fatal(err)
	}
	if h.opts.Rate != 10 || h.opts.Burst != 10 || h.opts.Queue != 256 || h.opts.History != 200 || h.opts.WriteTimeout != 10*time.Second || h.opts.PingEvery != 5*time.Second || h.opts.PingTimeout != 5*time.Second {
		t.Errorf("defaults: %+v", h.opts)
	}
	if h.Connected() {
		t.Error("connected with nobody")
	}
	if err := h.SendFrame(1, 0, 0, nil); err == nil {
		t.Error("an empty frame was sent")
	}
}

func TestSendWithNobodyThereKeepsWhatALaterPageNeeds(t *testing.T) {
	h := newTestHub(t)
	h.Send(ConfirmRequest{ID: "q1"})
	h.Send(ConfirmRequest{ID: "q1"}) // the same again does not make it twice
	h.Send(ConfirmRequest{ID: "q2"})
	h.Send(ConfirmResolved{ID: "q1", Outcome: OutcomeExpired})
	h.Send(ConfirmResolved{ID: "never-asked", Outcome: OutcomeExpired})
	h.Send(Ended{Reason: "done"})
	texts, frame := h.snapshot()
	if frame != nil {
		t.Error("a frame from nowhere")
	}
	if len(texts) != 2 || string(texts[0])[:25] != `{"type":"confirm_request"` || string(texts[1])[:14] != `{"type":"ended` {
		t.Errorf("%q", texts)
	}
}

func TestAResolvedConfirmationIsForgottenAndItsIdCanComeAgain(t *testing.T) {
	h := newTestHub(t)
	h.Send(ConfirmRequest{ID: "q1"})
	h.Send(ConfirmResolved{ID: "q1", Outcome: OutcomeApproved})
	if len(h.pending) != 0 || len(h.order) != 0 {
		t.Errorf("still held: %d pending, %v in order", len(h.pending), h.order)
	}
	h.Send(ConfirmRequest{ID: "q2"})
	h.Send(ConfirmRequest{ID: "q1"}) // an id that is used again is a new request
	texts, _ := h.snapshot()
	if len(texts) != 2 || string(texts[0]) != string(mustEncode(t, ConfirmRequest{ID: "q2"})) || string(texts[1]) != string(mustEncode(t, ConfirmRequest{ID: "q1"})) {
		t.Errorf("%q", texts)
	}
}

func mustEncode(t *testing.T, m Message) []byte {
	t.Helper()
	b, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
