// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/agent/x11"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

type fakeInputDevice struct {
	w, h    int
	sizeErr error
	ops     []string
	failOn  string // an operation string at which the device fails, once
	failErr error
	onOp    func(op string)
}

func (f *fakeInputDevice) rec(op string) error {
	if f.onOp != nil {
		f.onOp(op)
	}
	if f.failOn != "" && op == f.failOn {
		f.failOn = ""
		return f.failErr
	}
	f.ops = append(f.ops, op)
	return nil
}

func (f *fakeInputDevice) MoveTo(x, y int) error { return f.rec(fmt.Sprintf("move %d,%d", x, y)) }
func (f *fakeInputDevice) Button(b int, down bool) error {
	if down {
		return f.rec(fmt.Sprintf("down %d", b))
	}
	return f.rec(fmt.Sprintf("up %d", b))
}
func (f *fakeInputDevice) KeyCombo(_ context.Context, combo string, repeat int) error {
	return f.rec(fmt.Sprintf("keys %s x%d", combo, repeat))
}
func (f *fakeInputDevice) TypeText(_ context.Context, text string) error {
	return f.rec("type " + text)
}
func (f *fakeInputDevice) Sync() error { return f.rec("sync") }
func (f *fakeInputDevice) Size() (int, int, error) {
	return f.w, f.h, f.sizeErr
}

// inputRig is an Input over fakes, with a recorded clock.
type inputRig struct {
	in    *Input
	dev   *fakeInputDevice
	scr   *fakeScreen
	slept []time.Duration
}

func newInputRig() *inputRig {
	r := &inputRig{dev: &fakeInputDevice{w: 1280, h: 800, failErr: errors.New("BadAccess")}, scr: testScreen(64, 40)}
	r.in = NewInput(r.dev, r.scr, imaging.NewHasher())
	r.in.Sleep = func(d time.Duration) { r.slept = append(r.slept, d) }
	return r
}

func (r *inputRig) total() time.Duration {
	var t time.Duration
	for _, d := range r.slept {
		t += d
	}
	return t
}

func (r *inputRig) run(t *testing.T, events ...protocol.InputEvent) (protocol.InputRes, error) {
	t.Helper()
	res, _, err := r.in.Handle(context.Background(), frame(t, protocol.TypeInput, protocol.InputReq{Events: events}))
	if err != nil {
		return protocol.InputRes{}, err
	}
	return res.(protocol.InputRes), nil
}

// without returns the operations without the final sync.
func (r *inputRig) without() string {
	var keep []string
	for _, o := range r.dev.ops {
		if o != "sync" {
			keep = append(keep, o)
		}
	}
	return strings.Join(keep, "; ")
}

func TestInputClicks(t *testing.T) {
	tests := []struct {
		name string
		ev   protocol.InputEvent
		want string
	}{
		{"left click is the default", protocol.InputEvent{Type: "click", X: 100, Y: 50}, "move 100,50; down 1; up 1"},
		{"right", protocol.InputEvent{Type: "click", X: 5, Y: 6, Button: "right"}, "move 5,6; down 3; up 3"},
		{"middle", protocol.InputEvent{Type: "click", X: 5, Y: 6, Button: "middle"}, "move 5,6; down 2; up 2"},
		{"double", protocol.InputEvent{Type: "click", X: 9, Y: 9, Double: true}, "move 9,9; down 1; up 1; down 1; up 1"},
		{"move only", protocol.InputEvent{Type: "move", X: 1279, Y: 799}, "move 1279,799"},
		{"press and hold", protocol.InputEvent{Type: "down", X: 10, Y: 20}, "move 10,20; down 1"},
		{"release elsewhere", protocol.InputEvent{Type: "up", X: 30, Y: 40, Button: "right"}, "move 30,40; up 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newInputRig()
			if _, err := r.run(t, tt.ev); err != nil {
				t.Fatal(err)
			}
			if got := r.without(); got != tt.want {
				t.Errorf("%s, want %s", got, tt.want)
			}
		})
	}
	r := newInputRig()
	_, _ = r.run(t, protocol.InputEvent{Type: "click", X: 1, Y: 1, Double: true})
	if !slices.Contains(r.slept, doubleGap) || doubleGap >= 400*time.Millisecond {
		t.Errorf("the two clicks of a double click are far inside the desktop's interval: %v", r.slept)
	}
}

func TestInputScroll(t *testing.T) {
	tests := []struct {
		name   string
		dx, dy int
		want   string
	}{
		{"down", 0, 3, "move 10,10; down 5; up 5; down 5; up 5; down 5; up 5"},
		{"up", 0, -2, "move 10,10; down 4; up 4; down 4; up 4"},
		{"right", 2, 0, "move 10,10; down 7; up 7; down 7; up 7"},
		{"left", -1, 0, "move 10,10; down 6; up 6"},
		{"vertical first, then horizontal", 1, 1, "move 10,10; down 5; up 5; down 7; up 7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newInputRig()
			if _, err := r.run(t, protocol.InputEvent{Type: "scroll", X: 10, Y: 10, DX: tt.dx, DY: tt.dy}); err != nil {
				t.Fatal(err)
			}
			if got := r.without(); got != tt.want {
				t.Errorf("%s, want %s", got, tt.want)
			}
		})
	}
}

func TestInputDragGoesInStepsOverAboutTwoHundredMilliseconds(t *testing.T) {
	r := newInputRig()
	r.in.MaxSettle = 0 // measure the gesture alone
	if _, err := r.run(t, protocol.InputEvent{Type: "drag", X: 100, Y: 100, DragTo: &protocol.Point{X: 300, Y: 200}}); err != nil {
		t.Fatal(err)
	}
	var want []string
	want = append(want, "move 100,100", "down 1")
	for i := 1; i <= 10; i++ {
		want = append(want, fmt.Sprintf("move %d,%d", 100+20*i, 100+10*i))
	}
	want = append(want, "up 1")
	if got := r.without(); got != strings.Join(want, "; ") {
		t.Errorf("%s", got)
	}
	// The gesture itself takes 200 ms of steps plus a short pause at each end.
	if gesture := r.total() - r.in.Settle; gesture < 200*time.Millisecond || gesture > 400*time.Millisecond {
		t.Errorf("a drag takes about 200 ms in steps: %v", gesture)
	}
	// A right-button drag.
	r = newInputRig()
	_, _ = r.run(t, protocol.InputEvent{Type: "drag", X: 1, Y: 1, Button: "right", DragTo: &protocol.Point{X: 5, Y: 5}})
	if !strings.Contains(r.without(), "down 3") || !strings.HasSuffix(r.without(), "up 3") {
		t.Errorf("%s", r.without())
	}
}

func TestADragNeverLeavesTheButtonDown(t *testing.T) {
	r := newInputRig()
	r.dev.failOn = "move 140,120" // the second step of the drag fails
	_, err := r.run(t, protocol.InputEvent{Type: "drag", X: 100, Y: 100, DragTo: &protocol.Point{X: 300, Y: 200}})
	if code(err) != protocol.CodeInternal || !strings.Contains(err.Error(), "BadAccess") {
		t.Fatalf("%v", err)
	}
	if !strings.HasSuffix(r.without(), "up 1") {
		t.Errorf("the button is released: %s", r.without())
	}
	// A click whose release is refused still reports it.
	r = newInputRig()
	r.dev.failOn = "up 1"
	if _, err := r.run(t, protocol.InputEvent{Type: "click", X: 1, Y: 1}); err == nil {
		t.Error("a failed release is an error")
	}
	// A click interrupted after the press releases the button before it gives up.
	r = newInputRig()
	r.dev.failOn = "move 1,1"
	if _, err := r.run(t, protocol.InputEvent{Type: "click", X: 1, Y: 1}); err == nil {
		t.Error("a failed move is an error")
	}
}

func TestInputKeysAndText(t *testing.T) {
	r := newInputRig()
	_, err := r.run(t,
		protocol.InputEvent{Type: "key", Keys: "ctrl+l"},
		protocol.InputEvent{Type: "key", Keys: "Down", Repeat: 5},
		protocol.InputEvent{Type: "text", Text: "héllo 日本"},
		protocol.InputEvent{Type: "key", Keys: "Return"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.without(), "keys ctrl+l x1; keys Down x5; type héllo 日本; keys Return x1"; got != want {
		t.Errorf("%s, want %s", got, want)
	}
}

func TestInputEventsRunInOrderAndEndWithASync(t *testing.T) {
	r := newInputRig()
	_, _ = r.run(t, protocol.InputEvent{Type: "move", X: 1, Y: 1}, protocol.InputEvent{Type: "key", Keys: "a"}, protocol.InputEvent{Type: "move", X: 2, Y: 2})
	if got, want := strings.Join(r.dev.ops, "; "), "move 1,1; keys a x1; move 2,2; sync"; got != want {
		t.Errorf("%s, want %s", got, want)
	}
}

func TestInputValidationRefusesBeforeDoingAnything(t *testing.T) {
	many := make([]protocol.InputEvent, 65)
	for i := range many {
		many[i] = protocol.InputEvent{Type: "move", X: 1, Y: 1}
	}
	tests := []struct {
		name   string
		events []protocol.InputEvent
		want   string
	}{
		{"no events", nil, "empty"},
		{"too many events", many, "65 events"},
		{"x on the edge of the screen", []protocol.InputEvent{{Type: "move", X: 1280, Y: 0}}, "outside the 1280x800 screen"},
		{"y on the edge", []protocol.InputEvent{{Type: "click", X: 0, Y: 800}}, "outside"},
		{"negative", []protocol.InputEvent{{Type: "move", X: -1, Y: 5}}, "outside"},
		{"unknown type", []protocol.InputEvent{{Type: "tap"}}, "not move, down"},
		{"empty type", []protocol.InputEvent{{}}, "not move, down"},
		{"unknown button", []protocol.InputEvent{{Type: "click", Button: "back"}}, "not left, middle or right"},
		{"wheel as a button", []protocol.InputEvent{{Type: "click", Button: "wheel-up"}}, "not left, middle or right"},
		{"double on a move", []protocol.InputEvent{{Type: "down", X: 1, Y: 1, Double: true}}, "clicks only"},
		{"scroll by nothing", []protocol.InputEvent{{Type: "scroll", X: 1, Y: 1}}, "both zero"},
		{"scroll too far", []protocol.InputEvent{{Type: "scroll", X: 1, Y: 1, DY: 21}}, "at most 20"},
		{"scroll too far left", []protocol.InputEvent{{Type: "scroll", X: 1, Y: 1, DX: -21}}, "at most 20"},
		{"drag without a target", []protocol.InputEvent{{Type: "drag", X: 1, Y: 1}}, "drag_to is required"},
		{"drag off the screen", []protocol.InputEvent{{Type: "drag", X: 1, Y: 1, DragTo: &protocol.Point{X: 5000, Y: 1}}}, "drag_to"},
		{"key without keys", []protocol.InputEvent{{Type: "key"}}, "keys is required"},
		{"key repeated too often", []protocol.InputEvent{{Type: "key", Keys: "a", Repeat: 21}}, "repeat"},
		{"negative repeat", []protocol.InputEvent{{Type: "key", Keys: "a", Repeat: -1}}, "repeat"},
		{"two keys in one combination", []protocol.InputEvent{{Type: "key", Keys: "a+b"}}, "more than one key"},
		{"a key that does not exist", []protocol.InputEvent{{Type: "key", Keys: "Foo"}}, "not a key name"},
		{"empty text", []protocol.InputEvent{{Type: "text"}}, "text is empty"},
		{"a control character in the middle of the text", []protocol.InputEvent{{Type: "text", Text: "typed\x07not"}}, "control character"},
		{"escape in text", []protocol.InputEvent{{Type: "text", Text: "\x1b[2J"}}, "control character"},
		{"too much text in total", []protocol.InputEvent{{Type: "text", Text: strings.Repeat("a", 3000)}, {Type: "text", Text: strings.Repeat("b", 1500)}}, "more than 4000"},
		{"a good event, then a bad one", []protocol.InputEvent{{Type: "move", X: 1, Y: 1}, {Type: "move", X: 9999, Y: 1}}, "event 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newInputRig()
			_, err := r.run(t, tt.events...)
			if code(err) != protocol.CodeInvalidArgs || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
			if len(r.dev.ops) != 0 {
				t.Errorf("a refused request does nothing at all, even the events before the bad one: %v", r.dev.ops)
			}
		})
	}
	// An unknown field and a missing body.
	r := newInputRig()
	if _, _, err := r.in.Handle(context.Background(), frame(t, protocol.TypeInput, map[string]any{"events": []any{}, "root": true})); code(err) != protocol.CodeInvalidArgs {
		t.Errorf("%v", err)
	}
}

func TestInputEdgeOfTheScreenIsInside(t *testing.T) {
	r := newInputRig()
	if _, err := r.run(t, protocol.InputEvent{Type: "move", X: 0, Y: 0}, protocol.InputEvent{Type: "move", X: 1279, Y: 799}); err != nil {
		t.Errorf("the corners are on the screen: %v", err)
	}
	r = newInputRig()
	r.dev.w, r.dev.h = 640, 480 // the screen is checked as it is now
	if _, err := r.run(t, protocol.InputEvent{Type: "move", X: 700, Y: 10}); code(err) != protocol.CodeInvalidArgs {
		t.Errorf("%v", err)
	}
	r = newInputRig()
	r.dev.sizeErr = errors.New("BadWindow")
	if _, err := r.run(t, protocol.InputEvent{Type: "move"}); code(err) != protocol.CodeInternal {
		t.Errorf("%v", err)
	}
}

func TestInputDeviceErrors(t *testing.T) {
	r := newInputRig()
	r.dev.failOn = "move 2,2"
	_, err := r.run(t, protocol.InputEvent{Type: "move", X: 1, Y: 1}, protocol.InputEvent{Type: "move", X: 2, Y: 2}, protocol.InputEvent{Type: "move", X: 3, Y: 3})
	if code(err) != protocol.CodeInternal || !strings.Contains(err.Error(), "event 2 (move) failed after 1 earlier ones ran") {
		t.Errorf("the host is told how far it got: %v", err)
	}
	if r.without() != "move 1,1" {
		t.Errorf("nothing runs after a failure: %s", r.without())
	}
	// Key names the keyboard rejects at run time are the caller's mistake.
	r = newInputRig()
	r.dev.failOn = "keys F24 x1"
	r.dev.failErr = &x11.KeyError{Msg: "this keyboard has no key for keysym 0xffd5"}
	_, err = r.run(t, protocol.InputEvent{Type: "key", Keys: "F24"})
	if code(err) != protocol.CodeInvalidArgs || !strings.Contains(err.Error(), "no key") {
		t.Errorf("%v", err)
	}
	// A failed sync is reported too.
	r = newInputRig()
	r.dev.failOn = "sync"
	if _, err := r.run(t, protocol.InputEvent{Type: "move", X: 1, Y: 1}); code(err) != protocol.CodeInternal {
		t.Errorf("%v", err)
	}
}

func TestInputStopsBetweenEventsWhenCanceled(t *testing.T) {
	r := newInputRig()
	ctx, cancel := context.WithCancel(context.Background())
	r.dev.onOp = func(op string) {
		if op == "move 1,1" {
			cancel()
		}
	}
	_, _, err := r.in.Handle(ctx, frame(t, protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{
		{Type: "move", X: 1, Y: 1}, {Type: "move", X: 2, Y: 2}}}))
	if !errors.Is(err, context.Canceled) || r.without() != "move 1,1" {
		t.Errorf("%v %s", err, r.without())
	}
}

// --- the hash after the input ------------------------------------------------------------------------------------

// changing makes the screen different after the n-th capture.
func (r *inputRig) changesAfter(n int) {
	base := r.scr.img
	changed := image.NewRGBA(base.Bounds())
	copy(changed.Pix, base.Pix)
	changed.SetRGBA(3, 3, color.RGBA{R: 255, A: 255})
	count := 0
	r.scr.onCapture = func() {
		count++
		if count > n {
			r.scr.img = changed
		}
	}
	r.scr.captures = 0
}

func TestInputReportsTheScreenAfterWaitingForItToChange(t *testing.T) {
	r := newInputRig()
	before := r.in.Hasher.Hash(r.scr.img)
	r.changesAfter(3) // captures: 1 before, 2 after the settle, 3 after 100 ms, then it changes
	res, err := r.run(t, protocol.InputEvent{Type: "click", X: 1, Y: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.ScreenHashAfter == "" || res.ScreenHashAfter == before {
		t.Errorf("the hash must be the changed screen's: %q (before %q)", res.ScreenHashAfter, before)
	}
	if !slices.Contains(r.slept, 100*time.Millisecond) {
		t.Errorf("an unchanged screen is polled again: %v", r.slept)
	}
	// The hash is the same kind the screenshot handler reports.
	if want := r.in.Hasher.Hash(r.scr.img); res.ScreenHashAfter != want {
		t.Errorf("%s != %s", res.ScreenHashAfter, want)
	}
}

func TestInputWaitsOnlyAsLongAsItShouldForAScreenThatNeverChanges(t *testing.T) {
	r := newInputRig()
	res, err := r.run(t, protocol.InputEvent{Type: "key", Keys: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ScreenHashAfter != r.in.Hasher.Hash(r.scr.img) {
		t.Error("an unchanged screen still has a hash: the host sees that nothing happened")
	}
	if got := r.total(); got != r.in.Settle+r.in.MaxSettle {
		t.Errorf("settle %v plus at most %v more, waited %v", r.in.Settle, r.in.MaxSettle, got)
	}
}

func TestInputDoesNotWaitWhenTheScreenChangedAtOnce(t *testing.T) {
	r := newInputRig()
	r.changesAfter(1)
	if _, err := r.run(t, protocol.InputEvent{Type: "key", Keys: "a"}); err != nil {
		t.Fatal(err)
	}
	if got := r.total(); got != r.in.Settle {
		t.Errorf("waited %v, the settle time is %v", got, r.in.Settle)
	}
}

func TestInputWithoutAReadableScreen(t *testing.T) {
	r := newInputRig()
	r.scr.capErr = errors.New("BadMatch")
	res, err := r.run(t, protocol.InputEvent{Type: "click", X: 1, Y: 1})
	if err != nil || res.ScreenHashAfter != "" {
		t.Errorf("the input worked even though the screen cannot be read: %v %+v", err, res)
	}
	if slices.Contains(r.slept, 100*time.Millisecond) {
		t.Errorf("no polling for a screen that cannot be read: %v", r.slept)
	}
}

func TestInputRefusalsCarryTheProtocolCodes(t *testing.T) {
	var he *server.HandlerError
	r := newInputRig()
	_, err := r.run(t)
	if !errors.As(err, &he) || he.Code != protocol.CodeInvalidArgs {
		t.Errorf("%v", err)
	}
}
