// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"image"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/agent/a11y"
	"github.com/PacifAIst/controlsnack/internal/agent/server"
	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// Conditions of a `wait_for` request.
const (
	WaitSeconds      = "seconds"
	WaitScreenChange = "screen_change"
	WaitScreenIdle   = "screen_idle"
	WaitTextVisible  = "text_visible"
	WaitWindowTitle  = "window_title"
	WaitElement      = "element"
)

// Limits and timing of waiting.
const (
	defaultWait   = 10 * time.Second
	minWait       = 100 * time.Millisecond
	maxWait       = 60 * time.Second
	defaultIdle   = time.Second
	maxWaitValue  = 200 // characters of the text, title or query to wait for
	pollInterval  = 250 * time.Millisecond
	ocrInterval   = 2 * time.Second
	probeBudget   = 2 * time.Second
	maxOCRBudgetW = 120 * time.Second
)

// ElementProbe says whether an accessibility element matching a text exists in a scope; *a11y.Service is the real one.
type ElementProbe interface {
	Probe(ctx context.Context, scope, text string, budget time.Duration) (bool, error)
}

// TextFinder says whether a text is on the screen or in a region of it; *OCR is the real one.
type TextFinder interface {
	Contains(ctx context.Context, text string, region *protocol.Rect, budget time.Duration) (bool, error)
}

// WaitFor serves `wait_for` requests: waiting inside the guest for something to happen, so a model spends no turns
// looking again and again (PLAN.md section 8).
type WaitFor struct {
	Screen  Screen
	Hasher  imaging.Hasher
	Desktop Desktop
	// Probe and Text may be nil: the conditions that need them then say so.
	Probe ElementProbe
	Text  TextFinder

	// Poll and OCRGap are how often the conditions look, and how often at most text is read off the screen.
	Poll, OCRGap time.Duration
	Now          func() time.Time
	Sleep        func(ctx context.Context, d time.Duration) error
}

// NewWaitFor returns the handler with the default timing.
func NewWaitFor(s Screen, h imaging.Hasher, d Desktop, p ElementProbe, t TextFinder) *WaitFor {
	return &WaitFor{Screen: s, Hasher: h, Desktop: d, Probe: p, Text: t, Poll: pollInterval, OCRGap: ocrInterval, Now: time.Now, Sleep: sleepContext}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// check looks once. remaining is how long the wait has left.
type check func(ctx context.Context, remaining time.Duration) (bool, error)

// Handle answers one `wait_for` request.
func (w *WaitFor) Handle(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.WaitForReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	timeout := defaultWait
	if r.TimeoutMS != 0 {
		timeout = time.Duration(r.TimeoutMS) * time.Millisecond
		if timeout < minWait || timeout > maxWait {
			return nil, nil, server.Fail(protocol.CodeInvalidArgs, "timeout_ms %d is outside %d..%d (0 means %d)", r.TimeoutMS,
				minWait.Milliseconds(), maxWait.Milliseconds(), defaultWait.Milliseconds())
		}
	}
	if r.Condition == WaitSeconds {
		return w.seconds(ctx, r)
	}
	chk, err := w.condition(r)
	if err != nil {
		return nil, nil, err
	}

	start := w.Now()
	deadline := start.Add(timeout)
	for {
		met, err := chk(ctx, max(deadline.Sub(w.Now()), 0))
		if err != nil {
			return nil, nil, waitError(ctx, err)
		}
		now := w.Now()
		if met || !now.Before(deadline) {
			return protocol.WaitForRes{Met: met, WaitedMS: int(now.Sub(start).Milliseconds())}, nil, nil
		}
		if err := w.Sleep(ctx, min(w.Poll, deadline.Sub(now))); err != nil {
			return nil, nil, err
		}
	}
}

// seconds waits a number of seconds and is always met.
func (w *WaitFor) seconds(ctx context.Context, r protocol.WaitForReq) (any, []byte, error) {
	secs, err := strconv.ParseFloat(strings.TrimSpace(r.Value), 64)
	if err != nil || secs < minWait.Seconds() || secs > maxWait.Seconds() {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "seconds needs a value between %g and %g, in seconds", minWait.Seconds(), maxWait.Seconds())
	}
	d := time.Duration(secs * float64(time.Second))
	start := w.Now()
	if err := w.Sleep(ctx, d); err != nil {
		return nil, nil, err
	}
	return protocol.WaitForRes{Met: true, WaitedMS: int(w.Now().Sub(start).Milliseconds())}, nil, nil
}

// waitError passes cancellations on and turns anything else into the protocol's error.
func waitError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var he *server.HandlerError
	if errors.As(err, &he) {
		return err
	}
	return server.Fail(protocol.CodeInternal, "waiting failed: %v", err)
}

// text checks a value given for a condition.
func waitValue(r protocol.WaitForReq, maxRunes int) (string, error) {
	v := strings.TrimSpace(r.Value)
	switch {
	case v == "":
		return "", server.Fail(protocol.CodeInvalidArgs, "%s needs a value", r.Condition)
	case utf8.RuneCountInString(v) > maxRunes:
		return "", server.Fail(protocol.CodeInvalidArgs, "value is longer than %d characters", maxRunes)
	case !utf8.ValidString(v):
		return "", server.Fail(protocol.CodeInvalidArgs, "value is not valid UTF-8")
	}
	for _, c := range v {
		if unicode.IsControl(c) {
			return "", server.Fail(protocol.CodeInvalidArgs, "value contains a control character")
		}
	}
	return v, nil
}

// condition builds the check of a condition, with the state it keeps between looks.
func (w *WaitFor) condition(r protocol.WaitForReq) (check, error) {
	switch r.Condition {
	case WaitScreenChange:
		if r.Value != "" {
			return nil, server.Fail(protocol.CodeInvalidArgs, "screen_change takes no value")
		}
		first := ""
		return func(ctx context.Context, _ time.Duration) (bool, error) {
			h, _, err := w.snapshot(ctx)
			if err != nil {
				return false, err
			}
			if first == "" {
				first = h // the screen as it is when the wait begins
				return false, nil
			}
			return h != first, nil
		}, nil

	case WaitScreenIdle:
		idle := defaultIdle
		if v := strings.TrimSpace(r.Value); v != "" {
			ms, err := strconv.Atoi(v)
			if err != nil || time.Duration(ms)*time.Millisecond < minWait || time.Duration(ms)*time.Millisecond > maxWait {
				return nil, server.Fail(protocol.CodeInvalidArgs, "screen_idle takes a value of %d to %d, in milliseconds", minWait.Milliseconds(), maxWait.Milliseconds())
			}
			idle = time.Duration(ms) * time.Millisecond
		}
		last := ""
		var since time.Time
		return func(ctx context.Context, _ time.Duration) (bool, error) {
			h, _, err := w.snapshot(ctx)
			if err != nil {
				return false, err
			}
			now := w.Now()
			if h != last {
				last, since = h, now
				return false, nil
			}
			return now.Sub(since) >= idle, nil
		}, nil

	case WaitWindowTitle:
		want, err := waitValue(r, maxWaitValue)
		if err != nil {
			return nil, err
		}
		want = strings.ToLower(want)
		return func(ctx context.Context, _ time.Duration) (bool, error) {
			ws, err := w.Desktop.Windows(ctx)
			if err != nil {
				return false, mapError(ctx, err, "listing the windows")
			}
			for _, win := range ws {
				if strings.Contains(strings.ToLower(win.Title), want) {
					return true, nil
				}
			}
			return false, nil
		}, nil

	case WaitElement:
		query, err := waitValue(r, 100)
		if err != nil {
			return nil, err
		}
		if w.Probe == nil {
			return nil, server.Fail(protocol.CodeUnsupported, "accessibility is not available in this session")
		}
		return func(ctx context.Context, _ time.Duration) (bool, error) {
			ok, err := w.Probe.Probe(ctx, a11y.ScopeDesktop, query, probeBudget)
			return ok, a11yErrorOrNil(ctx, err)
		}, nil

	case WaitTextVisible:
		text, err := waitValue(r, maxWaitValue)
		if err != nil {
			return nil, err
		}
		if w.Probe == nil && w.Text == nil {
			return nil, server.Fail(protocol.CodeUnsupported, "neither accessibility nor text recognition is available in this session")
		}
		return w.textVisible(text), nil
	}
	return nil, server.Fail(protocol.CodeInvalidArgs, "condition %q is not seconds, screen_change, screen_idle, text_visible, window_title or element", r.Condition)
}

// a11yErrorOrNil keeps an accessibility failure that the model should hear of and ignores one that only means "not
// there" (a window with no accessible application).
func a11yErrorOrNil(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	return a11yError(ctx, err, "looking for the element")
}

// textVisible waits for a text on the screen: the accessibility names first, which are cheap, and the text read off the
// screen with recognition, which is not, at most every OCRGap and only when the screen has changed since it was last
// read, and only in the window that has the focus.
func (w *WaitFor) textVisible(text string) check {
	var lastRead time.Time
	lastHash := ""
	return func(ctx context.Context, remaining time.Duration) (bool, error) {
		if w.Probe != nil {
			ok, err := w.Probe.Probe(ctx, a11y.ScopeActiveWindow, text, probeBudget)
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if err == nil && ok {
				return true, nil
			}
		}
		if w.Text == nil {
			return false, nil
		}
		hash, screen, err := w.snapshot(ctx)
		if err != nil {
			return false, err
		}
		now := w.Now()
		if hash == lastHash || (!lastRead.IsZero() && now.Sub(lastRead) < w.OCRGap) {
			return false, nil
		}
		var region *protocol.Rect
		if win, err := w.Desktop.ActiveWindow(ctx); err == nil && win != nil {
			if r := image.Rect(win.X, win.Y, win.X+win.W, win.Y+win.H).Intersect(screen); r.Dx() >= minOCRRegion && r.Dy() >= minOCRRegion {
				region = &protocol.Rect{r.Min.X, r.Min.Y, r.Dx(), r.Dy()}
			}
		}
		budget := min(max(remaining, time.Duration(minOCRBudget)*time.Millisecond), maxOCRBudgetW)
		found, err := w.Text.Contains(ctx, text, region, budget)
		lastRead, lastHash = w.Now(), hash
		if err != nil {
			if errorCode(err) == protocol.CodeTimeout {
				return false, nil // too slow for the time left; the wait ends
			}
			return false, err
		}
		return found, nil
	}
}

// snapshot captures the screen and fingerprints it.
func (w *WaitFor) snapshot(ctx context.Context) (string, image.Rectangle, error) {
	img, err := w.Screen.Capture(ctx)
	if err != nil {
		return "", image.Rectangle{}, server.Fail(protocol.CodeInternal, "capturing the screen: %v", err)
	}
	return w.Hasher.Hash(img), img.Bounds(), nil
}

// errorCode is the protocol code of an error a handler made, or "".
func errorCode(err error) string {
	var he *server.HandlerError
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}
