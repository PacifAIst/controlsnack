// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
	"github.com/PacifAIst/controlsnack-local/web"
)

// findBrowser looks for a Chromium-based browser on this machine, or says there is none.
func findBrowser() string {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
			if base == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"))
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if p, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, p)
			}
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// page is a headless browser looking at the viewer.
type page struct {
	t    *testing.T
	ctx  context.Context
	mu   sync.Mutex
	logs []string // exceptions, console errors and warnings, and CSP violations
	skip []string // texts of reports that a test expects and ignores
}

func newPage(t *testing.T) *page {
	t.Helper()
	if os.Getenv("CS_NO_BROWSER_TESTS") != "" {
		t.Skip("CS_NO_BROWSER_TESTS is set")
	}
	exe := findBrowser()
	if exe == "" {
		t.Skip("no Chromium-based browser on this machine")
	}
	opts := append([]chromedp.ExecAllocatorOption(nil), chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Flag("disable-gpu", true), chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true), chromedp.Flag("disable-extensions", true), chromedp.Flag("disable-sync", true),
		chromedp.WindowSize(1440, 900))
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
	p := &page{t: t, ctx: ctx}
	t.Cleanup(func() { cancelTimeout(); cancelCtx(); cancelAlloc() })
	messages := chromedp.Console(ctx) // subscribes now, so nothing the page says is missed
	p.do(chromedp.EmulateViewport(1440, 900))
	go func() {
		for m, err := range messages {
			if err != nil {
				return
			}
			switch m.Type {
			case chromedp.ConsoleError, chromedp.ConsoleException, chromedp.ConsoleWarning:
				if strings.Contains(m.Text, "willReadFrequently") { // the browser's note on this test's own pixel reads, not on the page
					continue
				}
				p.log(fmt.Sprintf("%s: %s (%s %s)", m.Type, m.Text, m.Source, m.URL))
			}
		}
	}()
	return p
}

// ignoring tells the page to leave out the reports that hold a text.
func (p *page) ignoring(texts ...string) {
	p.mu.Lock()
	p.skip = append(p.skip, texts...)
	p.mu.Unlock()
}

func (p *page) log(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.skip {
		if strings.Contains(s, k) {
			return
		}
	}
	p.logs = append(p.logs, s)
}

func (p *page) logged() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.logs...)
}

// clean fails the test if the page threw, logged an error, or broke the content security policy. The browser tells the
// console a moment after the fact, so it waits a little before it looks.
func (p *page) clean() {
	p.t.Helper()
	time.Sleep(300 * time.Millisecond)
	for _, l := range p.logged() {
		p.t.Errorf("the page reported: %s", l)
	}
}

func (p *page) do(steps ...chromedp.Action[chromedp.Void]) {
	p.t.Helper()
	if err := chromedp.Do(p.ctx, steps...); err != nil {
		p.t.Fatalf("browser: %v", err)
	}
}

func (p *page) str(expr string) string {
	p.t.Helper()
	s, err := chromedp.Run(p.ctx, chromedp.Evaluate[string](expr))
	if err != nil {
		p.t.Fatalf("browser: %v\n(%s)", err, expr)
	}
	return s
}

func (p *page) strs(expr string) []string {
	p.t.Helper()
	s, err := chromedp.Run(p.ctx, chromedp.Evaluate[[]string](expr))
	if err != nil {
		p.t.Fatalf("browser: %v\n(%s)", err, expr)
	}
	return s
}

// wait polls an expression until it is true.
func (p *page) wait(what, expr string) {
	p.t.Helper()
	if _, err := chromedp.Run(p.ctx, chromedp.Poll[bool](expr, chromedp.WithPollingTimeout(pollLimit()), chromedp.WithPollingInterval(25*time.Millisecond))); err != nil {
		p.t.Fatalf("waiting for %s: %v\n(%s)", what, err, expr)
	}
}

func (p *page) shot(name string) {
	dir := os.Getenv("CS_SCREENSHOT_DIR")
	if dir == "" {
		return
	}
	buf, err := chromedp.Run(p.ctx, chromedp.CaptureScreenshot())
	if err != nil {
		p.t.Fatalf("screenshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".png"), buf, 0o600); err != nil {
		p.t.Error(err)
	}
}

// open logs in with a fresh link, as the launcher does.
func (p *page) open(r *rig) {
	p.t.Helper()
	link, err := r.s.AuthURL()
	if err != nil {
		p.t.Fatal(err)
	}
	p.do(chromedp.Navigate(link))
}

// startPage is a running session with the real front-end, and a fake session behind it.
func startPage(t *testing.T) (*hubRig, *page) {
	t.Helper()
	p := newPage(t)
	h := startHubWith(t, func(o *viewer.Options) { o.Static = web.Static() })
	return h, p
}

// solid is a picture of one color.
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

func (h *hubRig) frame(t *testing.T, seq uint32, img *image.RGBA) {
	t.Helper()
	if err := h.hub.SendFrame(seq, img.Rect.Dx(), img.Rect.Dy(), webp(t, img)); err != nil {
		t.Fatal(err)
	}
}

func ptr(n int) *int { return &n }

const centerPixel = `(() => { const c = document.getElementById('screen'); return Array.from(c.getContext('2d').getImageData(c.width >> 1, c.height >> 1, 1, 1).data); })()`

// canvasSum is a number that changes when anything on the canvas changes.
const canvasSum = `(() => { const c = document.getElementById('screen'); return String(c.getContext('2d').getImageData(0, 0, c.width, c.height).data.reduce((a, v, i) => (a + v * (i % 251)) % 1000003, 0)); })()`

func TestThePageInABrowser(t *testing.T) {
	h, p := startPage(t)
	hub := h.hub

	// What a page that connects late is shown: the state, the last cursor, the last picture.
	hub.Send(viewer.State{State: viewer.StateRunning, Reason: "task started"})
	hub.Send(viewer.Cursor{X: 300, Y: 200})
	h.frame(t, 1, solid(1280, 800, color.RGBA{R: 230, G: 20, B: 230, A: 255}))
	p.open(h.rig)

	// The header.
	p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
	if got := p.str(`document.getElementById('backend').textContent`); got != "lmstudio · qwen3-vl-4b-instruct" {
		t.Errorf("backend %q", got)
	}
	if badges := p.strs(`Array.from(document.querySelectorAll('.badge')).map(b => b.textContent)`); strings.Join(badges, "|") != "TCG (slow) · fallback from AUTO|VISION|NET OFF" {
		t.Errorf("badges %q", badges)
	}
	if title := p.str(`document.querySelector('.badge-warn').title`); !strings.Contains(title, "WHPX is not available") {
		t.Errorf("the accelerator badge does not say why: %q", title)
	}
	p.wait("the state", `document.getElementById('state').textContent === 'Working'`)
	if got := p.str(`document.getElementById('counters').textContent`); got != "turns 0/100 · actions 0/60" {
		t.Errorf("counters %q", got)
	}
	if got := p.str(`document.getElementById('timer').textContent`); !strings.HasSuffix(got, "/ 1:00:00") {
		t.Errorf("timer %q", got)
	}

	// The picture reaches the canvas: the middle of the screen is the color of the frame. A newer one replaces it.
	p.wait("the first picture", `(() => { const [r, g, b] = `+centerPixel+`; return r > 180 && g < 90 && b > 180; })()`)
	h.frame(t, 2, solid(1280, 800, color.RGBA{R: 20, G: 200, B: 40, A: 255}))
	p.wait("the second picture", `(() => { const [r, g, b] = `+centerPixel+`; return g > 150 && r < 90 && b < 100; })()`)
	h.frame(t, 3, desktop(1280, 800, 2))
	p.wait("the desktop", `(() => { const [r, g, b] = `+centerPixel+`; return !(g > 150 && r < 90 && b < 100); })()`)

	// The cursor sprite and the marker of the action about to run change the canvas.
	before := p.str(canvasSum)
	hub.Send(viewer.Plan{ActionID: "a1", Tool: "mouse", X: ptr(640), Y: ptr(400)})
	hub.Send(viewer.Cursor{X: 900, Y: 500})
	p.wait("the marker and the cursor", `(`+canvasSum+`) !== '`+before+`'`)
	withMarker := p.str(canvasSum)

	// Chat: Shift+Enter writes a new line (the browser's own behavior, left alone); Enter is taken by the page and sends.
	// The key events are dispatched as the page sees them: a real key press cannot be told apart in this test, because the
	// driver sends the character as a separate event that a physical keyboard does not.
	p.do(chromedp.Focus("#chat-input"), chromedp.KeyEvent("first line"), chromedp.KeyEvent(kb.Enter, chromedp.KeyModifiers(chromedp.ModifierShift)), chromedp.KeyEvent("second line"))
	if got := p.str(`JSON.stringify(document.getElementById('chat-input').value)`); got != `"first line\nsecond line"` {
		t.Errorf("after Shift+Enter the box holds %s", got)
	}
	key := func(init string) string {
		return p.str(`(() => { const ev = new KeyboardEvent('keydown', {key: 'Enter', bubbles: true, cancelable: true, ` + init + `}); document.getElementById('chat-input').dispatchEvent(ev); return String(ev.defaultPrevented); })()`)
	}
	if got := key(`shiftKey: true`); got != "false" {
		t.Errorf("Shift+Enter was taken by the page")
	}
	if got := key(`isComposing: true`); got != "false" {
		t.Errorf("Enter during an IME composition was taken by the page")
	}
	if chats, _, _ := h.in.snapshot(); len(chats) != 0 {
		t.Errorf("a line break or a composition sent the message: %q", chats)
	}
	if got := key(``); got != "true" {
		t.Errorf("Enter was not taken by the page")
	}
	until(t, "the chat message to reach the session", func() bool { c, _, _ := h.in.snapshot(); return len(c) == 1 })
	if chats, _, _ := h.in.snapshot(); chats[0] != "first line\nsecond line" {
		t.Errorf("chat %q", chats[0])
	}
	p.wait("the box to be emptied", `document.getElementById('chat-input').value === ''`)
	hub.Send(viewer.Chat{ID: "m1", Role: "user", Text: "first line\nsecond line", TS: ts})
	hub.Send(viewer.Chat{ID: "m2", Role: "assistant", Text: "I see a <b>dialog</b> with an \"Open\" button & will click it.", TS: ts})
	p.wait("the echo and the answer", `document.querySelectorAll('.msg').length === 2`)
	if got := p.str(`document.querySelector('.msg-assistant .msg-body').textContent`); got != `I see a <b>dialog</b> with an "Open" button & will click it.` {
		t.Errorf("assistant text %q", got)
	}
	if n := p.str(`String(document.querySelectorAll('.msg-assistant .msg-body b').length)`); n != "0" {
		t.Error("markup in a message became elements")
	}

	// Actions: collapsible, updated in place, with the figures.
	args := json.RawMessage(`{"x":640,"y":400,"button":"left"}`)
	hub.Send(viewer.Trace{ActionID: "a1", Turn: 3, Tool: "mouse", Args: args, Reason: "click Open", Verdict: "allow", Rules: []string{"G-1"}, Status: "running"})
	p.wait("the action", `document.querySelectorAll('.trace-item').length === 1`)
	hub.Send(viewer.Trace{ActionID: "a1", Turn: 3, Tool: "mouse", Args: args, Reason: "click Open", Verdict: "allow", Rules: []string{"G-1"}, Status: "done", DurationMS: 42, Summary: "clicked (640, 400)"})
	hub.Send(viewer.Trace{ActionID: "a2", Turn: 3, Tool: "exec", Args: json.RawMessage(`{"command":"ls ~/work"}`), Reason: "look", Verdict: "confirm", Status: "rejected", Summary: "you rejected it"})
	p.wait("two actions", `document.querySelectorAll('.trace-item').length === 2 && document.querySelector('.trace-item .t-status').textContent === 'done'`)
	p.wait("the marker to go once its action is over", `(`+canvasSum+`) !== '`+withMarker+`'`)
	if got := p.str(`document.getElementById('counters').textContent`); got != "turns 3/100 · actions 2/60" {
		t.Errorf("counters %q", got)
	}
	if got := p.str(`document.querySelector('.trace-item .chip').textContent`); got != "allow" {
		t.Errorf("verdict chip %q", got)
	}
	if open := p.str(`String(document.querySelector('.trace-item').open)`); open != "false" {
		t.Error("an action starts open")
	}
	p.do(chromedp.Click(".trace-item summary"))
	p.wait("the action to open", `document.querySelector('.trace-item').open === true`)
	if got := p.str(`document.querySelector('.trace-item pre').textContent`); !strings.Contains(got, `"button": "left"`) {
		t.Errorf("args %q", got)
	}

	// The audit tail and the figures of the stats.
	for i := range 3 {
		hub.Send(viewer.Audit{Line: fmt.Sprintf(`{"seq":%d,"kind":"action"}`, i)})
	}
	hub.Send(viewer.Stats{KBps: 12.5, FPS: 2, FrameBytes: 31000, LLMMS: 2100, Actions: 2})
	p.wait("the audit lines", `document.querySelectorAll('.audit-line').length === 3`)
	p.wait("the stats", `document.getElementById('traffic').textContent === '12.5 KB/s · 2.0 fps · model 2.1 s'`)

	// Notices, and the agent going away and coming back.
	hub.Send(viewer.Notice{Level: viewer.LevelWarn, Text: "The VM runs without hardware acceleration."})
	p.wait("the notice", `document.querySelectorAll('.notice-warn').length === 1`)
	p.do(chromedp.Click(".notice button"))
	p.wait("the notice to go", `document.querySelectorAll('.notice').length === 0`)
	hub.Send(viewer.Agent{Online: false, Detail: "the agent stopped answering; working through QEMU"})
	p.wait("the banner", `!document.getElementById('banner').hidden && document.getElementById('banner-title').textContent.startsWith('Agent offline')`)
	hub.Send(viewer.Agent{Online: true})
	p.wait("the banner to go", `document.getElementById('banner').hidden`)

	// Controls.
	p.do(chromedp.Click("#pause"))
	until(t, "pause", func() bool { _, _, c := h.in.snapshot(); return len(c) == 1 && c[0] == "pause" })
	hub.Send(viewer.State{State: viewer.StatePaused, Reason: "you paused"})
	p.wait("Resume", `document.getElementById('pause').textContent === 'Resume' && document.getElementById('state').textContent === 'Paused'`)
	p.do(chromedp.Click("#pause"), chromedp.Click("#stop"), chromedp.Click("#step"))
	until(t, "the controls", func() bool { _, _, c := h.in.snapshot(); return strings.Join(c, " ") == "pause resume stop step_on" })

	p.shot("viewer-main")

	// A page that connects again (a reload) is shown everything again, with no repeats.
	p.do(chromedp.Reload())
	p.wait("the reload", `document.querySelectorAll('.msg').length === 2 && document.querySelectorAll('.trace-item').length === 2 && document.querySelectorAll('.audit-line').length === 3`)
	p.wait("the picture again", `(() => { const [r, g, b] = `+centerPixel+`; return r + g + b > 60; })()`)

	// The kill button: the socket, and a plain request. The session dies once.
	p.do(chromedp.Click("#kill"))
	if res := h.waitKilled(); res.Source != killswitch.SourceButton && res.Source != killswitch.SourceAPI {
		t.Errorf("the kill: %+v", res)
	}
	hub.Send(viewer.State{State: viewer.StateKilled, Reason: "you pressed the button"})
	p.wait("the killed overlay", `!document.getElementById('overlay').hidden && document.getElementById('overlay-title').textContent === 'Session killed'`)
	p.shot("viewer-killed")
	p.clean()
}

func TestTheKillButtonWorksWithNoSocket(t *testing.T) {
	p := newPage(t)
	var attempts atomic.Int64
	refuse := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "no socket today", http.StatusServiceUnavailable)
	})
	r := start(t, func(o *viewer.Options) { o.Static = web.Static(); o.WebSocket = refuse })
	p.open(r)
	p.wait("the page", `document.getElementById('kill') !== null`)
	until(t, "the page to try the socket again", func() bool { return attempts.Load() >= 3 }) // after 0.5 s, then 1 s
	// The page asks for the CSRF value as soon as it opens, so that the kill needs one request fewer when it counts.
	p.wait("the CSRF value to be asked for", `performance.getEntriesByType('resource').some(e => e.name.endsWith('/api/csrf'))`)
	if got := p.str(`document.getElementById('state').textContent`); got != "Connecting" {
		t.Errorf("state %q", got)
	}
	if disabled := p.str(`String(document.getElementById('pause').disabled)`); disabled != "true" {
		t.Error("the controls work with no connection")
	}
	p.do(chromedp.Click("#kill"))
	if res := r.waitKilled(); res.Source != killswitch.SourceAPI {
		t.Errorf("the kill: %+v", res)
	}
	p.wait("the overlay", `document.getElementById('overlay-title').textContent === 'Killing the session…'`)
	// The refused sockets are all the page may report here.
	for _, l := range p.logged() {
		if !strings.Contains(l, "WebSocket") && !strings.Contains(l, "503") {
			t.Errorf("the page reported: %s", l)
		}
	}
}

func TestAWindowThatIsReplacedSaysSo(t *testing.T) {
	h, p := startPage(t)
	p.ignoring("favicon.svg") // the icon may be asked for after this window has been logged out
	p.open(h.rig)
	p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
	h.login() // another window logs in over this one
	p.wait("the overlay", `!document.getElementById('overlay').hidden && document.getElementById('overlay-title').textContent === 'Disconnected'`)
	if got := p.str(`document.getElementById('overlay-detail').textContent`); !strings.Contains(got, "replaced") {
		t.Errorf("detail %q", got)
	}
	if disabled := p.str(`String(document.getElementById('send').disabled)`); disabled != "true" {
		t.Error("a replaced window can still send")
	}
	p.clean()
}

func TestAnEndedSessionSaysSoAndTheKillButtonStops(t *testing.T) {
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
	h.hub.Send(viewer.Ended{Reason: "done", Summary: "The task is finished: the file was found."})
	h.hub.Send(viewer.State{State: viewer.StateEnded, Reason: "done"})
	p.wait("the overlay", `document.getElementById('overlay-title').textContent === 'Session ended'`)
	if got := p.str(`document.getElementById('overlay-detail').textContent`); got != "The task is finished: the file was found." {
		t.Errorf("detail %q", got)
	}
	if disabled := p.str(`String(document.getElementById('kill').disabled)`); disabled != "true" {
		t.Error("the kill button works on a session that has ended")
	}
	p.shot("viewer-ended")
	p.clean()
}

func TestTheBrowserTestSeesWhatTheContentPolicyRefuses(t *testing.T) {
	// A negative control: the checks above only mean something if a violation would show.
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
	p.str(`(() => { document.body.insertAdjacentHTML('beforeend', '<img src="x" onerror="window.pwned = 1">'); return ''; })()`)
	p.str(`(() => { const e = document.createElement('p'); e.setAttribute('style', 'color: red'); document.body.append(e); return ''; })()`)
	deadline := time.Now().Add(5 * time.Second)
	for len(p.logged()) < 2 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	logs := strings.Join(p.logged(), "\n")
	if !strings.Contains(logs, "Content Security Policy") && !strings.Contains(logs, "Refused") {
		t.Errorf("the policy refused two things and the test saw: %q", logs)
	}
	if got := p.str(`String(window.pwned)`); got != "undefined" {
		t.Error("an inline handler ran")
	}
}

// scheme makes the browser ask for the light or the dark color scheme.
func (p *page) scheme(name string) {
	p.t.Helper()
	_, err := chromedp.Run(p.ctx, func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		_, err := chromedp.Call(ctx, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: name}}})
		return chromedp.Void{}, err
	})
	if err != nil {
		p.t.Fatal(err)
	}
}

// contrastJS lists the contrast ratio (WCAG) between the color of the text of each element and the background it sits on.
const contrastJS = `(() => {
  const parse = (s) => { const p = s.match(/rgba?\(([^)]+)\)/)[1].split(',').map(parseFloat); return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 }; };
  const lum = (c) => { const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); };
  const bg = (el) => { for (let e = el; e; e = e.parentElement) { const c = parse(getComputedStyle(e).backgroundColor); if (c.a > 0.95) return c; } return { r: 255, g: 255, b: 255, a: 1 }; };
  const items = [
    ['.brand'], ['.session'], ['.backend'], ['.badge-ok'], ['.badge-warn'], ['.badge-danger'], ['.badge-info'], ['.state'], ['.figure'], ['.kill'],
    ['#pause'], ['#send'], ['.step'], ['.panel h2'], ['.msg-head'], ['.msg-user .msg-body'], ['.msg-assistant .msg-body'], ['.msg-system .msg-body'],
    ['.trace-item .t-tool'], ['.trace-item .t-summary'], ['.trace-item .t-ms'], ['.verdict-allow'], ['.verdict-confirm'], ['.verdict-deny'], ['.status-rejected'],
    ['.trace-body dt'], ['.trace-body pre'], ['.audit-line'], ['.notice-info'], ['.notice-warn'], ['.notice-error'], ['.notice button'], ['.banner'],
    ['#chat-input'], ['#chat-input', '::placeholder'],
  ];
  return items.map(([sel, pseudo]) => {
    const el = document.querySelector(sel);
    if (!el) return sel + ': missing';
    const fg = parse(getComputedStyle(el, pseudo || null).color);
    const l1 = lum(fg), l2 = lum(bg(el));
    return sel + (pseudo || '') + ': ' + ((Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)).toFixed(2);
  });
})()`

func TestTheTextIsReadableInBothColorSchemes(t *testing.T) {
	for _, scheme := range []string{"dark", "light"} {
		t.Run(scheme, func(t *testing.T) {
			h, p := startPage(t)
			p.scheme(scheme)
			hub := h.hub

			hub.Send(viewer.State{State: viewer.StatePaused, Reason: "you paused"})
			h.frame(t, 1, desktop(1280, 800, 2))
			hub.Send(viewer.Chat{ID: "m1", Role: "user", Text: "open the file manager", TS: ts})
			hub.Send(viewer.Chat{ID: "m2", Role: "assistant", Text: "I will click the menu.", TS: ts})
			hub.Send(viewer.Chat{ID: "m3", Role: "system", Text: "Text-only mode: this model cannot see pictures.", TS: ts})
			args := json.RawMessage(`{"command":"ls"}`)
			hub.Send(viewer.Trace{ActionID: "a1", Turn: 1, Tool: "exec", Args: args, Reason: "look", Verdict: "allow", Status: "done", DurationMS: 12, Summary: "listed"})
			hub.Send(viewer.Trace{ActionID: "a2", Turn: 1, Tool: "exec", Args: args, Reason: "delete", Verdict: "confirm", Rules: []string{"X-RM"}, Status: "rejected", Summary: "you rejected it"})
			hub.Send(viewer.Trace{ActionID: "a3", Turn: 2, Tool: "exec", Args: args, Reason: "sudo", Verdict: "deny", Rules: []string{"X-SUDO"}, Status: "denied", Summary: "refused"})
			hub.Send(viewer.Audit{Line: `{"seq":1,"kind":"lifecycle"}`})
			for _, lv := range []string{viewer.LevelInfo, viewer.LevelWarn, viewer.LevelError} {
				hub.Send(viewer.Notice{Level: lv, Text: "A " + lv + " notice."})
			}
			hub.Send(viewer.Agent{Online: false, Detail: "working through QEMU"})
			p.open(h.rig)
			p.wait("the page", `document.querySelectorAll('.notice').length === 3 && document.querySelectorAll('.trace-item').length === 3 && !document.getElementById('banner').hidden`)
			p.do(chromedp.Click(".trace-item summary"))
			// An info badge and a danger badge exist only for some sessions: put one of each on the page to measure them.
			p.str(`(() => { const b = document.getElementById('badges'); for (const k of ['info', 'danger']) { const s = document.createElement('span'); s.className = 'badge badge-' + k; s.textContent = k; b.append(s); } return ''; })()`)
			lowest, lowestName, n := 100.0, "", 0
			for _, line := range p.strs(contrastJS) {
				name, ratio, ok := strings.Cut(line, ": ")
				var r float64
				if _, err := fmt.Sscan(ratio, &r); !ok || err != nil || r < 4.5 {
					t.Errorf("%s: %s (the minimum for text is 4.5)", name, ratio)
					continue
				}
				if n++; r < lowest {
					lowest, lowestName = r, name
				}
			}
			t.Logf("%s: %d kinds of text measured, the lowest contrast is %.2f (%s); the minimum for text is 4.5", scheme, n, lowest, lowestName)
			if n < 30 {
				t.Errorf("only %d kinds of text were measured", n)
			}
			p.shot("viewer-" + scheme)
			p.clean()
		})
	}
}

// recordSockets makes every WebSocket the page opens reachable from the test, so that a test can drop one as a network would.
func (p *page) recordSockets() {
	p.t.Helper()
	const src = `(() => { const Real = window.WebSocket; window.__sockets = []; window.WebSocket = function (...a) { const s = new Real(...a); window.__sockets.push(s); return s; }; window.WebSocket.prototype = Real.prototype; Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 }); })();`
	_, err := chromedp.Run(p.ctx, func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		_, err := chromedp.Call(ctx, cdppage.AddScriptToEvaluateOnNewDocument, cdppage.AddScriptToEvaluateOnNewDocumentParams{Source: src})
		return chromedp.Void{}, err
	})
	if err != nil {
		p.t.Fatal(err)
	}
}

func TestThePageReconnectsAndIsShownEverythingOnce(t *testing.T) {
	h, p := startPage(t)
	p.recordSockets()
	hub := h.hub
	hub.Send(viewer.State{State: viewer.StateRunning, Reason: "task started"})
	hub.Send(viewer.Chat{ID: "m1", Role: "user", Text: "hello", TS: ts})
	hub.Send(viewer.Trace{ActionID: "a1", Turn: 1, Tool: "mouse", Verdict: "allow", Status: "done", Summary: "clicked"})
	hub.Send(viewer.Audit{Line: "first"})
	p.open(h.rig)
	p.wait("the page", `document.querySelectorAll('.msg').length === 1 && document.querySelectorAll('.trace-item').length === 1 && document.querySelectorAll('.audit-line').length === 1`)
	<-h.connect

	// The network drops the socket.
	p.str(`(() => { window.__sockets.at(-1).close(4001); return ''; })()`)
	p.wait("the banner", `!document.getElementById('banner').hidden && document.getElementById('banner-title').textContent.startsWith('Connection lost') && document.getElementById('state').textContent === 'Reconnecting'`)
	if disabled := p.str(`String(document.getElementById('send').disabled)`); disabled != "true" {
		t.Error("the page can send with no connection")
	}
	// What happens while it is gone is there when it is back, and what was there before is not shown twice.
	hub.Send(viewer.Chat{ID: "m2", Role: "assistant", Text: "while you were away", TS: ts})
	hub.Send(viewer.Audit{Line: "second"})
	p.wait("the reconnection", `document.getElementById('banner').hidden && document.getElementById('state').textContent === 'Working'`)
	p.wait("the replay", `document.querySelectorAll('.msg').length === 2 && document.querySelectorAll('.audit-line').length === 2 && document.querySelectorAll('.trace-item').length === 1`)
	if got := p.strs(`Array.from(document.querySelectorAll('.msg-body')).map(e => e.textContent)`); strings.Join(got, "|") != "hello|while you were away" {
		t.Errorf("chat %q", got)
	}
	if n := p.str(`String(window.__sockets.length)`); n != "2" {
		t.Errorf("%s sockets were opened", n)
	}
	if disabled := p.str(`String(document.getElementById('send').disabled)`); disabled != "false" {
		t.Error("the page cannot send after it reconnected")
	}
	p.clean()
}

func TestTheKillButtonUsesTheSocketToo(t *testing.T) {
	// With the plain request made impossible, the socket alone has to do it, and says what was pressed.
	h, p := startPage(t)
	p.open(h.rig)
	p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
	p.str(`(() => { window.fetch = () => Promise.reject(new Error('blocked by the test')); return ''; })()`)
	p.do(chromedp.Click("#kill"))
	if res := h.waitKilled(); res.Source != killswitch.SourceButton {
		t.Errorf("the kill: %+v", res)
	}
}

func TestAPageWhoseMessagesAreRefusedDoesNotKeepTrying(t *testing.T) {
	h, p := startPage(t)
	p.recordSockets()
	p.ignoring("WebSocket") // the browser notes that the host closed the socket
	p.open(h.rig)
	p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
	// Messages that break the rules, as a buggy page would send them: the host ends the connection with 1008.
	p.str(`(() => { const s = window.__sockets.at(-1); for (let i = 0; i < 60; i++) s.send('{"type":"ping"}'); return ''; })()`)
	p.wait("the overlay", `!document.getElementById('overlay').hidden && document.getElementById('overlay-title').textContent === 'Disconnected'`)
	if got := p.str(`document.getElementById('overlay-detail').textContent`); !strings.Contains(got, "bug") {
		t.Errorf("detail %q", got)
	}
	time.Sleep(1500 * time.Millisecond) // longer than the first retry would wait
	if n := p.str(`String(window.__sockets.length)`); n != "1" {
		t.Errorf("the page opened %s sockets; it must not retry after the host refused it", n)
	}
}

func TestTheBadgesOfDifferentSessions(t *testing.T) {
	for name, tc := range map[string]struct {
		hello func(*viewer.Hello)
		want  string // text:class, in order
	}{
		"hardware, text-only, net, unsafe, remote": {
			func(h *viewer.Hello) {
				h.Mode, h.Net, h.Unsafe, h.RemoteLLM = "text_only", true, true, true
				h.Accel = viewer.Accel{Requested: "kvm", Used: "kvm"}
			},
			"KVM:ok|TEXT-ONLY:warn|NET ON:warn|UNSAFE:danger|REMOTE LLM:warn",
		},
		"software by choice": {
			func(h *viewer.Hello) { h.Accel = viewer.Accel{Requested: "tcg", Used: "tcg"} },
			"TCG (slow):warn|VISION:ok|NET OFF:ok",
		},
		"software after a fallback": {
			func(h *viewer.Hello) {
				h.Accel = viewer.Accel{Requested: "whpx", Used: "tcg", FallbackReason: "no hypervisor"}
			},
			"TCG (slow) · fallback from WHPX:warn|VISION:ok|NET OFF:ok",
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPage(t)
			h := startHubWith(t, func(o *viewer.Options) { o.Static = web.Static() }, func(o *viewer.HubOptions) {
				o.Hello = func() viewer.Hello { h := sampleHello; tc.hello(&h); return h }
			})
			p.open(h.rig)
			p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
			got := p.strs(`Array.from(document.querySelectorAll('.badge')).map(b => b.textContent + ':' + b.className.replace('badge badge-', ''))`)
			if strings.Join(got, "|") != tc.want {
				t.Errorf("badges %q, want %q", got, tc.want)
			}
			p.clean()
		})
	}
}

// pollLimit is how long the page tests wait for something to happen. CS_FAST_FAIL=1 makes it short, for running the tests many
// times against deliberately broken code, where a wait that is going to fail should fail soon.
func pollLimit() time.Duration {
	if os.Getenv("CS_FAST_FAIL") != "" {
		return 4 * time.Second
	}
	return 15 * time.Second
}

func TestTheStepBoxShowsWhatTheHostIsDoing(t *testing.T) {
	for _, on := range []bool{true, false} {
		p := newPage(t)
		h := startHubWith(t, func(o *viewer.Options) { o.Static = web.Static() }, func(o *viewer.HubOptions) {
			o.Hello = func() viewer.Hello { h := sampleHello; h.Step = on; return h }
		})
		p.open(h.rig)
		p.wait("the hello message", `document.getElementById('session').textContent === 's_1234abcd'`)
		want := "false"
		if on {
			want = "true"
		}
		if got := p.str(`String(document.getElementById('step').checked)`); got != want {
			t.Errorf("the host's step mode is %v and the box is %s", on, got)
		}
		p.clean()
	}
}
