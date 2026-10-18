// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

type answerFunc = func(n int, at time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error)

// changing is a guest whose screen takes a new picture every period, with a real WebP of the given size behind it.
func changing(frame []byte, w, h int, period time.Duration) answerFunc {
	return func(_ int, at time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
		hash := fmt.Sprint("h", int64(at/period))
		cur := protocol.Point{X: 5, Y: 5}
		if req.IfChangedSince == hash {
			return protocol.ScreenshotRes{Hash: hash, Unchanged: true, Cursor: cur}, nil, nil
		}
		return protocol.ScreenshotRes{W: w, H: h, Format: "webp", Hash: hash, Cursor: cur}, frame, nil
	}
}

func TestTheIdleRateIsTwoLooksASecond(t *testing.T) {
	r := newPumpRig(t, 10*time.Second, quiet)
	r.run(t)
	looks := r.agent.all()
	if len(looks) != 20 {
		t.Fatalf("%d looks in 10 seconds", len(looks))
	}
	for i, l := range looks {
		if l.at != time.Duration(i)*500*time.Millisecond {
			t.Errorf("look %d at %v", i, l.at)
		}
		if l.req.Format != "webp" || l.req.Quality != 70 || l.req.MaxLongEdge != 0 || l.req.MarksGeneration != "" {
			t.Errorf("request %+v", l.req)
		}
	}
	if len(r.sink.frames) != 0 {
		t.Errorf("%d frames of a screen that did not change", len(r.sink.frames))
	}
	if len(r.sink.cursors) != 1 || r.sink.cursors[0].x != 5 {
		t.Errorf("cursor messages %+v", r.sink.cursors)
	}
	if len(r.sink.stats) != 4 {
		t.Errorf("%d stats messages in 10 seconds", len(r.sink.stats))
	}
}

func TestActivityRaisesTheRateForThreeSeconds(t *testing.T) {
	var r *pumpRig
	fired := false
	r = newPumpRig(t, 10*time.Second, func(n int, at time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
		if at >= 2*time.Second && !fired {
			fired = true
			r.pump.Activity()
		}
		return quiet(n, at, req)
	})
	r.run(t)
	if got := r.looksBetween(0, 2*time.Second); got != 4 {
		t.Errorf("%d looks before the action", got)
	}
	if got := r.looksBetween(2*time.Second, 5*time.Second); got != 15 {
		t.Errorf("%d looks during the three seconds of activity", got)
	}
	if got := r.looksBetween(5*time.Second, 10*time.Second); got != 10 {
		t.Errorf("%d looks after it", got)
	}
}

func TestActivityWhileWaitingBringsTheNextLookForward(t *testing.T) {
	r := newPumpRig(t, 6*time.Second, quiet)
	fired := false
	r.clock.beforeWait = func(d time.Duration) {
		// The pump has looked at 2.5 s and is about to wait 500 ms; an action starts just after the look.
		if !fired && r.clock.Since() >= 2500*time.Millisecond && d >= 400*time.Millisecond {
			fired = true
			r.pump.Activity()
		}
	}
	r.run(t)
	var times []time.Duration
	for _, l := range r.agent.all() {
		if l.at >= 2*time.Second && l.at < 4*time.Second {
			times = append(times, l.at)
		}
	}
	want := "[2s 2.5s 2.7s 2.9s 3.1s 3.3s 3.5s 3.7s 3.9s]"
	if fmt.Sprint(times) != want {
		t.Errorf("looks %v, want %s", times, want)
	}
}

func TestOnlyPicturesThatChangedAreSent(t *testing.T) {
	frame := webp(t, desktop(1280, 800, 1))
	r := newPumpRig(t, 10*time.Second, changing(frame, 1280, 800, 3*time.Second))
	r.run(t)
	if len(r.sink.frames) != 4 { // at 0, 3, 6 and 9 seconds
		t.Fatalf("%d frames: %+v", len(r.sink.frames), r.sink.frames)
	}
	for i, f := range r.sink.frames {
		if f.seq != uint32(i+1) || f.w != 1280 || f.h != 800 || f.size != len(frame) || f.at != time.Duration(i)*3*time.Second {
			t.Errorf("frame %d: %+v", i, f)
		}
	}
	// Each request names the picture the pump last sent.
	for _, l := range r.agent.all() {
		want := ""
		if l.at > 0 {
			want = fmt.Sprint("h", int64(l.at/(3*time.Second)))
			if l.at%(3*time.Second) == 0 { // the look that finds a new picture still asks about the old one
				want = fmt.Sprint("h", int64(l.at/(3*time.Second))-1)
			}
		}
		if l.req.IfChangedSince != want {
			t.Errorf("at %v the request asked about %q, want %q", l.at, l.req.IfChangedSince, want)
		}
	}
}

func TestAPictureThatIsNotWhatItSaysIsNotForwarded(t *testing.T) {
	frame := webp(t, desktop(640, 400, 0))
	for name, answer := range map[string]answerFunc{
		"not an image": func(int, time.Duration, protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
			return protocol.ScreenshotRes{W: 640, H: 400, Format: "webp", Hash: "x"}, []byte("<script>alert(1)</script>"), nil
		},
		"another size than it says": func(int, time.Duration, protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
			return protocol.ScreenshotRes{W: 1280, H: 800, Format: "webp", Hash: "x"}, frame, nil
		},
		"a huge size": func(int, time.Duration, protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
			return protocol.ScreenshotRes{W: 60000, H: 60000, Format: "webp", Hash: "x"}, frame, nil
		},
		"no picture": func(int, time.Duration, protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
			return protocol.ScreenshotRes{W: 640, H: 400, Format: "webp", Hash: "x"}, nil, nil
		},
	} {
		r := newPumpRig(t, 3*time.Second, answer)
		r.run(t)
		if len(r.sink.frames) != 0 || len(r.errs) == 0 || !strings.Contains(r.errs[0], "not one the viewer will show") {
			t.Errorf("%s: %d frames sent, errors %q", name, len(r.sink.frames), r.errs)
		}
		for _, l := range r.agent.all() {
			if l.req.IfChangedSince != "" {
				t.Errorf("%s: the pump took a refused picture for the last one", name)
			}
		}
	}
}

func TestAnAgentThatDoesNotAnswerIsAskedLessAndLessOften(t *testing.T) {
	r := newPumpRig(t, 10*time.Second, func(int, time.Duration, protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
		return protocol.ScreenshotRes{}, nil, errors.New("the agent is down")
	})
	r.run(t)
	var at []string
	for _, l := range r.agent.all() {
		at = append(at, l.at.String())
	}
	if strings.Join(at, " ") != "0s 1s 3s 5s 7s 9s" {
		t.Errorf("looks at %v", at)
	}
	if len(r.errs) != 6 || !strings.HasPrefix(r.errs[0], "1: taking a screenshot for the viewer") || !strings.HasPrefix(r.errs[5], "6:") {
		t.Errorf("errors %q", r.errs)
	}
}

func TestActivityDoesNotCutAnOutageShort(t *testing.T) {
	var r *pumpRig
	r = newPumpRig(t, 12*time.Second, func(_ int, at time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
		if at >= 6*time.Second {
			return quiet(0, at, req)
		}
		r.pump.Activity() // the session keeps acting while the screen cannot be read
		return protocol.ScreenshotRes{}, nil, errors.New("down")
	})
	r.run(t)
	looks := r.agent.all()
	for i := 1; i < len(looks) && looks[i].at < 6*time.Second; i++ {
		// failures in a row: 0.2 s at the active rate, doubled each time, up to the cap of 2 s
		gap, need := looks[i].at-looks[i-1].at, min(2*time.Second, 200*time.Millisecond<<i)
		if gap < need {
			t.Errorf("look %d came %v after the one before; the outage asks for at least %v", i, gap, need)
		}
	}
	// And once the agent answers, the pump is at the normal rate again.
	if got := r.looksBetween(9*time.Second, 12*time.Second); got < 4 {
		t.Errorf("%d looks after the recovery", got)
	}
	if len(r.errs) == 0 {
		t.Error("no errors reported")
	}
}

func TestAFrameTheSinkRefusesIsNotTakenForSent(t *testing.T) {
	frame := webp(t, desktop(640, 400, 0))
	r := newPumpRig(t, 6*time.Second, changing(frame, 640, 400, time.Second))
	r.sink.failAt = map[uint32]error{2: errors.New("too large")}
	r.run(t)
	var seqs []uint32
	for _, f := range r.sink.frames {
		seqs = append(seqs, f.seq)
	}
	// The refused frame counts as a failure, so the next look is a step later (2 s, not 1.5 s): frames 1, 3, 4, 5 and 6 go.
	if len(r.errs) == 0 || !strings.Contains(r.errs[0], "too large") || fmt.Sprint(seqs) != "[1 3 4 5 6]" {
		t.Errorf("frames %v, errors %q", seqs, r.errs)
	}
	// The look after the refusal asked about the last picture that did go.
	var asked []string
	for _, l := range r.agent.all() {
		asked = append(asked, l.req.IfChangedSince)
	}
	if asked[2] != "h0" || asked[3] != "h0" {
		t.Errorf("requests asked about %q", asked)
	}
}

func TestTheCursorIsSentWhenItMovesAtMostTenTimesASecond(t *testing.T) {
	var r *pumpRig
	moving := func(n int, at time.Duration, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte, error) {
		r.pump.Activity()
		x := n
		if at >= 4*time.Second {
			x = 1000 // the pointer stops
		}
		return protocol.ScreenshotRes{Hash: req.IfChangedSince, Unchanged: true, Cursor: protocol.Point{X: x, Y: 7}}, nil, nil
	}
	r = newPumpRig(t, 6*time.Second, moving, func(o *viewer.PumpOptions) { o.ActiveFPS = 50 })
	r.run(t)
	perSecond := map[int]int{}
	for _, c := range r.sink.cursors {
		perSecond[int(c.at/time.Second)]++
	}
	for sec, n := range perSecond {
		if n > 10 {
			t.Errorf("%d cursor messages in second %d", n, sec)
		}
	}
	if len(r.sink.cursors) < 40 {
		t.Errorf("%d cursor messages in 6 seconds of a moving pointer", len(r.sink.cursors))
	}
	// The pointer stopped at x=1000: that is the last thing the page was told.
	if last := r.sink.cursors[len(r.sink.cursors)-1]; last.x != 1000 || last.y != 7 {
		t.Errorf("last cursor %+v", last)
	}
	for i := 1; i < len(r.sink.cursors); i++ {
		if r.sink.cursors[i].x == r.sink.cursors[i-1].x && r.sink.cursors[i].y == r.sink.cursors[i-1].y {
			t.Errorf("the same cursor sent twice at %v", r.sink.cursors[i].at)
		}
	}
}

func TestStatsCarryTheFigures(t *testing.T) {
	frame := webp(t, desktop(1920, 1080, 2))
	r := newPumpRig(t, 9*time.Second, changing(frame, 1920, 1080, time.Second))
	r.pump.NoteAction()
	r.pump.NoteAction()
	r.pump.NoteLLM(2300 * time.Millisecond)
	r.run(t)
	if len(r.sink.stats) != 4 {
		t.Fatalf("%d stats", len(r.sink.stats))
	}
	s := r.sink.stats[1] // 4 s: two seconds of one frame a second
	if s.FPS < 0.9 || s.FPS > 1.1 || s.FrameBytes != len(frame) || s.LLMMS != 2300 || s.Actions != 2 {
		t.Errorf("stats %+v", s)
	}
	wantKB := float64(len(frame)+9) / 1024 // a frame a second
	if s.KBps < wantKB*0.8 || s.KBps > wantKB*1.4 {
		t.Errorf("%.1f KB/s, want about %.1f", s.KBps, wantKB)
	}
	// Rounded to a tenth.
	if s.KBps != float64(int64(s.KBps*10+0.5))/10 {
		t.Errorf("not rounded: %v", s.KBps)
	}
}

func TestTheIdleBudget(t *testing.T) {
	// An idle desktop changes now and then (a clock ticking over, a blinking caret): here one new 1080p picture every 10 s.
	// Over a minute the page is sent a few pictures and nothing else of weight.
	frame := webp(t, desktop(1920, 1080, 3))
	r := newPumpRig(t, 60*time.Second, changing(frame, 1920, 1080, 10*time.Second))
	r.run(t)
	rate := float64(r.sink.bytes) / 60 / 1024
	t.Logf("a 1080p frame is %.1f KB; an idle minute sends %d frames, %.1f KB/s", float64(len(frame))/1024, len(r.sink.frames), rate)
	if rate >= 50 {
		t.Errorf("an idle minute averages %.1f KB/s; the budget is 50", rate)
	}
	if len(r.sink.frames) != 6 || len(r.agent.all()) != 120 {
		t.Errorf("%d frames from %d looks", len(r.sink.frames), len(r.agent.all()))
	}
}

func TestA1080pFrameFitsTheBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		windows int
	}{{"a bare desktop", 0}, {"one window", 1}, {"three windows with text", 3}} {
		b := webp(t, desktop(1920, 1080, tc.windows))
		t.Logf("%s: %.1f KB", tc.name, float64(len(b))/1024)
		if len(b) >= 200<<10 {
			t.Errorf("%s: %d bytes; the budget is 200 KB", tc.name, len(b))
		}
		if len(b)+9 > viewer.MaxFrameBytes {
			t.Errorf("%s: the frame cannot be sent", tc.name)
		}
	}
}

func TestThePumpFollowsTheConnection(t *testing.T) {
	var looks atomic.Int64
	agent := agentFunc(func(context.Context, string, any, []byte, any) ([]byte, error) {
		looks.Add(1)
		return nil, errors.New("down") // keeps the loop short of work
	})
	sink := &fakeSink{clock: newFakeClock(time.Hour)}
	p, err := viewer.NewPump(viewer.PumpOptions{Agent: agent, Sink: sink, IdleFPS: 200, ActiveFPS: 200})
	if err != nil {
		t.Fatal(err)
	}
	p.OnDisconnect() // nothing to stop
	p.OnConnect()
	p.OnConnect() // a second connection event does not start a second loop
	until(t, "the pump to look", func() bool { return looks.Load() >= 3 })
	p.OnDisconnect()
	n := looks.Load()
	time.Sleep(60 * time.Millisecond)
	if looks.Load() != n {
		t.Error("the pump went on looking after the page was gone")
	}
	p.OnDisconnect()
	p.OnConnect() // and it can start again
	until(t, "the pump to look again", func() bool { return looks.Load() > n+2 })
	p.OnDisconnect()
}

type agentFunc func(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error)

func (f agentFunc) Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error) {
	return f(ctx, typ, req, blob, res)
}

func TestNewPumpWantsAnAgentAndASink(t *testing.T) {
	sink := &fakeSink{clock: newFakeClock(time.Second)}
	agent := agentFunc(func(context.Context, string, any, []byte, any) ([]byte, error) { return nil, nil })
	if _, err := viewer.NewPump(viewer.PumpOptions{Sink: sink}); err == nil {
		t.Error("no agent")
	}
	if _, err := viewer.NewPump(viewer.PumpOptions{Agent: agent}); err == nil {
		t.Error("no sink")
	}
	if _, err := viewer.NewPump(viewer.PumpOptions{Agent: agent, Sink: sink}); err != nil {
		t.Error(err)
	}
}

func TestThePumpWorksWithTheRealHub(t *testing.T) {
	// The pump's sink in production is the hub: frames and stats reach a connected page, and the byte count the stats
	// are made from is the hub's own.
	h := startHub(t, func(o *viewer.HubOptions) {})
	frame := webp(t, desktop(640, 400, 1))
	n := 0
	agent := agentFunc(func(_ context.Context, _ string, req any, _ []byte, res any) ([]byte, error) {
		n++
		out := res.(*protocol.ScreenshotRes)
		*out = protocol.ScreenshotRes{W: 640, H: 400, Format: "webp", Hash: fmt.Sprint("h", n), Cursor: protocol.Point{X: n, Y: n}}
		return frame, nil
	})
	p, err := viewer.NewPump(viewer.PumpOptions{Agent: agent, Sink: h.hub, IdleFPS: 100, ActiveFPS: 100, StatsEvery: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	p.OnConnect()
	defer p.OnDisconnect()
	stats := readType(t, c, "stats")
	if stats["frame_bytes"].(float64) != float64(len(frame)) || stats["kbps"].(float64) <= 0 {
		t.Errorf("stats %v", stats)
	}
	if st := h.hub.Stats(); st.BytesSent < int64(len(frame)) || st.FramesSent == 0 {
		t.Errorf("hub stats %+v", st)
	}
}

func TestDisconnectWaitsForALookInFlight(t *testing.T) {
	// Once OnDisconnect has returned the pump no longer touches the agent or the sink, so the session can close them.
	inside, release := make(chan struct{}), make(chan struct{})
	var after atomic.Bool
	var returned atomic.Bool
	agent := agentFunc(func(context.Context, string, any, []byte, any) ([]byte, error) {
		if returned.Load() {
			after.Store(true)
		}
		select {
		case inside <- struct{}{}:
		default:
		}
		<-release // a screenshot the guest is slow to answer
		if returned.Load() {
			after.Store(true)
		}
		return nil, errors.New("late")
	})
	p, err := viewer.NewPump(viewer.PumpOptions{Agent: agent, Sink: &fakeSink{clock: newFakeClock(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	p.OnConnect()
	<-inside
	done := make(chan struct{})
	go func() { p.OnDisconnect(); returned.Store(true); close(done) }()
	select {
	case <-done:
		t.Fatal("OnDisconnect returned with a look still in flight")
	case <-time.After(80 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("OnDisconnect did not return once the look was over")
	}
	time.Sleep(50 * time.Millisecond)
	if after.Load() {
		t.Error("the pump touched the agent after OnDisconnect returned")
	}
}
