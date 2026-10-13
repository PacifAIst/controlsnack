// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/agent/x11"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// InputDevice is what the input handler needs from the display; *x11.Conn is the real one.
type InputDevice interface {
	MoveTo(x, y int) error
	Button(button int, down bool) error
	KeyCombo(ctx context.Context, combo string, repeat int) error
	TypeText(ctx context.Context, text string) error
	Sync() error
	Size() (w, h int, err error)
}

// Limits of one `input` request.
const (
	maxInputEvents = 64
	maxTypedRunes  = 4000
	maxScrollNotch = 20
	maxKeyRepeat   = 20
)

// Input event types (PLAN.md section 5.1): the protocol's own plus "drag".
const (
	evMove   = "move"
	evDown   = "down"
	evUp     = "up"
	evClick  = "click"
	evScroll = "scroll"
	evKey    = "key"
	evText   = "text"
	evDrag   = "drag"
)

// Timing of mouse gestures. A double click is two clicks well inside the desktop's double-click interval (400 ms by
// default); a drag takes about 200 ms in ten steps, slow enough for applications that need motion events to start one.
const (
	clickHold   = 12 * time.Millisecond
	doubleGap   = 60 * time.Millisecond
	notchGap    = 8 * time.Millisecond
	dragSteps   = 10
	dragStepGap = 20 * time.Millisecond
	dragEdgeGap = 30 * time.Millisecond
)

// Input serves `input` requests.
type Input struct {
	Device InputDevice
	// Screen and Hasher give the screen hash after the input; they should be the screenshot handler's, so the two
	// hashes can be compared.
	Screen Screen
	Hasher imaging.Hasher
	// Settle is how long to wait after the last event before looking at the screen, and MaxSettle how much longer to
	// wait for a screen that has not changed yet. The effect of a click shows a moment later, and a hash taken at once
	// would say "nothing happened".
	Settle, MaxSettle time.Duration
	Sleep             func(time.Duration)
}

// NewInput returns the handler with the default timing.
func NewInput(d InputDevice, s Screen, h imaging.Hasher) *Input {
	return &Input{Device: d, Screen: s, Hasher: h, Settle: 120 * time.Millisecond, MaxSettle: 500 * time.Millisecond, Sleep: time.Sleep}
}

// Handle runs the events of one request in order and answers with the screen hash afterwards.
func (in *Input) Handle(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.InputReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	w, h, err := in.Device.Size()
	if err != nil {
		return nil, nil, server.Fail(protocol.CodeInternal, "reading the screen size: %v", err)
	}
	if err := validateInput(r, w, h); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	before := in.hash(ctx)
	for i, ev := range r.Events {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := in.run(ctx, ev); err != nil {
			var ke *x11.KeyError
			switch {
			case ctx.Err() != nil:
				return nil, nil, ctx.Err()
			case errors.As(err, &ke):
				return nil, nil, server.Fail(protocol.CodeInvalidArgs, "event %d (%s): %v", i+1, ev.Type, err)
			}
			return nil, nil, server.Fail(protocol.CodeInternal, "event %d (%s) failed after %d earlier ones ran: %v", i+1, ev.Type, i, err)
		}
	}
	if err := in.Device.Sync(); err != nil {
		return nil, nil, server.Fail(protocol.CodeInternal, "%v", err)
	}
	return protocol.InputRes{ScreenHashAfter: in.settled(ctx, before)}, nil, nil
}

// hash fingerprints the screen now ("" when it cannot be read).
func (in *Input) hash(ctx context.Context) string {
	img, err := in.Screen.Capture(ctx)
	if err != nil {
		return ""
	}
	return in.Hasher.Hash(img)
}

// settled waits for the input to show on the screen and returns the hash then: after Settle, and while the screen is
// still as it was before the input, in steps of 100 ms up to MaxSettle more.
func (in *Input) settled(ctx context.Context, before string) string {
	in.Sleep(in.Settle)
	after := in.hash(ctx)
	if before == "" || after == "" {
		return after
	}
	for waited := time.Duration(0); after == before && waited < in.MaxSettle && ctx.Err() == nil; waited += 100 * time.Millisecond {
		in.Sleep(100 * time.Millisecond)
		after = in.hash(ctx)
	}
	return after
}

// validateInput checks a whole request before anything is sent: a request that is refused does nothing at all.
func validateInput(r protocol.InputReq, w, h int) error {
	if len(r.Events) == 0 {
		return errors.New("events is empty")
	}
	if len(r.Events) > maxInputEvents {
		return fmt.Errorf("%d events in one request, the limit is %d", len(r.Events), maxInputEvents)
	}
	typed := 0
	inside := func(i int, name string, x, y int) error {
		if x < 0 || y < 0 || x >= w || y >= h {
			return fmt.Errorf("event %d: %s (%d, %d) is outside the %dx%d screen", i+1, name, x, y, w, h)
		}
		return nil
	}
	for i, ev := range r.Events {
		bad := func(format string, args ...any) error {
			return fmt.Errorf("event %d (%s): %s", i+1, ev.Type, fmt.Sprintf(format, args...))
		}
		switch ev.Type {
		case evMove, evDown, evUp, evClick:
			if err := inside(i, "position", ev.X, ev.Y); err != nil {
				return err
			}
			if ev.Type != evMove {
				if _, err := buttonNumber(ev.Button); err != nil {
					return bad("%v", err)
				}
			}
			if ev.Double && ev.Type != evClick {
				return bad("double applies to clicks only")
			}
		case evScroll:
			if err := inside(i, "position", ev.X, ev.Y); err != nil {
				return err
			}
			if ev.DX == 0 && ev.DY == 0 {
				return bad("dx and dy are both zero")
			}
			if abs(ev.DX) > maxScrollNotch || abs(ev.DY) > maxScrollNotch {
				return bad("scroll is at most %d notches in each direction", maxScrollNotch)
			}
		case evDrag:
			if err := inside(i, "start", ev.X, ev.Y); err != nil {
				return err
			}
			if ev.DragTo == nil {
				return bad("drag_to is required")
			}
			if err := inside(i, "drag_to", ev.DragTo.X, ev.DragTo.Y); err != nil {
				return err
			}
			if _, err := buttonNumber(ev.Button); err != nil {
				return bad("%v", err)
			}
		case evKey:
			if ev.Keys == "" {
				return bad("keys is required")
			}
			if ev.Repeat < 0 || ev.Repeat > maxKeyRepeat {
				return bad("repeat is 1 to %d", maxKeyRepeat)
			}
			if _, err := x11.ParseCombo(ev.Keys); err != nil {
				return bad("%v", err)
			}
		case evText:
			n := utf8.RuneCountInString(ev.Text)
			if n == 0 {
				return bad("text is empty")
			}
			if typed += n; typed > maxTypedRunes {
				return bad("more than %d characters to type in one request", maxTypedRunes)
			}
			if !utf8.ValidString(ev.Text) {
				return bad("text is not valid UTF-8")
			}
			if err := typable(ev.Text); err != nil {
				return bad("%v", err)
			}
		default:
			return fmt.Errorf("event %d: type %q is not move, down, up, click, scroll, drag, key or text", i+1, ev.Type)
		}
	}
	return nil
}

// typable checks that every character of a text has a key: the line break, the carriage return and the tab are keys
// (Return, Tab); any other control character is refused before the first character of the request is typed.
func typable(text string) error {
	for _, ch := range text {
		switch ch {
		case '\n', '\r', '\t':
			continue
		}
		if _, err := x11.RuneKeysym(ch); err != nil {
			return err
		}
	}
	return nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// buttonNumber maps a button name to X's number; the empty name is the left button.
func buttonNumber(name string) (int, error) {
	switch name {
	case "", "left":
		return x11.ButtonLeft, nil
	case "middle":
		return x11.ButtonMiddle, nil
	case "right":
		return x11.ButtonRight, nil
	}
	return 0, fmt.Errorf("button %q is not left, middle or right", name)
}

// run performs one validated event.
func (in *Input) run(ctx context.Context, ev protocol.InputEvent) error {
	d := in.Device
	switch ev.Type {
	case evMove:
		return d.MoveTo(ev.X, ev.Y)
	case evDown, evUp:
		b, _ := buttonNumber(ev.Button)
		if err := d.MoveTo(ev.X, ev.Y); err != nil {
			return err
		}
		return d.Button(b, ev.Type == evDown)
	case evClick:
		b, _ := buttonNumber(ev.Button)
		if err := d.MoveTo(ev.X, ev.Y); err != nil {
			return err
		}
		clicks := 1
		if ev.Double {
			clicks = 2
		}
		for i := 0; i < clicks; i++ {
			if i > 0 {
				in.Sleep(doubleGap)
			}
			if err := in.tapButton(b); err != nil {
				return err
			}
		}
		return nil
	case evScroll:
		if err := d.MoveTo(ev.X, ev.Y); err != nil {
			return err
		}
		if err := in.notches(ctx, ev.DY, x11.WheelUp, x11.WheelDown); err != nil {
			return err
		}
		return in.notches(ctx, ev.DX, x11.WheelLeft, x11.WheelRight)
	case evDrag:
		return in.drag(ctx, ev)
	case evKey:
		repeat := ev.Repeat
		if repeat == 0 {
			repeat = 1
		}
		return d.KeyCombo(ctx, ev.Keys, repeat)
	case evText:
		return d.TypeText(ctx, ev.Text)
	}
	return fmt.Errorf("unknown event type %q", ev.Type)
}

// tapButton presses and releases a button; the release is sent even if something goes wrong in between.
func (in *Input) tapButton(b int) (err error) {
	if err := in.Device.Button(b, true); err != nil {
		return err
	}
	defer func() {
		if rerr := in.Device.Button(b, false); err == nil {
			err = rerr
		}
	}()
	in.Sleep(clickHold)
	return nil
}

// notches turns the wheel n times: positive n is the second button (down, right), negative the first.
func (in *Input) notches(ctx context.Context, n, neg, pos int) error {
	b := pos
	if n < 0 {
		b, n = neg, -n
	}
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := in.Device.Button(b, true); err != nil {
			return err
		}
		if err := in.Device.Button(b, false); err != nil {
			return err
		}
		in.Sleep(notchGap)
	}
	return nil
}

// drag presses at the start, moves to the end in steps and releases, whatever happens in between.
func (in *Input) drag(ctx context.Context, ev protocol.InputEvent) (err error) {
	b, _ := buttonNumber(ev.Button)
	to := *ev.DragTo
	if err := in.Device.MoveTo(ev.X, ev.Y); err != nil {
		return err
	}
	if err := in.Device.Button(b, true); err != nil {
		return err
	}
	defer func() { // never leave the button down
		if rerr := in.Device.Button(b, false); err == nil {
			err = rerr
		}
	}()
	in.Sleep(dragEdgeGap)
	for i := 1; i <= dragSteps; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		x := ev.X + (to.X-ev.X)*i/dragSteps
		y := ev.Y + (to.Y-ev.Y)*i/dragSteps
		if err := in.Device.MoveTo(x, y); err != nil {
			return err
		}
		in.Sleep(dragStepGap)
	}
	in.Sleep(dragEdgeGap)
	return nil
}
