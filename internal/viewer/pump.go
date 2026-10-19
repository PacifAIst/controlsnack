// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// Caller is the host's end of the agent protocol (agentclient.Client).
type Caller interface {
	Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error)
}

// Sink is where the pump puts what the page is shown: the Hub.
type Sink interface {
	Send(m Message)
	SendFrame(seq uint32, w, h int, webp []byte) error
	Stats() HubStats
}

// Clock is the pump's time. The real one sleeps; the tests' one moves a pretend clock.
type Clock interface {
	Now() time.Time
	// Wait returns after d, or as soon as wake is signaled (the pump then looks at the time again), and reports false when
	// the context has ended.
	Wait(ctx context.Context, d time.Duration, wake <-chan struct{}) bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Wait(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
	case <-t.C:
	}
	return ctx.Err() == nil
}

// The rates of PLAN.md section 10.3.
const (
	DefaultQuality     = 70
	DefaultActiveFPS   = 5
	DefaultIdleFPS     = 2
	DefaultActiveFor   = 3 * time.Second
	DefaultStatsEvery  = 2 * time.Second
	DefaultCursorEvery = 100 * time.Millisecond // at most ten cursor messages a second
	maxBackoff         = 2 * time.Second
)

// PumpOptions configure a Pump.
type PumpOptions struct {
	// Agent takes the screenshots (agentclient.Client).
	Agent Caller
	// Sink receives the frames, the cursor and the stats (the Hub).
	Sink Sink
	// Quality is the WebP quality (default 70).
	Quality int
	// ActiveFPS is the rate during an action and for ActiveFor after it (default 5); IdleFPS the rate otherwise (default 2).
	ActiveFPS, IdleFPS float64
	ActiveFor          time.Duration
	// StatsEvery is the time between stats messages (default 2 s).
	StatsEvery time.Duration
	// CursorEvery is the least time between two cursor messages (default 100 ms).
	CursorEvery time.Duration
	// OnError is told about each screenshot that failed, with the number of failures in a row; the pump goes on, slower.
	OnError func(err error, inARow int)
	// Clock is the time (default the real one).
	Clock Clock
}

// Pump takes screenshots of the guest while a page is connected and sends the ones that changed. It never holds up the
// session: it runs on its own goroutine, and what the session tells it (activity, figures) are atomic stores.
type Pump struct {
	o PumpOptions

	activeUntil atomic.Int64 // unix nanoseconds
	llmMS       atomic.Int64
	actions     atomic.Int64
	wake        chan struct{}

	mu     sync.Mutex
	cancel context.CancelFunc // of the loop of the page that is connected; nil when none is
	done   chan struct{}      // closed when the latest loop has ended, which may be after its page went

	// state of the loop; only its goroutine uses these (one loop at a time: a new one starts when the one before has ended)
	seq        uint32
	failures   int
	lastHash   string
	lastCursor protocol.Point
	haveCursor bool
	cursorAt   time.Time

	frames      int64
	frameBytes  int
	statsAt     time.Time
	statsFrames int64
	statsBytes  int64
}

// NewPump makes a Pump with the defaults filled in. Its OnConnect and OnDisconnect are what the Hub calls.
func NewPump(o PumpOptions) (*Pump, error) {
	if o.Agent == nil || o.Sink == nil {
		return nil, fmt.Errorf("viewer: the pump needs an agent and a sink")
	}
	if o.Quality <= 0 {
		o.Quality = DefaultQuality
	}
	if o.ActiveFPS <= 0 {
		o.ActiveFPS = DefaultActiveFPS
	}
	if o.IdleFPS <= 0 {
		o.IdleFPS = DefaultIdleFPS
	}
	if o.ActiveFor <= 0 {
		o.ActiveFor = DefaultActiveFor
	}
	if o.StatsEvery <= 0 {
		o.StatsEvery = DefaultStatsEvery
	}
	if o.CursorEvery <= 0 {
		o.CursorEvery = DefaultCursorEvery
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	return &Pump{o: o, wake: make(chan struct{}, 1)}, nil
}

// Activity says that an action is running or has just run: the pump looks at the screen at the active rate for the next
// ActiveFor, and at once.
func (p *Pump) Activity() {
	p.activeUntil.Store(p.o.Clock.Now().Add(p.o.ActiveFor).UnixNano())
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// NoteAction counts an action for the stats and marks activity.
func (p *Pump) NoteAction() {
	p.actions.Add(1)
	p.Activity()
}

// NoteLLM records how long the model took to answer, for the stats.
func (p *Pump) NoteLLM(d time.Duration) { p.llmMS.Store(d.Milliseconds()) }

// OnConnect starts the loop when a page connects (HubOptions.OnConnect). If the loop of the page before is still finishing a
// look at the screen, the new loop waits for it in its own goroutine: two loops never run at once, and the page is not held up.
func (p *Pump) OnConnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	before, done := p.done, make(chan struct{})
	p.cancel, p.done = cancel, done
	go func() {
		defer close(done)
		if before != nil {
			<-before
		}
		p.run(ctx)
	}()
}

// OnDisconnect tells the loop to stop (HubOptions.OnDisconnect). It does not wait for a look at the screen that is in progress,
// which can take seconds on a slow guest: the Hub keeps the page's place until this returns, and a window that reloads is
// refused as long as the place is held. Stop waits.
func (p *Pump) OnDisconnect() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Stop stops the loop and waits until it has ended, for the end of a session and for tests.
func (p *Pump) Stop() {
	p.OnDisconnect()
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done != nil {
		<-done
	}
}

// interval is the time between two looks at the screen at a given moment: the active or the idle rate, and slower while
// the agent does not answer.
func (p *Pump) interval(now time.Time) time.Duration {
	fps := p.o.IdleFPS
	if now.UnixNano() < p.activeUntil.Load() {
		fps = p.o.ActiveFPS
	}
	d := time.Duration(float64(time.Second) / fps)
	if p.failures > 0 {
		d = min(maxBackoff, d<<min(p.failures, 6))
	}
	return d
}

func (p *Pump) run(ctx context.Context) {
	clk := p.o.Clock
	now := clk.Now()
	p.statsAt, p.statsFrames, p.statsBytes = now, p.frames, p.o.Sink.Stats().BytesSent
	p.failures = 0
	var lastStart time.Time // the first look is at once
	next := now
	for {
		if !clk.Wait(ctx, next.Sub(clk.Now()), p.wake) {
			return
		}
		now = clk.Now()
		// Activity changes the rate: the next look is never later than one interval after the last.
		if due := lastStart.Add(p.interval(now)); due.Before(next) {
			next = due
		}
		if now.Before(next) {
			continue
		}
		lastStart = now
		if err := p.look(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			p.failures++
			if p.o.OnError != nil {
				p.o.OnError(err, p.failures)
			}
		} else {
			p.failures = 0
		}
		now = clk.Now()
		if !now.Before(p.statsAt.Add(p.o.StatsEvery)) {
			p.sendStats(now)
		}
		next = lastStart.Add(p.interval(now)) // from the start of the look: a slow one makes the next come at once, never a burst
	}
}

// look takes one screenshot and sends what changed.
func (p *Pump) look(ctx context.Context) error {
	req := protocol.ScreenshotReq{Format: string(imaging.WebP), Quality: p.o.Quality, IfChangedSince: p.lastHash}
	var res protocol.ScreenshotRes
	blob, err := p.o.Agent.Call(ctx, protocol.TypeScreenshot, req, nil, &res)
	if err != nil {
		return fmt.Errorf("taking a screenshot for the viewer: %w", err)
	}
	if !res.Unchanged {
		if err := imaging.Inspect(blob, imaging.WebP, res.W, res.H); err != nil {
			return fmt.Errorf("the picture of the guest is not one the viewer will show: %w", err)
		}
		p.seq++
		if err := p.o.Sink.SendFrame(p.seq, res.W, res.H, blob); err != nil {
			return err
		}
		p.lastHash = res.Hash
		p.frames++
		p.frameBytes = len(blob)
	}
	p.sendCursor(res.Cursor)
	return nil
}

// sendCursor sends the cursor when it moved, at most CursorEvery apart; a move that has to wait is sent by the next look.
func (p *Pump) sendCursor(c protocol.Point) {
	if p.haveCursor && c == p.lastCursor {
		return
	}
	now := p.o.Clock.Now()
	if p.haveCursor && now.Sub(p.cursorAt) < p.o.CursorEvery && !now.Before(p.cursorAt) {
		return // the next look sends it: it is compared with the last one that was sent
	}
	p.o.Sink.Send(Cursor{X: c.X, Y: c.Y})
	p.lastCursor, p.haveCursor, p.cursorAt = c, true, now
}

func (p *Pump) sendStats(now time.Time) {
	st := p.o.Sink.Stats()
	secs := now.Sub(p.statsAt).Seconds()
	if secs <= 0 {
		return
	}
	round := func(x float64) float64 { return math.Round(x*10) / 10 }
	p.o.Sink.Send(Stats{
		KBps:       round(float64(st.BytesSent-p.statsBytes) / 1024 / secs),
		FPS:        round(float64(p.frames-p.statsFrames) / secs),
		FrameBytes: p.frameBytes,
		LLMMS:      p.llmMS.Load(),
		Actions:    int(p.actions.Load()),
	})
	p.statsAt, p.statsFrames, p.statsBytes = now, p.frames, st.BytesSent
}
