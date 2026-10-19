// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/agent/x11"
	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/safety/audit"
	"github.com/PacifAIst/controlsnack/internal/viewer"
	"github.com/PacifAIst/controlsnack/internal/vm/agentclient"
	"github.com/PacifAIst/controlsnack/internal/vm/qmp"
)

// When the agent in the guest does not answer, the session does not stop: QEMU can still show the screen and take the mouse and the
// keyboard (PLAN.md section 11). The Fallback answers the agent's requests for a picture, the screen's size and input from the
// monitor; everything else needs the agent and says so. A Link chooses between the two for every call.

// Monitor is the part of QEMU's monitor the fallback uses (*qmp.Client).
type Monitor interface {
	Screendump(ctx context.Context, path, format string) error
	InputSendEvent(ctx context.Context, events []qmp.InputEvent) error
}

// Limits of the fallback.
const (
	maxScreendump    = 64 << 20
	maxQMPEvents     = 256
	fallbackSettle   = 150 * time.Millisecond
	fallbackDragStep = 20 * time.Millisecond
	fallbackDragN    = 10
	maxTypedRunes    = 500
)

// Fallback is the agent's screen and input through QEMU's monitor.
type Fallback struct {
	QMP Monitor
	// Dir is where the monitor writes the screen to (the session's directory); a file is made for each picture and removed.
	Dir string
	// Screen is the size of the guest screen from the handshake; each picture corrects it.
	Screen prompt.Size
	Hasher imaging.Hasher
	// Sleep waits (time.Sleep when nil).
	Sleep func(time.Duration)

	mu     sync.Mutex
	n      int
	cx, cy int
}

// NewFallback makes a Fallback.
func NewFallback(m Monitor, dir string, screen prompt.Size) *Fallback {
	return &Fallback{QMP: m, Dir: dir, Screen: screen, Hasher: imaging.NewHasher()}
}

func (f *Fallback) sleep(d time.Duration) {
	if f.Sleep != nil {
		f.Sleep(d)
		return
	}
	time.Sleep(d)
}

func unavailable(why string) error {
	return &agentclient.RemoteError{Code: protocol.CodeUnsupported, Message: why}
}

func bad(format string, a ...any) error {
	return &agentclient.RemoteError{Code: protocol.CodeInvalidArgs, Message: fmt.Sprintf(format, a...)}
}

// Call answers a request of the agent's protocol as far as the monitor can: the screen, its size and input. The tree and the text of
// the screen are things the agent reads and are said to be unsupported (an observer goes on without them); anything else is the
// agent's alone and is as unreachable as the agent is.
func (f *Fallback) Call(ctx context.Context, typ string, req any, _ []byte, res any) ([]byte, error) {
	switch typ {
	case protocol.TypeScreenshot:
		r, ok := req.(protocol.ScreenshotReq)
		out, ok2 := res.(*protocol.ScreenshotRes)
		if !ok || !ok2 {
			return nil, errors.New("the fallback was asked for a screenshot in a form it does not know")
		}
		return f.screenshot(ctx, r, out)
	case protocol.TypeScreenInfo:
		out, ok := res.(*protocol.ScreenInfoRes)
		if !ok {
			return nil, errors.New("the fallback was asked for the screen's size in a form it does not know")
		}
		return nil, f.screenInfo(ctx, out)
	case protocol.TypeInput:
		r, ok := req.(protocol.InputReq)
		if !ok {
			return nil, errors.New("the fallback was given input in a form it does not know")
		}
		var out *protocol.InputRes
		if o, ok := res.(*protocol.InputRes); ok {
			out = o
		}
		return nil, f.input(ctx, r, out)
	case protocol.TypeA11yTree, protocol.TypeOCR:
		return nil, unavailable("the guest agent is not answering, and the structure and the text of the screen can only be read through it")
	}
	return nil, agentclient.ErrAgentDown
}

// capture takes the screen through the monitor.
func (f *Fallback) capture(ctx context.Context) (*image.RGBA, error) {
	f.mu.Lock()
	f.n++
	path := filepath.Join(f.Dir, fmt.Sprintf("fallback-%d.png", f.n))
	f.mu.Unlock()
	if err := f.QMP.Screendump(ctx, path, "png"); err != nil {
		return nil, fmt.Errorf("the monitor could not take the screen: %w", err)
	}
	defer func() { _ = os.Remove(path) }()
	file, err := os.Open(path) //nolint:gosec // inside the session's directory, named by the fallback
	if err != nil {
		return nil, fmt.Errorf("reading the screen the monitor took: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxScreendump+1))
	if err != nil || len(data) > maxScreendump {
		return nil, errors.New("the screen the monitor took is unreadable or too large")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the screen the monitor took is not a picture: %w", err)
	}
	if err := imaging.Inspect(data, imaging.PNG, cfg.Width, cfg.Height); err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the screen the monitor took is not a picture: %w", err)
	}
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	f.mu.Lock()
	f.Screen = prompt.Size{W: rgba.Bounds().Dx(), H: rgba.Bounds().Dy()}
	f.mu.Unlock()
	return rgba, nil
}

func (f *Fallback) cursor() protocol.Point {
	f.mu.Lock()
	defer f.mu.Unlock()
	return protocol.Point{X: f.cx, Y: f.cy}
}

func (f *Fallback) screenshot(ctx context.Context, r protocol.ScreenshotReq, out *protocol.ScreenshotRes) ([]byte, error) {
	format := imaging.WebP
	if r.Format != "" {
		var err error
		if format, err = imaging.ParseFormat(r.Format); err != nil {
			return nil, bad("%v", err)
		}
	}
	rgba, err := f.capture(ctx)
	if err != nil {
		return nil, err
	}
	hash := f.Hasher.Hash(rgba)
	if r.IfChangedSince != "" && r.IfChangedSince == hash {
		*out = protocol.ScreenshotRes{Hash: hash, Cursor: f.cursor(), Unchanged: true}
		return nil, nil
	}
	img, scale := imaging.Scale(rgba, r.MaxLongEdge)
	blob, err := imaging.Encode(img, format, r.Quality)
	if err != nil {
		return nil, fmt.Errorf("making the picture: %w", err)
	}
	b := img.Bounds()
	*out = protocol.ScreenshotRes{W: b.Dx(), H: b.Dy(), Format: string(format), Hash: hash, Cursor: f.cursor(), Scale: scale}
	return blob, nil
}

// size is the size of the guest screen: the handshake's, or what the first picture shows.
func (f *Fallback) size(ctx context.Context) (int, int, error) {
	f.mu.Lock()
	w, h := f.Screen.W, f.Screen.H
	f.mu.Unlock()
	if w >= 2 && h >= 2 {
		return w, h, nil
	}
	if _, err := f.capture(ctx); err != nil {
		return 0, 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Screen.W, f.Screen.H, nil
}

func (f *Fallback) screenInfo(ctx context.Context, out *protocol.ScreenInfoRes) error {
	w, h, err := f.size(ctx)
	if err != nil {
		return err
	}
	*out = protocol.ScreenInfoRes{Monitors: []protocol.Monitor{{ID: 0, Bounds: protocol.Rect{0, 0, w, h}, Primary: true}}, Cursor: f.cursor()}
	return nil
}

// send gives the monitor events, a few hundred at a time.
func (f *Fallback) send(ctx context.Context, evs []qmp.InputEvent) error {
	for len(evs) > 0 {
		n := min(len(evs), maxQMPEvents)
		if err := f.QMP.InputSendEvent(ctx, evs[:n]); err != nil {
			return fmt.Errorf("the monitor could not send the input: %w", err)
		}
		evs = evs[n:]
	}
	return nil
}

func (f *Fallback) moveTo(x, y, w, h int) ([]qmp.InputEvent, error) {
	evs, err := qmp.MoveTo(x, y, w, h)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.cx, f.cy = max(0, min(x, w-1)), max(0, min(y, h-1))
	f.mu.Unlock()
	return evs, nil
}

func button(name string, down bool) (qmp.InputEvent, error) { return qmp.Button(name, down) }

func buttonName(b string) (string, error) {
	switch strings.ToLower(b) {
	case "", "left":
		return "left", nil
	case "right":
		return "right", nil
	case "middle":
		return "middle", nil
	}
	return "", bad("the mouse button %q is not left, right or middle", b)
}

// input carries out the events of a request through the monitor, and gives the screen's hash after them.
func (f *Fallback) input(ctx context.Context, r protocol.InputReq, out *protocol.InputRes) error {
	w, h, err := f.size(ctx)
	if err != nil {
		return err
	}
	for i, ev := range r.Events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.event(ctx, ev, w, h); err != nil {
			var re *agentclient.RemoteError
			if errors.As(err, &re) {
				return &agentclient.RemoteError{Code: re.Code, Message: fmt.Sprintf("event %d (%s): %s", i+1, ev.Type, re.Message)}
			}
			return fmt.Errorf("event %d (%s) failed after %d earlier ones ran: %w", i+1, ev.Type, i, err)
		}
	}
	f.sleep(fallbackSettle)
	if out != nil {
		if rgba, err := f.capture(ctx); err == nil {
			out.ScreenHashAfter = f.Hasher.Hash(rgba)
		}
	}
	return nil
}

func (f *Fallback) event(ctx context.Context, ev protocol.InputEvent, w, h int) error {
	switch ev.Type {
	case "move":
		evs, err := f.moveTo(ev.X, ev.Y, w, h)
		if err != nil {
			return err
		}
		return f.send(ctx, evs)
	case "down", "up", "click":
		name, err := buttonName(ev.Button)
		if err != nil {
			return err
		}
		evs, err := f.moveTo(ev.X, ev.Y, w, h)
		if err != nil {
			return err
		}
		var presses []bool
		switch ev.Type {
		case "down":
			presses = []bool{true}
		case "up":
			presses = []bool{false}
		default:
			presses = []bool{true, false}
			if ev.Double {
				presses = []bool{true, false, true, false}
			}
		}
		for _, down := range presses {
			b, err := button(name, down)
			if err != nil {
				return err
			}
			evs = append(evs, b)
		}
		return f.send(ctx, evs)
	case "scroll":
		return f.scroll(ctx, ev, w, h)
	case "drag":
		return f.drag(ctx, ev, w, h)
	case "key":
		return f.keys(ctx, ev)
	case "text":
		return f.text(ctx, ev.Text)
	}
	return bad("the input %q is not one the fallback knows", ev.Type)
}

func (f *Fallback) scroll(ctx context.Context, ev protocol.InputEvent, w, h int) error {
	evs, err := f.moveTo(ev.X, ev.Y, w, h)
	if err != nil {
		return err
	}
	wheel := func(n int, neg, pos string) error {
		name := pos
		if n < 0 {
			name, n = neg, -n
		}
		for i := 0; i < n; i++ {
			for _, down := range []bool{true, false} {
				b, err := button(name, down)
				if err != nil {
					return err
				}
				evs = append(evs, b)
			}
		}
		return nil
	}
	if err := wheel(ev.DY, "wheel-up", "wheel-down"); err != nil {
		return err
	}
	if err := wheel(ev.DX, "wheel-left", "wheel-right"); err != nil {
		return err
	}
	return f.send(ctx, evs)
}

func (f *Fallback) drag(ctx context.Context, ev protocol.InputEvent, w, h int) error {
	if ev.DragTo == nil {
		return bad("a drag needs a place to drag to")
	}
	start, err := f.moveTo(ev.X, ev.Y, w, h)
	if err != nil {
		return err
	}
	down, err := button("left", true)
	if err != nil {
		return err
	}
	if err := f.send(ctx, append(start, down)); err != nil {
		return err
	}
	for i := 1; i <= fallbackDragN; i++ {
		f.sleep(fallbackDragStep)
		x := ev.X + (ev.DragTo.X-ev.X)*i/fallbackDragN
		y := ev.Y + (ev.DragTo.Y-ev.Y)*i/fallbackDragN
		step, err := f.moveTo(x, y, w, h)
		if err != nil {
			return err
		}
		if err := f.send(ctx, step); err != nil {
			return err
		}
	}
	up, err := button("left", false)
	if err != nil {
		return err
	}
	return f.send(ctx, []qmp.InputEvent{up})
}

func key(code string, down bool) (qmp.InputEvent, error) { return qmp.Key(code, down) }

// keys presses a combination: the modifiers down, the key, and everything up again in the opposite order.
func (f *Fallback) keys(ctx context.Context, ev protocol.InputEvent) error {
	combo, err := x11.ParseCombo(ev.Keys)
	if err != nil {
		return bad("%v", err)
	}
	var mods []string
	for _, m := range combo.Modifiers {
		code, _, ok := qcodeOf(m)
		if !ok {
			return bad("the modifier of %q has no key on the fallback's keyboard", ev.Keys)
		}
		mods = append(mods, code)
	}
	keyCode, shift := "", false
	if combo.Key != 0 {
		var ok bool
		if keyCode, shift, ok = qcodeOf(combo.Key); !ok {
			return bad("%q has a key the fallback's keyboard does not have", ev.Keys)
		}
	}
	var evs []qmp.InputEvent
	add := func(code string, down bool) error {
		e, err := key(code, down)
		if err != nil {
			return err
		}
		evs = append(evs, e)
		return nil
	}
	for n := 0; n < max(1, ev.Repeat); n++ {
		held := append([]string(nil), mods...)
		if shift && !contains(held, "shift") {
			held = append(held, "shift")
		}
		for _, m := range held {
			if err := add(m, true); err != nil {
				return err
			}
		}
		if keyCode != "" {
			if err := add(keyCode, true); err != nil {
				return err
			}
			if err := add(keyCode, false); err != nil {
				return err
			}
		}
		for i := len(held) - 1; i >= 0; i-- {
			if err := add(held[i], false); err != nil {
				return err
			}
		}
	}
	return f.send(ctx, evs)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// text types characters one by one on a US keyboard.
func (f *Fallback) text(ctx context.Context, s string) error {
	if utf8.RuneCountInString(s) > maxTypedRunes {
		return bad("the fallback types at most %d characters at a time", maxTypedRunes)
	}
	var evs []qmp.InputEvent
	add := func(code string, down bool) error {
		e, err := key(code, down)
		if err != nil {
			return err
		}
		evs = append(evs, e)
		return nil
	}
	for _, r := range s {
		var code string
		var shift, ok bool
		switch r {
		case '\n':
			code, ok = "ret", true
		case '\t':
			code, ok = "tab", true
		default:
			code, shift, ok = qcodeOf(uint32(r)) //nolint:gosec // a rune of a valid string is not negative
		}
		if !ok {
			return bad("the character %q has no key on the fallback's keyboard: it types plain keyboard characters only", r)
		}
		if shift {
			if err := add("shift", true); err != nil {
				return err
			}
		}
		if err := add(code, true); err != nil {
			return err
		}
		if err := add(code, false); err != nil {
			return err
		}
		if shift {
			if err := add("shift", false); err != nil {
				return err
			}
		}
	}
	return f.send(ctx, evs)
}

// AgentLink is the agent as the session has it (*agentclient.Client).
type AgentLink interface {
	Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error)
	Up() bool
}

// Link is the session's way to the guest: the agent while it answers, the monitor when it does not. It is what the observer, the
// executor and the viewer's frame pump are given as the agent. It tells the person, once for each change, that the agent went away
// and that it came back, and writes both down.
type Link struct {
	Agent    AgentLink
	Fallback *Fallback
	Viewer   Sink
	Audit    Recorder
	// Fatal is called when the audit log can no longer be written.
	Fatal func(error)

	mu     sync.Mutex
	known  bool
	online bool
}

// report tells of a change of the agent's state, and only of a change.
func (l *Link) report(online bool, why string) {
	l.mu.Lock()
	if l.known && l.online == online {
		l.mu.Unlock()
		return
	}
	first := !l.known
	l.known, l.online = true, online
	l.mu.Unlock()
	if online && first {
		return // the agent was there from the start: nothing to say
	}
	detail := "the guest agent is answering"
	event := "agent_up"
	if !online {
		detail, event = "the guest agent is not answering", "agent_down"
		if why != "" {
			detail += " (" + why + ")"
		}
	}
	if l.Viewer != nil {
		l.Viewer.Send(viewer.Agent{Online: online, Detail: detail})
		if online {
			l.Viewer.Send(viewer.Notice{Level: viewer.LevelInfo, Text: "The guest agent is back: commands, the accessibility tree and the rest work again."})
		} else if l.Fallback != nil {
			l.Viewer.Send(viewer.Notice{Level: viewer.LevelWarn, Text: "The guest agent is not answering. Until it is back the session sees the screen and uses the mouse and the keyboard only: no commands, no accessibility tree, no recognized text."})
		} else {
			l.Viewer.Send(viewer.Notice{Level: viewer.LevelWarn, Text: "The guest agent is not answering, and there is no other way to the guest."})
		}
	}
	if l.Audit != nil {
		if _, err := l.Audit.Append(audit.Record{Kind: audit.KindLifecycle, Event: event, Actor: "system", Reason: detail}); err != nil && l.Fatal != nil {
			l.Fatal(fmt.Errorf("the audit log can no longer be written, so the session stops: %w", err))
		}
	}
}

// AgentState is told by the agent's client whenever its connection changes (agentclient.Options.OnState).
func (l *Link) AgentState(s agentclient.State, err error) {
	switch s {
	case agentclient.StateUp:
		l.report(true, "")
	case agentclient.StateDown:
		why := ""
		if err != nil {
			why = err.Error()
		}
		l.report(false, textclean.Line(why, 120))
	}
}

// Call goes to the agent when it is answering and to the fallback when it is not.
func (l *Link) Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error) {
	if l.Agent.Up() {
		out, err := l.Agent.Call(ctx, typ, req, blob, res)
		if !errors.Is(err, agentclient.ErrAgentDown) {
			l.report(true, "")
			return out, err
		}
	}
	l.report(false, "")
	if l.Fallback == nil {
		return nil, agentclient.ErrAgentDown
	}
	return l.Fallback.Call(ctx, typ, req, blob, res)
}
