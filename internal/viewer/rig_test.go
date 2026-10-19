// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PacifAIst/controlsnack/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

const killToken = "0123456789abcdef0123456789abcdef"

// clock is a clock the tests move by hand.
type clock struct{ ns atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.ns.Store(time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *clock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *clock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

// socket is the stand-in for the WebSocket handler: it says it was entered and then holds the connection until it is
// told to go or its context ends.
type socket struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	ctxs    []context.Context
}

func newSocket() *socket {
	return &socket{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (s *socket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.ctxs = append(s.ctxs, r.Context())
	s.mu.Unlock()
	s.entered <- struct{}{}
	select {
	case <-s.release:
	case <-r.Context().Done():
	}
	_, _ = io.WriteString(w, "socket done")
}

func (s *socket) lastCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctxs[len(s.ctxs)-1]
}

// rig is a running viewer server with a real kill switch.
type rig struct {
	t      *testing.T
	s      *viewer.Server
	k      *killswitch.Killer
	clock  *clock
	sock   *socket
	client *http.Client
	mu     sync.Mutex
	events []viewer.Event
}

var staticFiles = fstest.MapFS{
	"index.html":        {Data: []byte("<!doctype html><title>t</title><script src=\"/app.js\"></script>")},
	"app.js":            {Data: []byte("\"use strict\";")},
	"app.css":           {Data: []byte("body{}")},
	"logo.svg":          {Data: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>")},
	"notes.txt":         {Data: []byte("not a kind of file the viewer serves")},
	"app.js.map":        {Data: []byte("{}")},
	"sub/inner.js":      {Data: []byte("1")},
	"sub/index.html":    {Data: []byte("sub")},
	"data.json":         {Data: []byte("{}")},
	"font/ui.woff2":     {Data: []byte("w")},
	"icons/favicon.ico": {Data: []byte("i")},
}

func start(t *testing.T, mod ...func(*viewer.Options)) *rig {
	t.Helper()
	r := &rig{t: t, clock: newClock(), sock: newSocket()}
	r.k = killswitch.New(killswitch.Options{Now: r.clock.Now})
	o := viewer.Options{
		Static: staticFiles, Killer: r.k, KillToken: killToken, WebSocket: r.sock, Now: r.clock.Now,
		Event: func(e viewer.Event) {
			r.mu.Lock()
			r.events = append(r.events, e)
			r.mu.Unlock()
		},
	}
	for _, m := range mod {
		m(&o)
	}
	s, err := viewer.New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = s.Close() })
	r.s = s
	r.client = &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	return r
}

func (r *rig) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.Kind)
	}
	return out
}

func (r *rig) count(kind string) int {
	n := 0
	for _, k := range r.kinds() {
		if k == kind {
			n++
		}
	}
	return n
}

// req is one request to the server.
type req struct {
	method string
	path   string
	host   string // the Host header; the server's address when empty
	header map[string]string
	cookie *http.Cookie
	body   string
}

// answer is what came back, with its body read and closed.
type answer struct {
	Status     string
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

// Cookies are the cookies the answer set.
func (a *answer) Cookies() []*http.Cookie { return a.cookies }

// newRequest is a request to the server with a context.
func (r *rig) newRequest(method, path string, body io.Reader) *http.Request {
	r.t.Helper()
	hr, err := http.NewRequestWithContext(context.Background(), method, "http://"+r.s.Addr()+path, body)
	if err != nil {
		r.t.Fatal(err)
	}
	return hr
}

func (r *rig) do(q req) (*answer, string) {
	r.t.Helper()
	if q.method == "" {
		q.method = http.MethodGet
	}
	hr := r.newRequest(q.method, q.path, strings.NewReader(q.body))
	if q.host != "" {
		hr.Host = q.host
	}
	for k, v := range q.header {
		hr.Header.Set(k, v)
	}
	if q.cookie != nil {
		hr.AddCookie(q.cookie)
	}
	resp, err := r.client.Do(hr)
	if err != nil {
		r.t.Fatalf("%s %s: %v", q.method, q.path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return &answer{Status: resp.Status, StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}, string(b)
}

// reachable reports whether something accepts connections at the address.
func reachable(addr string, wait time.Duration) bool {
	c, err := (&net.Dialer{Timeout: wait}).DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// login uses a fresh login link and returns the cookie the window got.
func (r *rig) login() *http.Cookie {
	r.t.Helper()
	link, err := r.s.AuthURL()
	if err != nil {
		r.t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		r.t.Fatal(err)
	}
	resp, _ := r.do(req{path: u.RequestURI()})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		r.t.Fatalf("login: %s, Location %q", resp.Status, resp.Header.Get("Location"))
	}
	for _, c := range resp.Cookies() {
		if c.Name == viewer.CookieName {
			return c
		}
	}
	r.t.Fatal("login: no cookie")
	return nil
}

// page is the header set of a request that comes from the viewer's own page.
func (r *rig) page() map[string]string {
	return map[string]string{"Origin": r.s.Origin(), "Sec-Fetch-Site": "same-origin", viewer.HeaderCSRF: r.s.CSRF()}
}

func (r *rig) waitKilled() killswitch.Result {
	r.t.Helper()
	select {
	case <-r.k.Done():
	case <-time.After(5 * time.Second):
		r.t.Fatal("the kill switch did not finish")
	}
	res, _ := r.k.Result()
	return res
}
