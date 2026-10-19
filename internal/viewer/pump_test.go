// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

// fakeClock is a clock the pump moves by waiting: Wait adds the time asked for and returns at once, so a minute of the
// pump's life takes no time. It ends the pump when the pretend time reaches end.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	start time.Time
	end   time.Time
	// beforeWait, when set, is called with the time the pump is about to wait, as if something happened during the wait.
	beforeWait func(d time.Duration)
}

func newFakeClock(d time.Duration) *fakeClock {
	t := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	return &fakeClock{now: t, start: t, end: t.Add(d)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Since() time.Duration { return c.Now().Sub(c.start) }

func (c *fakeClock) Wait(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	if c.beforeWait != nil {
		c.beforeWait(d)
	}
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-wake:
		return true
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.now = c.now.Add(d)
	}
	return c.now.Before(c.end)
}

// look is one screenshot request the pump made.
type look struct {
	at  time.Duration
	req protocol.ScreenshotReq
}

// fakeAgent answers screenshot requests from a scenario.
type fakeAgent struct {
	clock *fakeClock
	mu    sync.Mutex
	looks []look
	// answer decides what the guest says to the n-th request (from 0) made at a time since the start.
	answer func(n int, at time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error)
}

func (a *fakeAgent) Call(_ context.Context, typ string, req any, _ []byte, res any) ([]byte, error) {
	if typ != protocol.TypeScreenshot {
		return nil, fmt.Errorf("unexpected request %q", typ)
	}
	r := req.(protocol.ScreenshotReq)
	a.mu.Lock()
	n := len(a.looks)
	at := a.clock.Since()
	a.looks = append(a.looks, look{at, r})
	a.mu.Unlock()
	out, blob, err := a.answer(n, at, r)
	if err != nil {
		return nil, err
	}
	*(res.(*protocol.ScreenshotRes)) = out
	return blob, nil
}

func (a *fakeAgent) all() []look {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]look(nil), a.looks...)
}

// fakeSink records what the pump sends.
type fakeSink struct {
	mu      sync.Mutex
	frames  []sentFrame
	cursors []sentCursor
	stats   []viewer.Stats
	bytes   int64
	clock   *fakeClock
	failAt  map[uint32]error // frames the sink refuses
}

type sentFrame struct {
	seq  uint32
	w, h int
	size int
	at   time.Duration
}

type sentCursor struct {
	x, y int
	at   time.Duration
}

func (s *fakeSink) Send(m viewer.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch v := m.(type) {
	case viewer.Cursor:
		s.cursors = append(s.cursors, sentCursor{v.X, v.Y, s.clock.Since()})
		s.bytes += 30
	case viewer.Stats:
		s.stats = append(s.stats, v)
		s.bytes += 90
	}
}

func (s *fakeSink) SendFrame(seq uint32, w, h int, webp []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failAt[seq]; err != nil {
		return err
	}
	s.frames = append(s.frames, sentFrame{seq, w, h, len(webp), s.clock.Since()})
	s.bytes += int64(len(webp)) + 9
	return nil
}

func (s *fakeSink) Stats() viewer.HubStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return viewer.HubStats{BytesSent: s.bytes}
}

// desktop is a picture like the guest's: a wallpaper gradient, a panel, and windows with lines of text-like strokes.
func desktop(w, h, windows int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: uint8(30 + 40*x/w), G: uint8(60 + 60*y/h), B: uint8(120 + 50*(x+y)/(w+h)), A: 255})
		}
	}
	fill := func(r image.Rectangle, c color.RGBA) {
		for y := r.Min.Y; y < r.Max.Y && y < h; y++ {
			for x := r.Min.X; x < r.Max.X && x < w; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	fill(image.Rect(0, 0, w, 28), color.RGBA{R: 40, G: 40, B: 44, A: 255}) // the panel
	for i := range windows {
		x0, y0 := 120+i*140, 90+i*60
		fill(image.Rect(x0, y0, x0+w/2, y0+h/2), color.RGBA{R: 245, G: 245, B: 245, A: 255})
		fill(image.Rect(x0, y0, x0+w/2, y0+30), color.RGBA{R: 70, G: 90, B: 150, A: 255})
		for line := 0; line < (h/2-50)/18; line++ { // lines of text: short dark strokes with gaps
			for stroke := 0; stroke < 60; stroke++ {
				sx := x0 + 16 + stroke*11 + (line*7)%5
				if (stroke+line)%7 == 0 {
					continue
				}
				fill(image.Rect(sx, y0+44+line*18, sx+8, y0+44+line*18+9), color.RGBA{R: 30, G: 30, B: 30, A: 255})
			}
		}
	}
	return img
}

func webp(t testing.TB, img *image.RGBA) []byte {
	t.Helper()
	b, err := imaging.Encode(img, imaging.WebP, 70)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pumpRig is a pump over a fake clock, agent and sink.
type pumpRig struct {
	clock *fakeClock
	agent *fakeAgent
	sink  *fakeSink
	pump  *viewer.Pump
	errs  []string
}

func newPumpRig(t *testing.T, d time.Duration, answer func(n int, at time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error), mod ...func(*viewer.PumpOptions)) *pumpRig {
	t.Helper()
	clk := newFakeClock(d)
	r := &pumpRig{clock: clk, agent: &fakeAgent{clock: clk, answer: answer}, sink: &fakeSink{clock: clk}}
	o := viewer.PumpOptions{Agent: r.agent, Sink: r.sink, Clock: clk, OnError: func(err error, n int) { r.errs = append(r.errs, fmt.Sprintf("%d: %v", n, err)) }}
	for _, m := range mod {
		m(&o)
	}
	p, err := viewer.NewPump(o)
	if err != nil {
		t.Fatal(err)
	}
	r.pump = p
	return r
}

// run lets the pump live until the pretend time is over, then disconnects.
func (r *pumpRig) run(t *testing.T) {
	t.Helper()
	r.pump.OnConnect()
	until(t, "the pretend time to run out", func() bool { return !r.clock.Now().Before(r.clock.end) })
	r.pump.Stop()
}

func (r *pumpRig) looksBetween(from, to time.Duration) int {
	n := 0
	for _, l := range r.agent.all() {
		if l.at >= from && l.at < to {
			n++
		}
	}
	return n
}

// quiet is a guest whose screen never changes, with the cursor in one place.
func quiet(_ int, _ time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
	return protocol.ScreenshotRes{Hash: req.IfChangedSince, Unchanged: true, Cursor: protocol.Point{X: 5, Y: 5}}, nil, nil
}

// slowLook is a guest that takes as long over a screenshot as the test lets it: every look says it has started and waits for release.
func slowLook(started chan<- struct{}, release <-chan struct{}) func(int, time.Duration, protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
	return func(n int, at time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
		select {
		case started <- struct{}{}:
		default: // nobody is listening any more
		}
		<-release
		return quiet(n, at, req)
	}
}

func TestAPageThatGoesIsNotHeldUpByALookAtTheScreenThatIsStillRunning(t *testing.T) {
	started, release := make(chan struct{}, 8), make(chan struct{})
	r := newPumpRig(t, time.Hour, slowLook(started, release))
	r.pump.OnConnect()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the pump made no look")
	}
	// The look cannot be interrupted (a slow guest takes seconds over it). The Hub holds the page's place until OnDisconnect
	// returns, and a window that reloads is refused while it is held, so OnDisconnect must not wait for the look.
	returned := make(chan struct{})
	go func() { r.pump.OnDisconnect(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("OnDisconnect waited for a look at the screen to end")
	}
	close(release)
	r.pump.Stop() // and Stop does wait: it returns only when the loop has ended
}

func TestAPageThatConnectsAgainGetsItsLoopOnlyAfterTheOldLookIsOver(t *testing.T) {
	started, release := make(chan struct{}, 64), make(chan struct{})
	r := newPumpRig(t, time.Hour, slowLook(started, release))
	r.pump.OnConnect()
	<-started
	r.pump.OnDisconnect()
	r.pump.OnConnect() // the same window again, while the first look is still under way
	time.Sleep(150 * time.Millisecond)
	if n := len(r.agent.all()); n != 1 {
		t.Fatalf("%d looks are going on: the new loop did not wait for the old one, and the two share the pump's state", n)
	}
	close(release)
	until(t, "the new loop to look", func() bool { return len(r.agent.all()) >= 2 })
	r.pump.Stop()
}
