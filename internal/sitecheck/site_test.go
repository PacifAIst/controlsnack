// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package sitecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	siteDir = "../../site"
	slogan  = "ControlSnack: Computer Use with any multimodal AI"
)

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(siteDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTheSiteHasItsFilesAndItsSlogan(t *testing.T) {
	for _, name := range []string{"index.html", "docs.html", "404.html", "style.css", "app.js", "favicon.svg", "_headers", "robots.txt"} {
		if _, err := os.Stat(filepath.Join(siteDir, name)); err != nil {
			t.Errorf("the site lacks %s: %v", name, err)
		}
	}
	html := read(t, "index.html")
	if !strings.Contains(html, "<title>"+slogan+"</title>") || !strings.Contains(html, "<h1>"+slogan+"</h1>") {
		t.Error("the slogan is the title and the heading of the first page")
	}
	for _, id := range []string{"how", "steps", "models", "safety", "test", "faq"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("no section %q", id)
		}
	}
}

var (
	reAttrURL  = regexp.MustCompile(`(?i)\b(?:src|href|action|poster|srcset)\s*=\s*["'](?:https?:)?//`)
	reCSSURL   = regexp.MustCompile(`(?i)(?:url\(\s*["']?(?:https?:)?//|@import)`)
	reInline   = regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>`)
	reHandler  = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	reStyleAtt = regexp.MustCompile(`(?i)\sstyle\s*=`)
	reDanger   = regexp.MustCompile(`\b(?:innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval|new Function|importScripts)\b|\bfetch\(|XMLHttpRequest|sendBeacon|WebSocket|EventSource`)
	reFragment = regexp.MustCompile(`href="#([A-Za-z0-9_-]+)"`)
)

func TestTheSiteLoadsNothingFromElsewhereAndHasNoInlineCode(t *testing.T) {
	for _, name := range []string{"index.html", "docs.html", "404.html"} {
		// The pages may link to two addresses, the source of the program and the page of its releases (where the downloads are);
		// nothing is ever loaded from there or anywhere else.
		html := read(t, name)
		for _, allowed := range []string{`<a href="https://github.com/PacifAIst/controlsnack">`, `<a href="https://github.com/PacifAIst/controlsnack/releases">`} {
			html = strings.ReplaceAll(html, allowed, "<a>")
		}
		if reAttrURL.MatchString(html) {
			t.Errorf("%s loads or links to another site by an attribute: %s", name, reAttrURL.FindString(html))
		}
		for _, m := range reInline.FindAllString(html, -1) {
			if !strings.Contains(m, "src=") {
				t.Errorf("%s has an inline script: %s", name, m)
			}
		}
		if reHandler.MatchString(html) {
			t.Errorf("%s has an event handler attribute: %s", name, reHandler.FindString(html))
		}
		if reStyleAtt.MatchString(html) {
			t.Errorf("%s has a style attribute (the policy forbids it)", name)
		}
		if strings.Contains(html, "<style") {
			t.Errorf("%s has an inline style element", name)
		}
	}
	if css := read(t, "style.css"); reCSSURL.MatchString(css) {
		t.Errorf("the style sheet fetches something: %s", reCSSURL.FindString(css))
	}
	js := read(t, "app.js")
	if m := reDanger.FindString(js); m != "" {
		t.Errorf("app.js uses %q: it builds elements from text, and talks to no server", m)
	}
	// Every in-page link goes somewhere.
	html := read(t, "index.html")
	for _, m := range reFragment.FindAllStringSubmatch(html, -1) {
		if m[1] != "top" && !strings.Contains(html, `id="`+m[1]+`"`) {
			t.Errorf("a link to #%s goes nowhere", m[1])
		}
	}
}

func TestTheSiteHeadersAreStrict(t *testing.T) {
	headers := read(t, "_headers")
	csp := cspOf(headers)
	if csp == "" {
		t.Fatal("no Content-Security-Policy")
	}
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'none'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("the policy lacks %s: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe") || strings.Contains(csp, "*") {
		t.Errorf("the policy is not strict: %s", csp)
	}
	for _, want := range []string{"X-Content-Type-Options: nosniff", "Referrer-Policy: no-referrer"} {
		if !strings.Contains(headers, want) {
			t.Errorf("_headers lacks %s", want)
		}
	}
}

func cspOf(headers string) string {
	for _, ln := range strings.Split(headers, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(ln), "Content-Security-Policy:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---- in a browser --------------------------------------------------------------------------------------------------

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
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	case "darwin":
		candidates = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "/Applications/Chromium.app/Contents/MacOS/Chromium"}
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"} {
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

// serve serves the site as Cloudflare Pages would, with the headers of _headers, and returns its address.
func serve(t *testing.T) string {
	t.Helper()
	csp := cspOf(read(t, "_headers"))
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.Dir(siteDir))
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String() + "/"
}

type browser struct {
	t    *testing.T
	ctx  context.Context
	mu   sync.Mutex
	logs []string
}

func newBrowser(t *testing.T, width, height int) *browser {
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
		chromedp.Flag("disable-extensions", true), chromedp.WindowSize(width, height))
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
	b := &browser{t: t, ctx: ctx}
	t.Cleanup(func() { cancelTimeout(); cancelCtx(); cancelAlloc() })
	messages := chromedp.Console(ctx)
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(int64(width), int64(height))); err != nil {
		t.Fatal(err)
	}
	go func() {
		for m, err := range messages {
			if err != nil {
				return
			}
			switch m.Type {
			case chromedp.ConsoleError, chromedp.ConsoleException, chromedp.ConsoleWarning:
				b.mu.Lock()
				b.logs = append(b.logs, fmt.Sprintf("%s: %s (%s)", m.Type, m.Text, m.URL))
				b.mu.Unlock()
			}
		}
	}()
	return b
}

func (b *browser) run(expr string) string {
	b.t.Helper()
	s, err := chromedp.Run(b.ctx, chromedp.Evaluate[string](expr))
	if err != nil {
		b.t.Fatalf("browser: %v\n(%s)", err, expr)
	}
	return s
}

func (b *browser) goTo(url string) {
	b.t.Helper()
	if err := chromedp.Do(b.ctx, chromedp.Navigate(url), chromedp.WaitReady("body")); err != nil {
		b.t.Fatal(err)
	}
}

func (b *browser) clean() {
	b.t.Helper()
	time.Sleep(300 * time.Millisecond)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range b.logs {
		b.t.Errorf("the page reported: %s", l)
	}
}

func (b *browser) shot(name string) {
	dir := os.Getenv("CS_SCREENSHOT_DIR")
	if dir == "" {
		return
	}
	buf, err := chromedp.Run(b.ctx, chromedp.FullScreenshot(90))
	if err != nil {
		b.t.Fatalf("screenshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".png"), buf, 0o600); err != nil {
		b.t.Error(err)
	}
}

// view saves what the window shows now, after scrolling to an element.
func (b *browser) view(name, sel string) {
	b.t.Helper()
	dir := os.Getenv("CS_SCREENSHOT_DIR")
	if dir == "" {
		return
	}
	b.run(`document.querySelector(` + quote(sel) + `).scrollIntoView({block: 'start', behavior: 'instant'}); ''`)
	time.Sleep(200 * time.Millisecond)
	buf, err := chromedp.Run(b.ctx, chromedp.CaptureScreenshot())
	if err != nil {
		b.t.Fatalf("screenshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".png"), buf, 0o600); err != nil {
		b.t.Error(err)
	}
}

// click presses an element, and set types into a field, the way a person's browser would tell the page.
func (b *browser) click(sel string) {
	b.t.Helper()
	b.run(`(() => { const e = document.querySelector(` + quote(sel) + `); if (!e) return 'no ' + ` + quote(sel) + `; e.click(); return ''; })()`)
}

func (b *browser) set(sel, value string) {
	b.t.Helper()
	b.run(`(() => { const e = document.querySelector(` + quote(sel) + `); if (!e) return 'no ' + ` + quote(sel) + `; e.value = ` + quote(value) + `; e.dispatchEvent(new Event('input', {bubbles: true})); e.dispatchEvent(new Event('change', {bubbles: true})); return ''; })()`)
}

// quote is s as a JavaScript string.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestTheSitePageWorksInABrowserWithTheStrictPolicy(t *testing.T) {
	url := serve(t)
	b := newBrowser(t, 1440, 900)
	b.goTo(url)
	b.run(`localStorage.clear(); ''`)
	b.goTo(url)

	// The tabs show one panel at a time, and the arrows move between them.
	b.click("#tab-mac")
	if got := b.run(`[document.getElementById('panel-mac').hidden, document.getElementById('panel-windows').hidden, document.getElementById('tab-mac').getAttribute('aria-selected')].join()`); got != "false,true,true" {
		t.Errorf("after choosing macOS: %s", got)
	}
	b.run(`document.getElementById('tab-mac').dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowRight', bubbles: true})); ''`)
	if got := b.run(`document.getElementById('tab-linux').getAttribute('aria-selected')`); got != "true" {
		t.Errorf("the right arrow does not move to Linux: %s", got)
	}
	// Every command block has its copy button, however many the page has.
	if n := b.run(`String(document.querySelectorAll('pre.cmd .copy').length) + '/' + document.querySelectorAll('pre.cmd').length`); strings.Split(n, "/")[0] != strings.Split(n, "/")[1] || n == "0/0" {
		t.Errorf("copy buttons on commands: %s", n)
	}

	// The timer.
	b.click("#t-start")
	time.Sleep(1300 * time.Millisecond)
	if got := b.run(`document.getElementById('clock').textContent`); got == "00:00.0" {
		t.Errorf("the clock did not move: %s", got)
	}
	b.click("#stages li:nth-child(1) button")
	got := b.run(`document.querySelector('#stages li:nth-child(1) input').value`)
	if got != "0:01" {
		t.Errorf("the first stage took %q", got)
	}
	// A second stage is the time since the first, not the time since the start.
	time.Sleep(1300 * time.Millisecond)
	b.click("#stages li:nth-child(2) button")
	if got := b.run(`document.querySelector('#stages li:nth-child(2) input').value`); got != "0:01" {
		t.Errorf("the second stage took %q after the first, not 0:01", got)
	}
	b.set("#stages li:nth-child(2) input", "1:05") // and a time can be typed over it
	if got := b.run(`document.getElementById('report').value`); !strings.Contains(got, "| Install QEMU | 0:01 |") || !strings.Contains(got, "| Import or build the image | 1:05 |") {
		t.Errorf("the report lacks the stage times:\n%s", got)
	}
	b.set("#stages li:nth-child(3) input", "nonsense")
	if got := b.run(`document.getElementById('r-msg').textContent`); !strings.Contains(got, "m:ss") {
		t.Errorf("a time that is not a time is not explained: %q", got)
	}
	b.click("#t-pause")
	if got := b.run(`[document.getElementById('t-start').textContent, document.getElementById('t-pause').disabled].join()`); got != "Resume,true" {
		t.Errorf("after pausing: %s", got)
	}

	// The form and the checks end up in the report, in Markdown and in JSON.
	b.set(`[name="os"]`, "Windows 10")
	b.set(`[name="model"]`, "qwen3-vl-4b-instruct")
	b.set(`[name="notes"]`, "a note | with a pipe\nand a second line")
	b.click(`input[name="chk-doctor"][value="pass"]`)
	b.click(`input[name="chk-task2"][value="fail"]`)
	b.set(`#checklist .check:nth-child(8) input[type="number"]`, "14")
	report := b.run(`document.getElementById('report').value`)
	for _, want := range []string{"- System: Windows 10", "- Model: qwen3-vl-4b-instruct", "| doctor gave a verdict | pass |", "| Task 2 done: type a line in the text editor | fail |",
		"| Kill stopped the VM | not answered | 14 ms |", "a note | with a pipe"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	if !strings.Contains(report, "| Check | Result | Detail |") || strings.Contains(report, "| a note |") {
		t.Errorf("the table is broken by what was typed:\n%s", report)
	}
	b.shot("site-desktop")
	b.view("site-hero", "#top")
	b.view("site-steps", "#steps")
	b.view("site-test", "#test")

	// What was typed is there after a reload, and gone after Clear.
	b.goTo(url)
	if got := b.run(`[document.querySelector('[name="model"]').value, document.querySelector('input[name="chk-doctor"]:checked').value, document.getElementById('clock').textContent !== '00:00.0'].join()`); got != "qwen3-vl-4b-instruct,pass,true" {
		t.Errorf("after a reload: %s", got)
	}
	b.run(`window.confirm = () => true; ''`)
	b.click("#r-clear")
	if got := b.run(`[document.querySelector('[name="model"]').value, String(document.querySelectorAll('input[name="chk-doctor"]:checked').length), document.getElementById('clock').textContent, String(localStorage.length)].join()`); got != ",0,00:00.0,0" {
		// the page saves again as it renders, so a stored copy of the blank state is also fine
		if !strings.HasPrefix(got, ",0,00:00.0,") {
			t.Errorf("after Clear: %s", got)
		}
	}
	b.clean()
}

func TestTheSitePageFitsAPhone(t *testing.T) {
	url := serve(t)
	b := newBrowser(t, 390, 844)
	b.goTo(url)
	b.run(`localStorage.clear(); ''`)
	b.goTo(url)
	// Nothing makes the page wider than the screen (a table or a command may scroll inside its own box).
	if got := b.run(`[document.documentElement.scrollWidth, window.innerWidth].join()`); strings.Split(got, ",")[0] != strings.Split(got, ",")[1] {
		t.Errorf("the page scrolls sideways: scroll width and window width are %s", got)
	}
	// Every button and field a person taps is at least 40 pixels tall.
	if got := b.run(`Array.from(document.querySelectorAll('button, select, input[type="text"], input[type="number"]')).filter(e => e.offsetParent && e.getBoundingClientRect().height < 30 && !e.closest('.seg')).map(e => e.id || e.name || e.className).join()`); got != "" {
		t.Errorf("small tap targets: %s", got)
	}
	b.shot("site-phone")
	b.clean()
}

func TestTheNotFoundPageHasItsStyle(t *testing.T) {
	url := serve(t)
	b := newBrowser(t, 1000, 700)
	b.goTo(url + "404.html")
	if got := b.run(`getComputedStyle(document.querySelector('main')).paddingTop`); got == "0px" {
		t.Errorf("the 404 page is not styled: %s", got)
	}
	b.clean()
}
