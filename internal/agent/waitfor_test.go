// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/a11y"
	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/agent/x11"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// clock is a time that moves only when something waits or works.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.t = c.t.Add(d)
	return nil
}

type probeFake struct {
	calls   atomic.Int32
	foundAt int // the call number from which the element exists (0: never)
	err     error
	scopes  []string
}

func (p *probeFake) Probe(_ context.Context, scope, _ string, _ time.Duration) (bool, error) {
	n := int(p.calls.Add(1))
	p.scopes = append(p.scopes, scope)
	return p.foundAt > 0 && n >= p.foundAt, p.err
}

type textFake struct {
	calls   int
	foundAt int
	err     error
	regions []*protocol.Rect
	budgets []time.Duration
	clk     *clock
	cost    time.Duration
}

func (f *textFake) Contains(_ context.Context, _ string, r *protocol.Rect, budget time.Duration) (bool, error) {
	f.calls++
	f.regions = append(f.regions, r)
	f.budgets = append(f.budgets, budget)
	if f.clk != nil {
		f.clk.t = f.clk.t.Add(f.cost)
	}
	return f.foundAt > 0 && f.calls >= f.foundAt, f.err
}

type waitRig struct {
	w   *WaitFor
	clk *clock
	scr *fakeScreen
	d   *fakeDesktop
}

func newWaitRig() *waitRig {
	r := &waitRig{clk: &clock{t: time.Unix(1_700_000_000, 0)}, scr: testScreen(160, 100), d: twoWindows()}
	r.w = NewWaitFor(r.scr, imaging.NewHasher(), r.d, nil, nil)
	r.w.Now, r.w.Sleep = r.clk.now, r.clk.sleep
	// Looking at the screen takes a while, as it does for real.
	r.scr.onCapture = func() { r.clk.t = r.clk.t.Add(50 * time.Millisecond) }
	return r
}

func (r *waitRig) wait(t *testing.T, req protocol.WaitForReq) (protocol.WaitForRes, error) {
	t.Helper()
	res, _, err := r.w.Handle(context.Background(), frame(t, protocol.TypeWaitFor, req))
	if err != nil {
		return protocol.WaitForRes{}, err
	}
	return res.(protocol.WaitForRes), nil
}

// changeScreenAt alters the screen when it is captured for the nth time.
func (r *waitRig) changeScreenAt(n int) {
	count := 0
	base := r.scr.onCapture
	r.scr.onCapture = func() {
		base()
		if count++; count == n {
			r.scr.img.Pix[0] ^= 0xff
		}
	}
}

func TestWaitForScreenChange(t *testing.T) {
	r := newWaitRig()
	r.changeScreenAt(4) // the first look is the baseline; the change shows at the fourth
	res, err := r.wait(t, protocol.WaitForReq{Condition: "screen_change", TimeoutMS: 10000})
	if err != nil || !res.Met {
		t.Fatalf("%+v %v", res, err)
	}
	if res.WaitedMS < 750 || res.WaitedMS > 1100 {
		t.Errorf("three polls of 250 ms and the time to look: %d ms", res.WaitedMS)
	}

	r = newWaitRig() // a screen that never changes
	res, err = r.wait(t, protocol.WaitForReq{Condition: "screen_change", TimeoutMS: 2000})
	if err != nil || res.Met || res.WaitedMS < 2000 || res.WaitedMS > 2300 {
		t.Errorf("a wait that ends unmet is an answer, not an error: %+v %v", res, err)
	}
}

func TestWaitForScreenIdle(t *testing.T) {
	r := newWaitRig()
	r.changeScreenAt(3)
	res, err := r.wait(t, protocol.WaitForReq{Condition: "screen_idle", Value: "1000", TimeoutMS: 20000})
	if err != nil || !res.Met {
		t.Fatalf("%+v %v", res, err)
	}
	// The screen changed at the third look (about 0.6 s in); a second of quiet follows.
	if res.WaitedMS < 1500 || res.WaitedMS > 2200 {
		t.Errorf("waited %d ms", res.WaitedMS)
	}

	r = newWaitRig() // idle from the start: met after the idle time, with the default of a second
	res, err = r.wait(t, protocol.WaitForReq{Condition: "screen_idle"})
	if err != nil || !res.Met || res.WaitedMS < 1000 || res.WaitedMS > 1500 {
		t.Errorf("%+v %v", res, err)
	}

	r = newWaitRig() // a screen that keeps changing is never idle
	n := 0
	base := r.scr.onCapture
	r.scr.onCapture = func() { base(); n++; r.scr.img.Pix[0] = byte(n) }
	if res, err = r.wait(t, protocol.WaitForReq{Condition: "screen_idle", Value: "500", TimeoutMS: 3000}); err != nil || res.Met {
		t.Errorf("%+v %v", res, err)
	}
	for _, v := range []string{"abc", "99", "100000", "-5", "1.5"} {
		if _, err := newWaitRig().wait(t, protocol.WaitForReq{Condition: "screen_idle", Value: v}); code(err) != protocol.CodeInvalidArgs {
			t.Errorf("value %q: %v", v, err)
		}
	}
}

func TestWaitForSeconds(t *testing.T) {
	r := newWaitRig()
	res, err := r.wait(t, protocol.WaitForReq{Condition: "seconds", Value: "2.5"})
	if err != nil || !res.Met || res.WaitedMS != 2500 {
		t.Errorf("%+v %v", res, err)
	}
	for _, v := range []string{"", "x", "0", "0.05", "61", "-1"} {
		if _, err := r.wait(t, protocol.WaitForReq{Condition: "seconds", Value: v}); code(err) != protocol.CodeInvalidArgs {
			t.Errorf("value %q: %v", v, err)
		}
	}
}

// titles is a desktop whose windows change after some looks.
type titles struct {
	*fakeDesktop
	calls  int
	appear int
}

func (d *titles) Windows(ctx context.Context) ([]x11.WindowInfo, error) {
	d.calls++
	ws, err := d.fakeDesktop.Windows(ctx)
	if d.calls >= d.appear {
		ws = append(ws, x11.WindowInfo{ID: 9, Title: "Save As — Mousepad"})
	}
	return ws, err
}

func TestWaitForWindowTitle(t *testing.T) {
	r := newWaitRig()
	r.w.Desktop = &titles{fakeDesktop: r.d, appear: 4}
	res, err := r.wait(t, protocol.WaitForReq{Condition: "window_title", Value: "save as", TimeoutMS: 5000})
	if err != nil || !res.Met || res.WaitedMS != 750 {
		t.Errorf("%+v %v", res, err)
	}
	if res, err = newWaitRig().wait(t, protocol.WaitForReq{Condition: "window_title", Value: "thunar", TimeoutMS: 1000}); err != nil || !res.Met || res.WaitedMS != 0 {
		t.Errorf("a window that is already there: %+v %v", res, err)
	}
	if res, err = newWaitRig().wait(t, protocol.WaitForReq{Condition: "window_title", Value: "no such window", TimeoutMS: 1000}); err != nil || res.Met {
		t.Errorf("%+v %v", res, err)
	}
	r = newWaitRig()
	r.d.winErr = errors.New("BadWindow")
	if _, err = r.wait(t, protocol.WaitForReq{Condition: "window_title", Value: "x"}); code(err) != protocol.CodeInternal {
		t.Errorf("%v", err)
	}
}

func TestWaitForElement(t *testing.T) {
	r := newWaitRig()
	p := &probeFake{foundAt: 3}
	r.w.Probe = p
	res, err := r.wait(t, protocol.WaitForReq{Condition: "element", Value: "Save", TimeoutMS: 5000})
	if err != nil || !res.Met || res.WaitedMS != 500 || p.calls.Load() != 3 {
		t.Errorf("%+v %v (%d probes)", res, err, p.calls.Load())
	}
	if p.scopes[0] != a11y.ScopeDesktop {
		t.Errorf("a dialog may be any window: %v", p.scopes)
	}
	if _, err := newWaitRig().wait(t, protocol.WaitForReq{Condition: "element", Value: "Save"}); code(err) != protocol.CodeUnsupported {
		t.Errorf("without accessibility: %v", err)
	}
	r.w.Probe = &probeFake{err: &a11y.Error{Kind: a11y.KindInvalid, Msg: "query is longer"}}
	if _, err := r.wait(t, protocol.WaitForReq{Condition: "element", Value: "x"}); code(err) != protocol.CodeInvalidArgs {
		t.Errorf("%v", err)
	}
	r.w.Probe = &probeFake{err: a11y.ErrNoBus}
	if _, err := r.wait(t, protocol.WaitForReq{Condition: "element", Value: "x"}); code(err) != protocol.CodeUnsupported {
		t.Errorf("%v", err)
	}
}

func TestWaitForTextFoundByTheAccessibilityNamesFirst(t *testing.T) {
	r := newWaitRig()
	p := &probeFake{foundAt: 2}
	f := &textFake{foundAt: 1, clk: r.clk}
	r.w.Probe, r.w.Text = p, f
	res, err := r.wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "Done", TimeoutMS: 5000})
	if err != nil || !res.Met {
		t.Fatalf("%+v %v", res, err)
	}
	if p.scopes[0] != a11y.ScopeActiveWindow {
		t.Errorf("the window that has the focus: %v", p.scopes)
	}
	if f.calls != 1 {
		t.Errorf("text was read off the screen once before the names had it (%d)", f.calls)
	}
}

func TestWaitForTextReadsTheActiveWindowAndNotTooOften(t *testing.T) {
	r := newWaitRig()
	f := &textFake{foundAt: 3, clk: r.clk, cost: 100 * time.Millisecond}
	r.w.Text = f
	r.d.active = &x11.WindowInfo{ID: 1, Title: "Terminal", X: 10, Y: 20, W: 100, H: 60}
	// The screen changes on every look, so the only brake is the gap between readings.
	n := 0
	base := r.scr.onCapture
	r.scr.onCapture = func() { base(); n++; r.scr.img.Pix[0] = byte(n) }
	res, err := r.wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "Hello", TimeoutMS: 20000})
	if err != nil || !res.Met {
		t.Fatalf("%+v %v", res, err)
	}
	if f.calls != 3 {
		t.Fatalf("%d readings", f.calls)
	}
	if *f.regions[0] != (protocol.Rect{10, 20, 100, 60}) {
		t.Errorf("only the window that has the focus is read: %v", f.regions[0])
	}
	// Three readings at least two seconds apart.
	if res.WaitedMS < 4000 {
		t.Errorf("readings are at least %s apart: %d ms for three", ocrInterval, res.WaitedMS)
	}
	for _, b := range f.budgets {
		if b < time.Second || b > maxOCRBudgetW {
			t.Errorf("budget %s", b)
		}
	}
}

func TestWaitForTextDoesNotReadAScreenThatHasNotChanged(t *testing.T) {
	r := newWaitRig()
	f := &textFake{clk: r.clk}
	r.w.Text = f
	res, err := r.wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "Hello", TimeoutMS: 9000})
	if err != nil || res.Met {
		t.Fatalf("%+v %v", res, err)
	}
	if f.calls != 1 {
		t.Errorf("the same screen is read once (%d readings)", f.calls)
	}
	// The window may be off the screen or tiny: then the whole screen is read.
	r = newWaitRig()
	f = &textFake{clk: r.clk}
	r.w.Text = f
	r.d.active = &x11.WindowInfo{X: 5000, Y: 5000, W: 100, H: 100}
	if _, err = r.wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "Hello", TimeoutMS: 1000}); err != nil || f.regions[0] != nil {
		t.Errorf("%v %v", err, f.regions)
	}
	// A window half off the screen is read where it is on it.
	r = newWaitRig()
	f = &textFake{clk: r.clk}
	r.w.Text = f
	r.d.active = &x11.WindowInfo{X: 100, Y: 60, W: 200, H: 200}
	if _, err = r.wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "Hello", TimeoutMS: 1000}); err != nil || *f.regions[0] != (protocol.Rect{100, 60, 60, 40}) {
		t.Errorf("%v %v", err, f.regions[0])
	}
}

func TestWaitForTextRecognitionProblems(t *testing.T) {
	r := newWaitRig()
	r.w.Text = &textFake{err: server.Fail(protocol.CodeTimeout, "took longer"), clk: r.clk}
	if res, err := r.wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "x", TimeoutMS: 1000}); err != nil || res.Met {
		t.Errorf("a recognition too slow for the time left ends the wait unmet: %+v %v", res, err)
	}
	r = newWaitRig()
	r.w.Text = &textFake{err: server.Fail(protocol.CodeUnsupported, "tesseract is not installed"), clk: r.clk}
	if _, err := r.wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "x", TimeoutMS: 1000}); code(err) != protocol.CodeUnsupported {
		t.Errorf("a recognition that cannot run is told: %v", err)
	}
	if _, err := newWaitRig().wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "x"}); code(err) != protocol.CodeUnsupported {
		t.Errorf("neither source: %v", err)
	}
	// Accessibility failing is not fatal when recognition can still look.
	r = newWaitRig()
	r.w.Probe = &probeFake{err: a11y.ErrNoBus}
	r.w.Text = &textFake{foundAt: 1, clk: r.clk}
	if res, err := r.wait(t, protocol.WaitForReq{Condition: "text_visible", Value: "x", TimeoutMS: 1000}); err != nil || !res.Met {
		t.Errorf("%+v %v", res, err)
	}
}

func TestWaitForValidation(t *testing.T) {
	r := newWaitRig()
	for name, req := range map[string]protocol.WaitForReq{
		"unknown condition": {Condition: "forever"},
		"no condition":      {},
		"timeout low":       {Condition: "screen_change", TimeoutMS: 50},
		"timeout high":      {Condition: "screen_change", TimeoutMS: 61000},
		"timeout negative":  {Condition: "screen_change", TimeoutMS: -1},
		"change with value": {Condition: "screen_change", Value: "x"},
		"title no value":    {Condition: "window_title"},
		"title long":        {Condition: "window_title", Value: strings.Repeat("x", 201)},
		"title control":     {Condition: "window_title", Value: "a\x00b"},
		"text no value":     {Condition: "text_visible", Value: "  "},
		"element long":      {Condition: "element", Value: strings.Repeat("x", 101)},
	} {
		if _, err := r.wait(t, req); code(err) != protocol.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := r.w.Handle(context.Background(), frame(t, protocol.TypeWaitFor, map[string]any{"condition": "screen_change", "forever": true})); code(err) != protocol.CodeInvalidArgs {
		t.Errorf("an unknown field: %v", err)
	}
	if r.scr.captures != 0 {
		t.Errorf("a refused wait looked at the screen (%d)", r.scr.captures)
	}
}

func TestWaitForStopsWhenCanceled(t *testing.T) {
	r := newWaitRig()
	ctx, cancel := context.WithCancel(context.Background())
	r.w.Sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return r.clk.sleep(ctx, d)
	}
	if _, _, err := r.w.Handle(ctx, frame(t, protocol.TypeWaitFor, protocol.WaitForReq{Condition: "screen_change", TimeoutMS: 10000})); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	r = newWaitRig()
	r.scr.capErr = errors.New("BadMatch")
	if _, err := r.wait(t, protocol.WaitForReq{Condition: "screen_change"}); code(err) != protocol.CodeInternal || !strings.Contains(err.Error(), "BadMatch") {
		t.Errorf("%v", err)
	}
}

func TestWaitForRealTimeNeverWaitsPastTheTimeout(t *testing.T) {
	scr := testScreen(64, 40)
	w := NewWaitFor(scr, imaging.NewHasher(), twoWindows(), nil, nil)
	begin := time.Now()
	res, _, err := w.Handle(context.Background(), frame(t, protocol.TypeWaitFor, protocol.WaitForReq{Condition: "screen_change", TimeoutMS: 400}))
	if err != nil || res.(protocol.WaitForRes).Met || time.Since(begin) > 3*time.Second {
		t.Errorf("%+v %v after %s", res, err, time.Since(begin))
	}
}
