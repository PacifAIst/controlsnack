// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

func TestTheServerListensOnLoopbackOnly(t *testing.T) {
	r := start(t)
	host, port, err := net.SplitHostPort(r.s.Addr())
	if err != nil || host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("address %q (%v)", r.s.Addr(), err)
	}
	if r.s.Origin() != "http://"+r.s.Addr() {
		t.Errorf("origin %q", r.s.Origin())
	}
	if err := r.s.Start(context.Background()); err == nil {
		t.Error("a second Start was accepted")
	}
}

func TestNewRefusesAnIncompleteSetup(t *testing.T) {
	good := func() viewer.Options {
		return viewer.Options{Static: staticFiles, Killer: killswitch.New(killswitch.Options{}), KillToken: killToken, WebSocket: newSocket()}
	}
	for name, mod := range map[string]func(*viewer.Options){
		"no front-end": func(o *viewer.Options) { o.Static = nil },
		"no killer":    func(o *viewer.Options) { o.Killer = nil },
		"no token":     func(o *viewer.Options) { o.KillToken = "" },
		"short token":  func(o *viewer.Options) { o.KillToken = "abc" },
		"no socket":    func(o *viewer.Options) { o.WebSocket = nil },
		"no randomness": func(o *viewer.Options) {
			o.Random = func(int) (string, error) { return "", errors.New("no entropy") }
		},
	} {
		o := good()
		mod(&o)
		if _, err := viewer.New(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := viewer.New(good()); err != nil {
		t.Errorf("a complete setup: %v", err)
	}
	s, _ := viewer.New(good())
	if _, err := s.AuthURL(); err == nil {
		t.Error("a login link before Start")
	}
	if s.Addr() != "" || s.Origin() != "" || s.Close() != nil {
		t.Error("a server that is not started has an address, or Close failed")
	}
}

func TestTheHostHeaderMustBeTheViewersAddress(t *testing.T) {
	r := start(t)
	c := r.login()
	_, port, _ := net.SplitHostPort(r.s.Addr())
	for _, host := range []string{"localhost:" + port, "127.0.0.1", "127.0.0.1:1", "evil.example:" + port, "127.0.0.1:" + port + ".", "[::1]:" + port, "0.0.0.0:" + port, "127.0.0.2:" + port} {
		for _, p := range []string{"/", "/app.js", "/auth?t=x", "/ws", "/api/kill", "/control/kill", "/nothing"} {
			resp, _ := r.do(req{path: p, host: host, cookie: c, method: http.MethodPost})
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("Host %q on %s: %s", host, p, resp.Status)
			}
		}
	}
	if r.k.Killed() {
		t.Error("a request with a wrong Host fired the kill switch")
	}
	if r.count(viewer.EventRefused) == 0 {
		t.Error("the refusals were not reported")
	}
}

func TestEveryAnswerCarriesTheSecurityHeaders(t *testing.T) {
	r := start(t)
	c := r.login()
	_, port, _ := net.SplitHostPort(r.s.Addr())
	wantCSP := "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; connect-src 'self' ws://127.0.0.1:" + port +
		"; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	for _, q := range []req{
		{path: "/", cookie: c}, {path: "/"}, {path: "/app.js", cookie: c}, {path: "/nothing", cookie: c}, {path: "/auth"}, {path: "/auth?t=zzz"},
		{path: "/ws"}, {path: "/api/kill"}, {method: http.MethodPost, path: "/api/kill"}, {method: http.MethodPost, path: "/"},
		{method: http.MethodPost, path: "/control/kill"}, {path: "/", host: "evil.example"}, {method: http.MethodDelete, path: "/auth"},
	} {
		resp, _ := r.do(q)
		for h, want := range map[string]string{
			"Content-Security-Policy":      wantCSP,
			"X-Content-Type-Options":       "nosniff",
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Cache-Control":                "no-store",
		} {
			if got := resp.Header.Get(h); got != want {
				t.Errorf("%s %s (%s): %s = %q, want %q", q.method, q.path, resp.Status, h, got, want)
			}
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: the answer opens itself to other origins", q.path)
		}
	}
}

func TestALoginLinkWorksOnceAndSetsAStrictCookie(t *testing.T) {
	r := start(t)
	link, err := r.s.AuthURL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, r.s.Origin()+"/auth?t=") || len(link) < len(r.s.Origin())+len("/auth?t=")+64 {
		t.Fatalf("link %q", link)
	}
	resp, _ := r.do(req{path: strings.TrimPrefix(link, r.s.Origin())})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("login: %s -> %q", resp.Status, resp.Header.Get("Location"))
	}
	sc := resp.Header.Values("Set-Cookie")
	if len(sc) != 1 {
		t.Fatalf("Set-Cookie: %q", sc)
	}
	for _, want := range []string{viewer.CookieName + "=", "Path=/", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(sc[0], want) {
			t.Errorf("the cookie %q lacks %q", sc[0], want)
		}
	}
	if strings.Contains(sc[0], "Max-Age") || strings.Contains(sc[0], "Expires") {
		t.Errorf("the cookie outlives the window: %q", sc[0])
	}
	if got := r.count(viewer.EventAuth); got != 1 {
		t.Errorf("auth events: %v", r.kinds())
	}
}

func TestOnlyTheWindowThatLoggedInSeesThePage(t *testing.T) {
	r := start(t)
	if resp, _ := r.do(req{path: "/"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no cookie: %s", resp.Status)
	}
	c := r.login()
	resp, body := r.do(req{path: "/", cookie: c})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "<title>t</title>") || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("with the cookie: %s %q %q", resp.Status, resp.Header.Get("Content-Type"), body)
	}
	for name, bad := range map[string]*http.Cookie{
		"empty":     {Name: viewer.CookieName, Value: ""},
		"wrong":     {Name: viewer.CookieName, Value: strings.Repeat("a", len(c.Value))},
		"truncated": {Name: viewer.CookieName, Value: c.Value[:len(c.Value)-1]},
		"extended":  {Name: viewer.CookieName, Value: c.Value + "0"},
		"other":     {Name: "other", Value: c.Value},
	} {
		if resp, _ := r.do(req{path: "/", cookie: bad}); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s cookie: %s", name, resp.Status)
		}
	}
}

func TestLoginLinksThatAreNotGood(t *testing.T) {
	r := start(t)
	link, _ := r.s.AuthURL()
	good := strings.TrimPrefix(link, r.s.Origin())
	token := strings.TrimPrefix(good, "/auth?t=")
	for name, q := range map[string]string{
		"none":      "/auth",
		"empty":     "/auth?t=",
		"unknown":   "/auth?t=" + strings.Repeat("0", 64),
		"two":       "/auth?t=" + token + "&t=" + token,
		"too long":  "/auth?t=" + strings.Repeat("a", 300),
		"other key": "/auth?token=" + token,
		"cut":       "/auth?t=" + token[:63],
	} {
		if resp, _ := r.do(req{path: q}); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %s", name, resp.Status)
		}
	}
	// A method that is not GET does not use the link up.
	for _, m := range []string{http.MethodPost, http.MethodHead, http.MethodPut} {
		if resp, _ := r.do(req{method: m, path: good}); resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET" {
			t.Errorf("%s: %s allow %q", m, "", resp.Header.Get("Allow"))
		}
	}
	if r.k.Killed() {
		t.Fatal("links that are not good fired the kill switch")
	}
	if resp, _ := r.do(req{path: good}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("the good link, after all that: %s", resp.Status)
	}
}

func TestALoginLinkExpires(t *testing.T) {
	r := start(t)
	link, _ := r.s.AuthURL()
	good := strings.TrimPrefix(link, r.s.Origin())
	r.clock.Advance(viewer.DefaultBootstrapTTL + time.Second)
	if resp, _ := r.do(req{path: good}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an expired link: %s", resp.Status)
	}
	// An expired link that was never used is not a sign of anybody racing the viewer.
	if resp, _ := r.do(req{path: good}); resp.StatusCode != http.StatusUnauthorized || r.k.Killed() {
		t.Errorf("the expired link again: %s, killed %v", resp.Status, r.k.Killed())
	}
	if got := r.count(viewer.EventAuthDenied); got != 1 { // the second is inside the second the events are limited to
		t.Errorf("denied events: %v", r.kinds())
	}
	// One just inside its time still works.
	link, _ = r.s.AuthURL()
	r.clock.Advance(viewer.DefaultBootstrapTTL - time.Second)
	if resp, _ := r.do(req{path: strings.TrimPrefix(link, r.s.Origin())}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a link inside its time: %s", resp.Status)
	}
}

func TestTheTimeOfALoginLinkIsConfigurable(t *testing.T) {
	r := start(t, func(o *viewer.Options) { o.BootstrapTTL = 5 * time.Second })
	link, _ := r.s.AuthURL()
	r.clock.Advance(6 * time.Second)
	if resp, _ := r.do(req{path: strings.TrimPrefix(link, r.s.Origin())}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a link past a short time: %s", resp.Status)
	}
}

func TestUsingALoginLinkTwiceEndsTheSession(t *testing.T) {
	r := start(t)
	link, _ := r.s.AuthURL()
	good := strings.TrimPrefix(link, r.s.Origin())
	if resp, _ := r.do(req{path: good}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("first use: %s", resp.Status)
	}
	if r.k.Killed() {
		t.Fatal("the first use fired the kill switch")
	}
	resp, _ := r.do(req{path: good})
	if resp.StatusCode != http.StatusForbidden || len(resp.Header.Values("Set-Cookie")) != 0 {
		t.Errorf("second use: %s, cookies %q", resp.Status, resp.Header.Values("Set-Cookie"))
	}
	res := r.waitKilled()
	if res.Source != killswitch.SourceInternal || !strings.Contains(res.Reason, "twice") {
		t.Errorf("the kill: %+v", res)
	}
	if r.count(viewer.EventTripwire) != 1 {
		t.Errorf("events: %v", r.kinds())
	}
}

func TestTooManyLoginLinksAtOnce(t *testing.T) {
	r := start(t)
	var err error
	n := 0
	for ; err == nil && n < 100; n++ {
		_, err = r.s.AuthURL()
	}
	if err == nil || n < 8 || n > 40 {
		t.Fatalf("%d links were issued, then %v", n, err)
	}
	r.clock.Advance(3 * viewer.DefaultBootstrapTTL) // old links are forgotten and there is room again
	if _, err := r.s.AuthURL(); err != nil {
		t.Errorf("after the old ones expired: %v", err)
	}
}

func TestEventsNeverHoldASecret(t *testing.T) {
	r := start(t)
	link, _ := r.s.AuthURL()
	good := strings.TrimPrefix(link, r.s.Origin())
	token := strings.TrimPrefix(good, "/auth?t=")
	c := r.login()
	r.do(req{path: good})
	r.do(req{path: "/auth?t=" + strings.Repeat("e", 64)})
	r.do(req{path: "/", cookie: &http.Cookie{Name: viewer.CookieName, Value: "guess"}})
	r.do(req{method: http.MethodPost, path: "/api/kill", cookie: c, header: map[string]string{viewer.HeaderCSRF: "guess"}})
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		for _, secret := range []string{token, c.Value, r.s.CSRF(), killToken, "guess", strings.Repeat("e", 64)} {
			if strings.Contains(e.Detail, secret) {
				t.Errorf("event %+v holds a secret", e)
			}
		}
	}
}

func TestStaticFilesAreServedFromTheFrontEndAndNothingElse(t *testing.T) {
	r := start(t)
	c := r.login()
	ok := map[string]string{
		"/": "text/html; charset=utf-8", "/index.html": "text/html; charset=utf-8", "/app.js": "text/javascript; charset=utf-8",
		"/app.css": "text/css; charset=utf-8", "/logo.svg": "image/svg+xml", "/data.json": "application/json",
		"/font/ui.woff2": "font/woff2", "/icons/favicon.ico": "image/x-icon", "/sub/inner.js": "text/javascript; charset=utf-8",
	}
	for p, ct := range ok {
		resp, body := r.do(req{path: p, cookie: c})
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != ct || body == "" {
			t.Errorf("%s: %s %q %q", p, resp.Status, resp.Header.Get("Content-Type"), body)
		}
	}
	for _, p := range []string{
		"/notes.txt", "/app.js.map", "/sub/", "/sub", "/font", "/nothing.js", "/../index.html", "/%2e%2e/index.html", "/..%2findex.html",
		"//app.js", "/./app.js", "/sub/../app.js", "/app.js/", "/app.js%00.css", "/%2Fapp.js", "/sub%2Finner.js/..", "/INDEX.HTML", "/App.js",
		"/.git/config", "/internal/viewer/server.go",
	} {
		resp, body := r.do(req{path: p, cookie: c})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %s %q", p, resp.Status, body)
		}
	}
	// HEAD tells the size and sends nothing; other methods are not allowed.
	resp, body := r.do(req{method: http.MethodHead, path: "/app.js", cookie: c})
	if resp.StatusCode != http.StatusOK || body != "" || resp.Header.Get("Content-Length") != "13" {
		t.Errorf("HEAD: %s %q length %q", resp.Status, body, resp.Header.Get("Content-Length"))
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		resp, _ := r.do(req{method: m, path: "/app.js", cookie: c})
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: %s allow %q", m, resp.Status, resp.Header.Get("Allow"))
		}
	}
}

func TestTheKillButtonNeedsTheCookieTheCSRFValueAndTheOrigin(t *testing.T) {
	r := start(t)
	c := r.login()
	post := func(cookie *http.Cookie, h map[string]string) int {
		resp, _ := r.do(req{method: http.MethodPost, path: "/api/kill", cookie: cookie, header: h})
		return resp.StatusCode
	}
	page := r.page()
	without := func(k string) map[string]string {
		h := r.page()
		delete(h, k)
		return h
	}
	with := func(k, v string) map[string]string {
		h := r.page()
		h[k] = v
		return h
	}
	for name, tc := range map[string]struct {
		cookie *http.Cookie
		h      map[string]string
		want   int
	}{
		"no cookie":              {nil, page, http.StatusUnauthorized},
		"wrong cookie":           {&http.Cookie{Name: viewer.CookieName, Value: "x"}, page, http.StatusUnauthorized},
		"no CSRF":                {c, without(viewer.HeaderCSRF), http.StatusForbidden},
		"wrong CSRF":             {c, with(viewer.HeaderCSRF, "x"), http.StatusForbidden},
		"the kill token as CSRF": {c, with(viewer.HeaderCSRF, killToken), http.StatusForbidden},
		"another origin":         {c, with("Origin", "http://evil.example"), http.StatusForbidden},
		"origin null":            {c, with("Origin", "null"), http.StatusForbidden},
		"another port":           {c, with("Origin", "http://127.0.0.1:1"), http.StatusForbidden},
		"cross-site fetch":       {c, with("Sec-Fetch-Site", "cross-site"), http.StatusForbidden},
		"same-site fetch":        {c, with("Sec-Fetch-Site", "same-site"), http.StatusForbidden},
		"navigation":             {c, with("Sec-Fetch-Site", "none"), http.StatusForbidden},
	} {
		if got := post(tc.cookie, tc.h); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
	if resp, _ := r.do(req{path: "/api/kill", cookie: c, header: page}); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %s", resp.Status)
	}
	if r.k.Killed() {
		t.Fatal("a refused request fired the kill switch")
	}

	// A request from a client that sends no Origin and no fetch metadata (not a browser page) still needs the cookie and
	// the CSRF value, and works.
	resp, body := r.do(req{method: http.MethodPost, path: "/api/kill", cookie: c, header: map[string]string{viewer.HeaderCSRF: r.s.CSRF()}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"killed":true`) {
		t.Fatalf("the kill: %s %q", resp.Status, body)
	}
	if res := r.waitKilled(); res.Source != killswitch.SourceAPI {
		t.Errorf("the kill: %+v", res)
	}
}

func TestTheKillEndpointOfTheCommandLine(t *testing.T) {
	r := start(t)
	for name, h := range map[string]map[string]string{
		"no token": nil, "wrong token": {"Authorization": "Bearer " + strings.Repeat("0", 32)}, "basic": {"Authorization": "Basic " + killToken},
		"token alone": {"Authorization": killToken},
	} {
		if resp, _ := r.do(req{method: http.MethodPost, path: "/control/kill", header: h}); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %s", name, resp.Status)
		}
	}
	if resp, _ := r.do(req{path: "/control/kill", header: map[string]string{"Authorization": "Bearer " + killToken}}); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %s", resp.Status)
	}
	if r.k.Killed() {
		t.Fatal("a refused request fired the kill switch")
	}
	// It needs no cookie: it is for a process that is not the viewer.
	resp, body := r.do(req{method: http.MethodPost, path: "/control/kill", header: map[string]string{"Authorization": "Bearer " + killToken}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"killed":true`) {
		t.Fatalf("the kill: %s %q", resp.Status, body)
	}
	if res := r.waitKilled(); res.Source != killswitch.SourceCLI {
		t.Errorf("the kill: %+v", res)
	}
}

func TestTheKillClientOfTheCLIReachesTheServer(t *testing.T) {
	// `controlsnack kill` finds the session through its lock and sends the bearer token to the recorded address; the
	// server that the viewer starts has to be the one that answers it.
	r := start(t)
	resp, _ := r.do(req{method: http.MethodPost, path: killswitch.ControlKillPath, header: map[string]string{"Authorization": "Bearer " + killToken}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s", resp.Status)
	}
	r.waitKilled()
}

func TestTheSocketNeedsTheCookieAndTheOrigin(t *testing.T) {
	r := start(t)
	c := r.login()
	page := r.page()
	with := func(k, v string) map[string]string {
		h := r.page()
		h[k] = v
		return h
	}
	without := func(k string) map[string]string {
		h := r.page()
		delete(h, k)
		return h
	}
	for name, tc := range map[string]struct {
		cookie *http.Cookie
		h      map[string]string
		want   int
	}{
		"no cookie":       {nil, page, http.StatusUnauthorized},
		"wrong cookie":    {&http.Cookie{Name: viewer.CookieName, Value: "x"}, page, http.StatusUnauthorized},
		"no origin":       {c, without("Origin"), http.StatusForbidden},
		"another origin":  {c, with("Origin", "http://evil.example"), http.StatusForbidden},
		"localhost":       {c, with("Origin", strings.Replace(r.s.Origin(), "127.0.0.1", "localhost", 1)), http.StatusForbidden},
		"cross-site":      {c, with("Sec-Fetch-Site", "cross-site"), http.StatusForbidden},
		"two origins":     {c, nil, http.StatusForbidden}, // set below
		"POST is no open": {c, page, http.StatusMethodNotAllowed},
	} {
		q := req{path: "/ws", cookie: tc.cookie, header: tc.h}
		if name == "two origins" {
			q.header = page
			hr := r.newRequest(http.MethodGet, "/ws", nil)
			hr.AddCookie(c)
			hr.Header.Add("Origin", r.s.Origin())
			hr.Header.Add("Origin", r.s.Origin())
			resp, err := r.client.Do(hr)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("%s: %s", name, resp.Status)
			}
			continue
		}
		if name == "POST is no open" {
			q.method = http.MethodPost
		}
		if resp, _ := r.do(q); resp.StatusCode != tc.want {
			t.Errorf("%s: %s, want %d", name, resp.Status, tc.want)
		}
	}
	select {
	case <-r.sock.entered:
		t.Fatal("a refused request reached the socket handler")
	default:
	}
	// The window itself gets through.
	r.sock.release = make(chan struct{})
	done := make(chan string, 1)
	go func() { _, b := r.do(req{path: "/ws", cookie: c, header: page}); done <- b }()
	<-r.sock.entered
	close(r.sock.release)
	if b := <-done; b != "socket done" {
		t.Errorf("body %q", b)
	}
}

func TestOnlyOneSocketAtATime(t *testing.T) {
	r := start(t)
	c := r.login()
	first := make(chan struct{})
	go func() { r.do(req{path: "/ws", cookie: c, header: r.page()}); close(first) }()
	<-r.sock.entered

	resp, _ := r.do(req{path: "/ws", cookie: c, header: r.page()})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("a second connection: %s", resp.Status)
	}
	select {
	case <-r.sock.entered:
		t.Fatal("the second connection reached the handler")
	default:
	}
	if r.count(viewer.EventRefused) == 0 {
		t.Error("the second connection was not reported")
	}

	// When the first is over, the window can connect again (a reload).
	close(r.sock.release)
	<-first
	r.sock.release = make(chan struct{})
	again := make(chan int, 1)
	go func() { resp, _ := r.do(req{path: "/ws", cookie: c, header: r.page()}); again <- resp.StatusCode }()
	<-r.sock.entered
	close(r.sock.release)
	if got := <-again; got != http.StatusOK {
		t.Errorf("after the first closed: %d", got)
	}
}

func TestAWindowThatLogsInOverAnotherEndsItsSocket(t *testing.T) {
	r := start(t)
	old := r.login()
	done := make(chan struct{})
	go func() { r.do(req{path: "/ws", cookie: old, header: r.page()}); close(done) }()
	<-r.sock.entered
	ctx := r.sock.lastCtx()

	fresh := r.login()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the old window's socket was not ended")
	}
	<-done
	if resp, _ := r.do(req{path: "/", cookie: old}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the old cookie: %s", resp.Status)
	}
	if resp, _ := r.do(req{path: "/", cookie: fresh}); resp.StatusCode != http.StatusOK {
		t.Errorf("the new cookie: %s", resp.Status)
	}
}

func TestClosingTheServerEndsTheSocketAndStopsListening(t *testing.T) {
	r := start(t)
	c := r.login()
	done := make(chan struct{})
	go func() {
		// The connection is cut by Close, so the client may well see an error and not an answer.
		hr := r.newRequest(http.MethodGet, "/ws", nil)
		hr.AddCookie(c)
		for k, v := range r.page() {
			hr.Header.Set(k, v)
		}
		if resp, err := r.client.Do(hr); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		close(done)
	}()
	<-r.sock.entered
	ctx := r.sock.lastCtx()
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the socket's context did not end")
	}
	<-done
	if reachable(r.s.Addr(), time.Second) {
		t.Error("the port is still open")
	}
	if resp, err := (&http.Client{Timeout: time.Second}).Do(r.newRequest(http.MethodGet, "/", nil)); err == nil {
		_ = resp.Body.Close()
		t.Error("the server still answers")
	}
}

func TestTheServerStopsWhenItsContextEnds(t *testing.T) {
	r := &rig{t: t, clock: newClock(), sock: newSocket()}
	s, err := viewer.New(viewer.Options{Static: staticFiles, Killer: killswitch.New(killswitch.Options{}), KillToken: killToken, WebSocket: r.sock})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	addr := s.Addr()
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if !reachable(addr, 200*time.Millisecond) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the server kept listening after its context ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
