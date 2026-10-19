// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack/internal/llm/vision"
	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/viewer"
	"github.com/PacifAIst/controlsnack/internal/vm/agentclient"
	"github.com/PacifAIst/controlsnack/internal/vm/qmp"
)

// fakeMonitor is QEMU's monitor: it writes a picture when asked for the screen and keeps the input it is given.
type fakeMonitor struct {
	mu      sync.Mutex
	w, h    int
	shade   uint8 // the screen's color; changing it changes the screen
	dumps   []string
	batches [][]qmp.InputEvent
	dumpErr error
	inErr   error
	raw     []byte // what to write instead of a picture
}

func (m *fakeMonitor) Screendump(_ context.Context, path, format string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dumps = append(m.dumps, path)
	if m.dumpErr != nil {
		return m.dumpErr
	}
	if format != "png" {
		return fmt.Errorf("format %s", format)
	}
	if m.raw != nil {
		return os.WriteFile(path, m.raw, 0o600)
	}
	img := image.NewRGBA(image.Rect(0, 0, m.w, m.h))
	for y := 0; y < m.h; y++ {
		for x := 0; x < m.w; x++ {
			img.Set(x, y, color.RGBA{m.shade, uint8(x), uint8(y), 255})
		}
	}
	f, err := os.Create(path) //nolint:gosec // a test file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}

func (m *fakeMonitor) InputSendEvent(_ context.Context, evs []qmp.InputEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inErr != nil {
		return m.inErr
	}
	m.batches = append(m.batches, append([]qmp.InputEvent(nil), evs...))
	return nil
}

// events are all the input events given so far, one per line in the words of the test: "abs x 100", "btn left down", "key ctrl down".
func (m *fakeMonitor) events() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, b := range m.batches {
		for _, e := range b {
			d, _ := e.Data.(map[string]any)
			switch e.Type {
			case "abs":
				out = append(out, fmt.Sprintf("abs %v %v", d["axis"], d["value"]))
			case "btn":
				out = append(out, fmt.Sprintf("btn %v %s", d["button"], upDown(d["down"])))
			case "key":
				k, _ := d["key"].(map[string]any)
				out = append(out, fmt.Sprintf("key %v %s", k["data"], upDown(d["down"])))
			}
		}
	}
	return out
}

func upDown(v any) string {
	if b, _ := v.(bool); b {
		return "down"
	}
	return "up"
}

func newFallback(t *testing.T) (*Fallback, *fakeMonitor) {
	t.Helper()
	m := &fakeMonitor{w: 200, h: 100, shade: 10}
	f := NewFallback(m, t.TempDir(), prompt.Size{W: 200, H: 100})
	f.Sleep = func(time.Duration) {}
	return f, m
}

func shot(t *testing.T, f *Fallback, req protocol.ScreenshotReq) (protocol.ScreenshotRes, []byte) {
	t.Helper()
	var res protocol.ScreenshotRes
	blob, err := f.Call(context.Background(), protocol.TypeScreenshot, req, nil, &res)
	if err != nil {
		t.Fatal(err)
	}
	return res, blob
}

func TestTheFallbackTakesThePictureThroughTheMonitorInTheFormatAndSizeAsked(t *testing.T) {
	f, m := newFallback(t)
	res, blob := shot(t, f, protocol.ScreenshotReq{Format: "png"})
	if res.W != 200 || res.H != 100 || res.Format != "png" || res.Scale != 1 || len(res.Hash) != 16 || res.Unchanged || len(blob) == 0 {
		t.Fatalf("result %+v", res)
	}
	if err := imaging.Inspect(blob, imaging.PNG, res.W, res.H); err != nil {
		t.Errorf("the picture: %v", err)
	}
	// Smaller, in another format.
	res, blob = shot(t, f, protocol.ScreenshotReq{Format: "jpeg", MaxLongEdge: 100, Quality: 70})
	if res.W != 100 || res.H != 50 || res.Format != "jpeg" || res.Scale != 0.5 {
		t.Fatalf("scaled result %+v", res)
	}
	if err := imaging.Inspect(blob, imaging.JPEG, res.W, res.H); err != nil {
		t.Errorf("the scaled picture: %v", err)
	}
	// WebP is the default.
	res, blob = shot(t, f, protocol.ScreenshotReq{})
	if res.Format != "webp" || imaging.Inspect(blob, imaging.WebP, res.W, res.H) != nil {
		t.Errorf("default %+v", res)
	}
	// The files the monitor wrote are gone.
	entries, _ := os.ReadDir(f.Dir)
	if len(entries) != 0 || len(m.dumps) != 3 {
		t.Errorf("%d files left, %d dumps", len(entries), len(m.dumps))
	}
}

func TestAScreenThatHasNotChangedIsSaidToHaveNotChanged(t *testing.T) {
	f, m := newFallback(t)
	first, _ := shot(t, f, protocol.ScreenshotReq{Format: "png"})
	res, blob := shot(t, f, protocol.ScreenshotReq{Format: "png", IfChangedSince: first.Hash})
	if !res.Unchanged || res.Hash != first.Hash || len(blob) != 0 {
		t.Errorf("same screen: %+v (%d bytes)", res, len(blob))
	}
	m.shade = 99
	res, blob = shot(t, f, protocol.ScreenshotReq{Format: "png", IfChangedSince: first.Hash})
	if res.Unchanged || res.Hash == first.Hash || len(blob) == 0 {
		t.Errorf("changed screen: %+v", res)
	}
}

func TestAPictureTheMonitorCannotGiveIsAnError(t *testing.T) {
	f, m := newFallback(t)
	var res protocol.ScreenshotRes
	m.dumpErr = errors.New("qemu says no")
	if _, err := f.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res); err == nil || !strings.Contains(err.Error(), "qemu says no") {
		t.Errorf("error %v", err)
	}
	m.dumpErr, m.raw = nil, []byte("not a picture")
	if _, err := f.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res); err == nil || !strings.Contains(err.Error(), "not a picture") {
		t.Errorf("error %v", err)
	}
	m.raw = nil
	_, err := f.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "bmp"}, nil, &res)
	var re *agentclient.RemoteError
	if !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs {
		t.Errorf("format: %v", err)
	}
	if _, err := f.Call(context.Background(), protocol.TypeScreenshot, 5, nil, &res); err == nil {
		t.Error("a request in a form it does not know was answered")
	}
	if _, err := f.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{}, nil, nil); err == nil {
		t.Error("an answer nowhere to go was accepted")
	}
}

func TestTheFallbackKnowsTheScreensSizeAndWhereTheMouseWent(t *testing.T) {
	f, m := newFallback(t)
	var info protocol.ScreenInfoRes
	if _, err := f.Call(context.Background(), protocol.TypeScreenInfo, struct{}{}, nil, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Monitors) != 1 || info.Monitors[0].Bounds != (protocol.Rect{0, 0, 200, 100}) || !info.Monitors[0].Primary || info.Cursor != (protocol.Point{}) {
		t.Errorf("info %+v", info)
	}
	req := protocol.InputReq{Events: []protocol.InputEvent{{Type: "move", X: 120, Y: 40}}}
	if _, err := f.Call(context.Background(), protocol.TypeInput, req, nil, &protocol.InputRes{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Call(context.Background(), protocol.TypeScreenInfo, struct{}{}, nil, &info); err != nil {
		t.Fatal(err)
	}
	if info.Cursor != (protocol.Point{X: 120, Y: 40}) {
		t.Errorf("cursor %+v", info.Cursor)
	}
	res, _ := shot(t, f, protocol.ScreenshotReq{Format: "png"})
	if res.Cursor != (protocol.Point{X: 120, Y: 40}) {
		t.Errorf("cursor in the picture's answer %+v", res.Cursor)
	}
	// With no size from the handshake the first picture says it.
	f2 := NewFallback(m, t.TempDir(), prompt.Size{})
	m.w, m.h = 64, 48
	if _, err := f2.Call(context.Background(), protocol.TypeScreenInfo, struct{}{}, nil, &info); err != nil || info.Monitors[0].Bounds != (protocol.Rect{0, 0, 64, 48}) {
		t.Errorf("size from a picture: %+v %v", info, err)
	}
	m.dumpErr = errors.New("gone")
	f3 := NewFallback(m, t.TempDir(), prompt.Size{})
	if _, err := f3.Call(context.Background(), protocol.TypeScreenInfo, struct{}{}, nil, &info); err == nil {
		t.Error("a screen that cannot be seen has a size")
	}
	if _, err := f.Call(context.Background(), protocol.TypeScreenInfo, struct{}{}, nil, 5); err == nil {
		t.Error("an answer nowhere to go was accepted")
	}
}

func inputOK(t *testing.T, f *Fallback, evs ...protocol.InputEvent) protocol.InputRes {
	t.Helper()
	var res protocol.InputRes
	if _, err := f.Call(context.Background(), protocol.TypeInput, protocol.InputReq{Events: evs}, nil, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMouseInputBecomesThePointerAndButtonsOfTheMonitor(t *testing.T) {
	f, m := newFallback(t)
	for _, tc := range []struct {
		name string
		ev   protocol.InputEvent
		want string
	}{
		{"move", protocol.InputEvent{Type: "move", X: 0, Y: 0}, "abs x 0|abs y 0"},
		{"move to the far corner", protocol.InputEvent{Type: "move", X: 199, Y: 99}, "abs x 32767|abs y 32767"},
		{"outside the screen is clamped", protocol.InputEvent{Type: "move", X: 5000, Y: -4}, "abs x 32767|abs y 0"},
		{"click", protocol.InputEvent{Type: "click", X: 0, Y: 0}, "abs x 0|abs y 0|btn left down|btn left up"},
		{"right click", protocol.InputEvent{Type: "click", X: 0, Y: 0, Button: "right"}, "abs x 0|abs y 0|btn right down|btn right up"},
		{"middle click", protocol.InputEvent{Type: "click", X: 0, Y: 0, Button: "middle"}, "abs x 0|abs y 0|btn middle down|btn middle up"},
		{"double click", protocol.InputEvent{Type: "click", X: 0, Y: 0, Double: true}, "abs x 0|abs y 0|btn left down|btn left up|btn left down|btn left up"},
		{"press", protocol.InputEvent{Type: "down", X: 0, Y: 0}, "abs x 0|abs y 0|btn left down"},
		{"release", protocol.InputEvent{Type: "up", X: 0, Y: 0}, "abs x 0|abs y 0|btn left up"},
		{"scroll down", protocol.InputEvent{Type: "scroll", X: 0, Y: 0, DY: 2}, "abs x 0|abs y 0|btn wheel-down down|btn wheel-down up|btn wheel-down down|btn wheel-down up"},
		{"scroll up", protocol.InputEvent{Type: "scroll", X: 0, Y: 0, DY: -1}, "abs x 0|abs y 0|btn wheel-up down|btn wheel-up up"},
		{"scroll right", protocol.InputEvent{Type: "scroll", X: 0, Y: 0, DX: 1}, "abs x 0|abs y 0|btn wheel-right down|btn wheel-right up"},
		{"scroll left", protocol.InputEvent{Type: "scroll", X: 0, Y: 0, DX: -1}, "abs x 0|abs y 0|btn wheel-left down|btn wheel-left up"},
	} {
		m.mu.Lock()
		m.batches = nil
		m.mu.Unlock()
		inputOK(t, f, tc.ev)
		if got := strings.Join(m.events(), "|"); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestADragPressesMovesInStepsAndReleases(t *testing.T) {
	f, m := newFallback(t)
	var slept []time.Duration
	f.Sleep = func(d time.Duration) { slept = append(slept, d) }
	inputOK(t, f, protocol.InputEvent{Type: "drag", X: 0, Y: 0, DragTo: &protocol.Point{X: 100, Y: 50}})
	ev := m.events()
	if len(ev) != 2+1+10*2+1 || ev[2] != "btn left down" || ev[len(ev)-1] != "btn left up" {
		t.Fatalf("events %v", ev)
	}
	if ev[len(ev)-3] != "abs x "+fmt.Sprint((100*32767+99/2)/199) || ev[len(ev)-2][:5] != "abs y" {
		t.Errorf("the last step: %v", ev[len(ev)-3:])
	}
	steps := 0
	for _, d := range slept {
		if d == fallbackDragStep {
			steps++
		}
	}
	if steps != 10 {
		t.Errorf("%d pauses between steps", steps)
	}
	var re *agentclient.RemoteError
	var res protocol.InputRes
	_, err := f.Call(context.Background(), protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{{Type: "drag", X: 1, Y: 1}}}, nil, &res)
	if !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs || !strings.Contains(re.Message, "event 1 (drag)") {
		t.Errorf("a drag to nowhere: %v", err)
	}
}

func TestKeysBecomeKeyPressesOnAUSKeyboard(t *testing.T) {
	f, m := newFallback(t)
	for _, tc := range []struct{ keys, want string }{
		{"a", "key a down|key a up"},
		{"A", "key shift down|key a down|key a up|key shift up"},
		{"ctrl+l", "key ctrl down|key l down|key l up|key ctrl up"},
		{"ctrl+shift+t", "key ctrl down|key shift down|key t down|key t up|key shift up|key ctrl up"},
		{"alt+F4", "key alt down|key f4 down|key f4 up|key alt up"},
		{"Return", "key ret down|key ret up"},
		{"shift+Delete", "key shift down|key delete down|key delete up|key shift up"},
		{"ctrl+shift+A", "key ctrl down|key shift down|key a down|key a up|key shift up|key ctrl up"},
		{"super", "key meta_l down|key meta_l up"},
		{"ctrl+plus", "key ctrl down|key shift down|key equal down|key equal up|key shift up|key ctrl up"},
		{"Page_Down", "key pgdn down|key pgdn up"},
		{"kp_5", "key kp_5 down|key kp_5 up"},
		{"space", "key spc down|key spc up"},
		{"slash", "key slash down|key slash up"},
		{"?", "key shift down|key slash down|key slash up|key shift up"},
	} {
		m.mu.Lock()
		m.batches = nil
		m.mu.Unlock()
		inputOK(t, f, protocol.InputEvent{Type: "key", Keys: tc.keys})
		if got := strings.Join(m.events(), "|"); got != tc.want {
			t.Errorf("%q:\n got %s\nwant %s", tc.keys, got, tc.want)
		}
	}
	m.mu.Lock()
	m.batches = nil
	m.mu.Unlock()
	inputOK(t, f, protocol.InputEvent{Type: "key", Keys: "Tab", Repeat: 3})
	if got := strings.Join(m.events(), "|"); got != strings.Repeat("key tab down|key tab up|", 3)[:len(strings.Repeat("key tab down|key tab up|", 3))-1] {
		t.Errorf("repeated: %s", got)
	}
}

func TestTextIsTypedOneCharacterAtATime(t *testing.T) {
	f, m := newFallback(t)
	inputOK(t, f, protocol.InputEvent{Type: "text", Text: "Hi!\n\t"})
	want := "key shift down|key h down|key h up|key shift up|" +
		"key i down|key i up|" +
		"key shift down|key 1 down|key 1 up|key shift up|" +
		"key ret down|key ret up|key tab down|key tab up"
	if got := strings.Join(m.events(), "|"); got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	// A long text goes in several calls to the monitor, never more than it takes at once.
	m.mu.Lock()
	m.batches = nil
	m.mu.Unlock()
	inputOK(t, f, protocol.InputEvent{Type: "text", Text: strings.Repeat("ab", 100)})
	m.mu.Lock()
	var sizes []int
	for _, b := range m.batches {
		sizes = append(sizes, len(b))
	}
	m.mu.Unlock()
	if len(sizes) != 2 || sizes[0] != 256 || sizes[1] != 400-256 {
		t.Errorf("batches of %v", sizes)
	}
}

func TestWhatTheFallbackCannotTypeOrDoIsTheModelsToCorrect(t *testing.T) {
	f, m := newFallback(t)
	for name, ev := range map[string]protocol.InputEvent{
		"a character with no key":    {Type: "text", Text: "caf\u00e9"},
		"too much text":              {Type: "text", Text: strings.Repeat("x", maxTypedRunes+1)},
		"a key that is not a name":   {Type: "key", Keys: "banana"},
		"an empty combination":       {Type: "key", Keys: ""},
		"a key with no key for it":   {Type: "key", Keys: "\u00e9"},
		"a mouse button not known":   {Type: "click", Button: "side"},
		"an input it does not know":  {Type: "wiggle"},
		"a keypad key not on the pc": {Type: "key", Keys: "ctrl+\u20ac"},
	} {
		var res protocol.InputRes
		_, err := f.Call(context.Background(), protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{ev}}, nil, &res)
		var re *agentclient.RemoteError
		if !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs || !strings.Contains(re.Message, "event 1 (") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(m.events()) != 0 {
		t.Errorf("something was sent: %v", m.events())
	}
	// A refusal in the middle says which event and how many ran.
	_, err := f.Call(context.Background(), protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{{Type: "move", X: 1, Y: 1}, {Type: "key", Keys: "banana"}}}, nil, &protocol.InputRes{})
	var re *agentclient.RemoteError
	if !errors.As(err, &re) || !strings.Contains(re.Message, "event 2 (key)") {
		t.Errorf("error %v", err)
	}
}

func TestInputThatTheMonitorRefusesIsAnErrorThatSaysHowFarItGot(t *testing.T) {
	f, m := newFallback(t)
	m.inErr = errors.New("qemu says no")
	var res protocol.InputRes
	_, err := f.Call(context.Background(), protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{{Type: "key", Keys: "a"}}}, nil, &res)
	if err == nil || !strings.Contains(err.Error(), "event 1 (key) failed after 0 earlier ones ran") || !strings.Contains(err.Error(), "qemu says no") {
		t.Errorf("error %v", err)
	}
	m.inErr = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Call(ctx, protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{{Type: "key", Keys: "a"}}}, nil, &res); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled request: %v", err)
	}
	m.dumpErr = errors.New("no screen")
	f2 := NewFallback(m, t.TempDir(), prompt.Size{})
	if _, err := f2.Call(context.Background(), protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{{Type: "key", Keys: "a"}}}, nil, &res); err == nil {
		t.Error("input to a screen of unknown size")
	}
}

func TestInputGivesTheHashOfTheScreenAfterItAndWaitsForItToShow(t *testing.T) {
	f, m := newFallback(t)
	var slept []time.Duration
	f.Sleep = func(d time.Duration) { slept = append(slept, d) }
	res := inputOK(t, f, protocol.InputEvent{Type: "key", Keys: "a"})
	if len(res.ScreenHashAfter) != 16 {
		t.Errorf("hash %q", res.ScreenHashAfter)
	}
	if len(slept) != 1 || slept[0] != fallbackSettle {
		t.Errorf("slept %v", slept)
	}
	before, _ := shot(t, f, protocol.ScreenshotReq{Format: "png"})
	if before.Hash != res.ScreenHashAfter {
		t.Errorf("the hash after input, %q, is not the hash of the same screen, %q", res.ScreenHashAfter, before.Hash)
	}
	m.shade = 200
	if res2 := inputOK(t, f, protocol.InputEvent{Type: "key", Keys: "a"}); res2.ScreenHashAfter == before.Hash {
		t.Error("a screen that changed has the same hash")
	}
	// Asked for no answer it does not take the picture.
	n := len(m.dumps)
	if _, err := f.Call(context.Background(), protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{{Type: "key", Keys: "a"}}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.dumps) != n {
		t.Error("a picture was taken for nobody")
	}
}

func TestWhatOnlyTheAgentCanDoIsUnreachableAndWhatItReadsIsUnsupported(t *testing.T) {
	f, _ := newFallback(t)
	for _, typ := range []string{protocol.TypeExec, protocol.TypeBrowser, protocol.TypeWindows, protocol.TypeWaitFor, protocol.TypeFilePeek, protocol.TypeA11yAction} {
		if _, err := f.Call(context.Background(), typ, struct{}{}, nil, nil); !errors.Is(err, agentclient.ErrAgentDown) {
			t.Errorf("%s: %v", typ, err)
		}
	}
	for _, typ := range []string{protocol.TypeA11yTree, protocol.TypeOCR} {
		_, err := f.Call(context.Background(), typ, struct{}{}, nil, nil)
		var re *agentclient.RemoteError
		if !errors.As(err, &re) || re.Code != protocol.CodeUnsupported || !strings.Contains(re.Message, "not answering") {
			t.Errorf("%s: %v", typ, err)
		}
	}
}

func TestEveryKeyTheAgentsNamesGiveHasAKeyOnTheFallbacksKeyboardOrIsSaidNotTo(t *testing.T) {
	for _, tc := range []struct {
		ks    uint32
		code  string
		shift bool
		ok    bool
	}{
		{'a', "a", false, true}, {'Z', "z", true, true}, {'5', "5", false, true}, {' ', "spc", false, true},
		{'-', "minus", false, true}, {'_', "minus", true, true}, {'@', "2", true, true}, {'~', "grave_accent", true, true},
		{ksReturn, "ret", false, true}, {ksF1, "f1", false, true}, {ksF1 + 11, "f12", false, true}, {ksF1 + 23, "f24", false, true}, {ksF1 + 24, "", false, false},
		{ksKP0 + 7, "kp_7", false, true}, {ksISOLevel3, "alt_r", false, true}, {0xe9, "", false, false}, {0x01000000 + 0x20ac, "", false, false},
		{ksSuperL, "meta_l", false, true}, {ksPageUp, "pgup", false, true}, {ksCapsLock, "caps_lock", false, true},
	} {
		code, shift, ok := qcodeOf(tc.ks)
		if code != tc.code || shift != tc.shift || ok != tc.ok {
			t.Errorf("%#x: %q %v %v, want %q %v %v", tc.ks, code, shift, ok, tc.code, tc.shift, tc.ok)
		}
	}
	// Every character of the keyboard has a key whose name the monitor accepts.
	for r := rune(0x20); r < 0x7f; r++ {
		code, _, ok := qcodeOf(uint32(r))
		if !ok {
			t.Errorf("%q has no key", r)
			continue
		}
		if _, err := qmp.Key(code, true); err != nil {
			t.Errorf("%q: %v", r, err)
		}
	}
}

// fakeAgent is the agent behind a Link: answering, or not.
type linkAgent struct {
	mu    sync.Mutex
	up    bool
	err   error
	calls []string
}

func (a *linkAgent) Call(_ context.Context, typ string, _ any, _ []byte, _ any) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, typ)
	return []byte("from the agent"), a.err
}

func (a *linkAgent) Up() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.up }

func (a *linkAgent) set(up bool, err error) { a.mu.Lock(); a.up, a.err = up, err; a.mu.Unlock() }

func (e *events) agents() []viewer.Agent {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []viewer.Agent
	for _, m := range e.msgs {
		if c, ok := m.(viewer.Agent); ok {
			out = append(out, c)
		}
	}
	return out
}

func newLink(t *testing.T, withFallback bool) (*Link, *linkAgent, *fakeMonitor, *events, *log) {
	t.Helper()
	ag := &linkAgent{up: true}
	view, lg := &events{}, &log{}
	l := &Link{Agent: ag, Viewer: view, Audit: lg}
	var m *fakeMonitor
	if withFallback {
		var f *Fallback
		f, m = newFallback(t)
		l.Fallback = f
	}
	return l, ag, m, view, lg
}

func TestWhileTheAgentAnswersEverythingGoesToIt(t *testing.T) {
	l, ag, m, view, lg := newLink(t, true)
	out, err := l.Call(context.Background(), protocol.TypeExec, struct{}{}, nil, nil)
	if err != nil || string(out) != "from the agent" || len(ag.calls) != 1 {
		t.Fatalf("%q %v %v", out, err, ag.calls)
	}
	// An error of the agent's own is the agent's answer, not a reason to use the monitor.
	ag.set(true, &agentclient.RemoteError{Code: protocol.CodeNotFound, Message: "no such element"})
	if _, err := l.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{}, nil, &protocol.ScreenshotRes{}); err == nil {
		t.Error("the agent's error was lost")
	}
	if len(m.dumps) != 0 || len(view.agents()) != 0 || len(lg.all()) != 0 {
		t.Errorf("the fallback or the viewer was involved: %d dumps, %v, %d records", len(m.dumps), view.agents(), len(lg.all()))
	}
}

func TestWhenTheAgentGoesTheMonitorTakesOverAndThePersonIsToldOnce(t *testing.T) {
	l, ag, m, view, lg := newLink(t, true)
	if _, err := l.Call(context.Background(), protocol.TypeExec, struct{}{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	ag.set(false, nil)
	var res protocol.ScreenshotRes
	for i := 0; i < 3; i++ {
		if _, err := l.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.dumps) != 3 || len(ag.calls) != 1 {
		t.Errorf("%d dumps, %d agent calls", len(m.dumps), len(ag.calls))
	}
	if ags := view.agents(); len(ags) != 1 || ags[0].Online || !strings.Contains(ags[0].Detail, "not answering") {
		t.Errorf("agent messages %+v", ags)
	}
	ns := view.notices()
	if len(ns) != 1 || ns[0].Level != viewer.LevelWarn || !strings.Contains(ns[0].Text, "mouse and the keyboard only") {
		t.Errorf("notices %+v", ns)
	}
	if all := lg.all(); len(all) != 1 || all[0].Kind != "lifecycle" || all[0].Event != "agent_down" || all[0].Actor != "system" {
		t.Errorf("records %+v", all)
	}
	// What only the agent can do is unreachable.
	if _, err := l.Call(context.Background(), protocol.TypeExec, struct{}{}, nil, nil); !errors.Is(err, agentclient.ErrAgentDown) {
		t.Errorf("exec: %v", err)
	}
	// And the agent comes back.
	ag.set(true, nil)
	if out, err := l.Call(context.Background(), protocol.TypeExec, struct{}{}, nil, nil); err != nil || string(out) != "from the agent" {
		t.Fatalf("after the return: %q %v", out, err)
	}
	if ags := view.agents(); len(ags) != 2 || !ags[1].Online {
		t.Errorf("agent messages %+v", ags)
	}
	if ns := view.notices(); len(ns) != 2 || ns[1].Level != viewer.LevelInfo || !strings.Contains(ns[1].Text, "back") {
		t.Errorf("notices %+v", ns)
	}
	if all := lg.all(); len(all) != 2 || all[1].Event != "agent_up" {
		t.Errorf("records %+v", all)
	}
}

func TestAnAgentThatGoesBetweenTheLookAndTheCallIsCaughtByTheFallback(t *testing.T) {
	l, ag, m, _, _ := newLink(t, true)
	ag.set(true, agentclient.ErrAgentDown) // up when asked, down when called
	var res protocol.ScreenshotRes
	if _, err := l.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res); err != nil || len(m.dumps) != 1 {
		t.Errorf("%v, %d dumps", err, len(m.dumps))
	}
}

func TestWithoutAMonitorTheAgentBeingGoneIsTheEnd(t *testing.T) {
	l, ag, _, view, _ := newLink(t, false)
	ag.set(false, nil)
	if _, err := l.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{}, nil, &protocol.ScreenshotRes{}); !errors.Is(err, agentclient.ErrAgentDown) {
		t.Errorf("error %v", err)
	}
	if ns := view.notices(); len(ns) != 1 || !strings.Contains(ns[0].Text, "no other way to the guest") {
		t.Errorf("notices %+v", ns)
	}
}

func TestTheAgentsOwnWordOnItsStateIsTold(t *testing.T) {
	l, _, _, view, lg := newLink(t, true)
	l.AgentState(agentclient.StateUp, nil)
	l.AgentState(agentclient.StateConnecting, nil)
	if len(view.agents()) != 0 || len(lg.all()) != 0 {
		t.Error("an agent that was there from the start, or one that is connecting, was reported")
	}
	l.AgentState(agentclient.StateDown, errors.New("heartbeat\nmissed   three times"))
	l.AgentState(agentclient.StateDown, errors.New("again"))
	if ags := view.agents(); len(ags) != 1 || ags[0].Detail != "the guest agent is not answering (heartbeat missed three times)" {
		t.Errorf("agent messages %+v", ags)
	}
	l.AgentState(agentclient.StateUp, nil)
	if ags := view.agents(); len(ags) != 2 || !ags[1].Online {
		t.Errorf("agent messages %+v", ags)
	}
	// A long reason is cut.
	l2, _, _, view2, _ := newLink(t, true)
	l2.AgentState(agentclient.StateDown, errors.New(strings.Repeat("x", 500)))
	if d := view2.agents()[0].Detail; len(d) > 200 {
		t.Errorf("a detail of %d characters", len(d))
	}
}

func TestADeadLogWhileTheAgentGoesIsFatal(t *testing.T) {
	l, ag, _, _, lg := newLink(t, true)
	lg.failAt = 1
	var got []error
	l.Fatal = func(err error) { got = append(got, err) }
	ag.set(false, nil)
	var res protocol.ScreenshotRes
	if _, err := l.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Error(), "the audit log can no longer be written") {
		t.Errorf("fatal %v", got)
	}
	// Without anything to tell, a Link is still a link.
	bare := &Link{Agent: &linkAgent{up: false}}
	bare.Fallback, _ = newFallback(t)
	if _, err := bare.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res); err != nil {
		t.Errorf("a link with no viewer or log: %v", err)
	}
}

func TestTheObserverAndTheExecutorWorkThroughTheMonitorWhenTheAgentIsGone(t *testing.T) {
	l, ag, m, view, _ := newLink(t, true)
	ag.set(false, nil)
	obs, err := vision.NewObserver(l, vision.ObserverConfig{Setup: vision.Setup{Vision: true, Mode: "native", Profile: "full", ImageFormat: imaging.PNG, ImageMax: 100}, Guest: prompt.Size{W: 200, H: 100}})
	if err != nil {
		t.Fatal(err)
	}
	seen, err := obs.Look(context.Background())
	if err != nil {
		t.Fatalf("the observer cannot look through the monitor: %v", err)
	}
	if seen.Shot == nil || seen.Shot.Note.W != 100 || seen.Shot.Note.H != 50 || seen.UI != nil || len(seen.Notes) != 1 || !strings.Contains(seen.Notes[0], "accessibility tree") {
		t.Errorf("seen: shot %v, ui %v, notes %q", seen.Shot, seen.UI, seen.Notes)
	}
	// The model clicks at (50, 25) of a picture half the screen's size, which is (100, 50) of the screen.
	x := NewExecutor(l)
	ga, err := seen.Mapper.Guest(act(&actions.MouseArgs{Op: "click", X: 50, Y: 25}))
	if err != nil {
		t.Fatal(err)
	}
	d := x.Do(context.Background(), ga)
	if !d.Result.OK || d.ScreenHash == "" {
		t.Fatalf("the click: %+v", d)
	}
	want := fmt.Sprintf("abs x %d|abs y %d|btn left down|btn left up", (100*32767+99/2)/199, (50*32767+49)/99)
	if got := strings.Join(m.events(), "|"); got != want {
		t.Errorf("events\n got %s\nwant %s", got, want)
	}
	// A command is not possible without the agent, and the model is told so in the words of every failure.
	d = x.Do(context.Background(), act(&actions.ExecArgs{Command: "ls", TimeoutS: 5}))
	if d.Result.OK || d.Result.Code != "agent_unavailable" {
		t.Errorf("exec: %+v", d.Result)
	}
	if ags := view.agents(); len(ags) != 1 || ags[0].Online {
		t.Errorf("agent messages %+v", ags)
	}
}

func TestThePictureTheMonitorMakesHasTheQualityAskedAndNoMoreThanTheLimit(t *testing.T) {
	f, m := newFallback(t)
	_, low := shot(t, f, protocol.ScreenshotReq{Format: "jpeg", Quality: 10})
	_, high := shot(t, f, protocol.ScreenshotReq{Format: "jpeg", Quality: 95})
	if len(low) >= len(high) {
		t.Errorf("a picture of quality 10 is %d bytes and one of quality 95 is %d", len(low), len(high))
	}
	m.raw = make([]byte, maxScreendump+16)
	var res protocol.ScreenshotRes
	if _, err := f.Call(context.Background(), protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("a screen of 64 megabytes: %v", err)
	}
}

func TestThePointerIsWhereTheLastMoveLeftItEvenWhenTheMoveWasOutsideTheScreen(t *testing.T) {
	f, _ := newFallback(t)
	inputOK(t, f, protocol.InputEvent{Type: "move", X: 5000, Y: -4})
	var info protocol.ScreenInfoRes
	if _, err := f.Call(context.Background(), protocol.TypeScreenInfo, struct{}{}, nil, &info); err != nil {
		t.Fatal(err)
	}
	if info.Cursor != (protocol.Point{X: 199, Y: 0}) {
		t.Errorf("cursor %+v", info.Cursor)
	}
	first, _ := shot(t, f, protocol.ScreenshotReq{Format: "png"})
	same, _ := shot(t, f, protocol.ScreenshotReq{Format: "png", IfChangedSince: first.Hash})
	if !same.Unchanged || same.Cursor != (protocol.Point{X: 199, Y: 0}) {
		t.Errorf("an unchanged screen's answer %+v", same)
	}
	// The left button can be asked for by name.
	m := f.QMP.(*fakeMonitor)
	m.mu.Lock()
	m.batches = nil
	m.mu.Unlock()
	inputOK(t, f, protocol.InputEvent{Type: "click", X: 0, Y: 0, Button: "left"})
	if got := strings.Join(m.events(), "|"); !strings.Contains(got, "btn left down") {
		t.Errorf("events %s", got)
	}
}

func TestInputOnAScreenOfKnownSizeTakesOnePictureForTheHashAndNoMore(t *testing.T) {
	f, m := newFallback(t)
	inputOK(t, f, protocol.InputEvent{Type: "key", Keys: "a"})
	if len(m.dumps) != 1 {
		t.Errorf("%d pictures for one input", len(m.dumps))
	}
}
