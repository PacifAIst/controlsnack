// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
)

// Paths of the server.
const (
	PathAuth  = "/auth"
	PathWS    = "/ws"
	PathKill  = "/api/kill"
	PathIndex = "/"
)

// HeaderCSRF carries the CSRF value (the `csrf` of the hello message) on the requests that change something.
const HeaderCSRF = "X-CS-CSRF"

// DefaultBootstrapTTL is how long a login link is good.
const DefaultBootstrapTTL = 60 * time.Second

// Options configure a Server.
type Options struct {
	// Static is the front-end: the files the server serves, and nothing else.
	Static fs.FS
	// Killer is the kill switch of the session; POST /api/kill and POST /control/kill fire it, and a login link that is
	// used twice (the tripwire) does too.
	Killer *killswitch.Killer
	// KillToken is the bearer token of `controlsnack kill` for POST /control/kill.
	KillToken string
	// WebSocket handles /ws once the request has passed the door. The context of the request ends when the window is
	// replaced or the viewer closes. At most one runs at a time.
	WebSocket http.Handler
	// Event receives what the door saw (see Event). It is called from request goroutines, at a limited rate.
	Event func(Event)
	// BootstrapTTL is the time a login link is good (default DefaultBootstrapTTL).
	BootstrapTTL time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Random makes a secret of n bytes as hex (default secret.NewToken).
	Random func(n int) (string, error)
}

// Server is the viewer's HTTP server.
type Server struct {
	opts   Options
	auth   *auth
	events *events
	csrf   string
	csp    string

	mu     sync.Mutex
	ln     net.Listener
	srv    *http.Server
	addr   string // 127.0.0.1:<port>, what the Host header must be
	origin string // http://127.0.0.1:<port>

	slot chan struct{} // holds a token while a socket is connected
}

// New checks the options and makes a Server that is not listening yet.
func New(o Options) (*Server, error) {
	switch {
	case o.Static == nil:
		return nil, errors.New("viewer: no front-end to serve")
	case o.Killer == nil:
		return nil, errors.New("viewer: no kill switch")
	case len(o.KillToken) < 16:
		return nil, errors.New("viewer: the kill token is missing or too short")
	case o.WebSocket == nil:
		return nil, errors.New("viewer: no handler for the socket")
	}
	if o.BootstrapTTL <= 0 {
		o.BootstrapTTL = DefaultBootstrapTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Random == nil {
		o.Random = secret.NewToken
	}
	csrf, err := o.Random(tokenBytes)
	if err != nil {
		return nil, err
	}
	return &Server{
		opts: o, auth: newAuth(o.Now, o.BootstrapTTL, o.Random), events: newEvents(o.Now, o.Event), csrf: csrf,
		slot: make(chan struct{}, 1),
	}, nil
}

// Start listens on 127.0.0.1 (a free port) and serves until Close or the context ends.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return errors.New("viewer: already started")
	}
	ln, err := platform.ListenLoopback(ctx, 0)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s.ln, s.addr, s.origin = ln, fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("http://127.0.0.1:%d", port)
	s.csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; connect-src 'self' ws://" + s.addr +
		"; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	s.srv = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = s.srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	return nil
}

// Close stops the server and ends whatever the logged-in window had open.
func (s *Server) Close() error {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	s.auth.close()
	if srv == nil {
		return nil
	}
	return srv.Close()
}

// Addr is the address the server listens on ("127.0.0.1:port"), empty before Start.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Origin is the origin of the viewer's page ("http://127.0.0.1:port"), empty before Start.
func (s *Server) Origin() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.origin
}

// CSRF is the value the page must send in HeaderCSRF; it reaches the page in the hello message.
func (s *Server) CSRF() string { return s.csrf }

// AuthURL makes a login link: the address a window opens to log in. It is good once, for BootstrapTTL; nothing may
// fetch it but the window itself (a second use is the tripwire).
func (s *Server) AuthURL() (string, error) {
	origin := s.Origin()
	if origin == "" {
		return "", errors.New("viewer: not started")
	}
	token, err := s.auth.issue()
	if err != nil {
		return "", err
	}
	return origin + PathAuth + "?t=" + url.QueryEscape(token), nil
}

// ServeHTTP is the door. Every request passes the same checks in the same order: the Host header, then the route.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", s.csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")

	if r.Host != s.addr { // a page on another name that points at this port (DNS rebinding) is not served
		s.refuse(w, http.StatusForbidden, "host", "the Host header is not the viewer's address")
		return
	}
	switch r.URL.Path {
	case killswitch.ControlKillPath:
		killswitch.ControlHandler(s.opts.Killer, s.opts.KillToken).ServeHTTP(w, r)
	case PathAuth:
		s.handleAuth(w, r)
	case PathWS:
		s.handleWS(w, r)
	case PathKill:
		s.handleKill(w, r)
	default:
		s.handleStatic(w, r)
	}
}

// refuse answers a request that failed a check, and tells the session (rate limited).
func (s *Server) refuse(w http.ResponseWriter, status int, what, detail string) {
	s.events.send(EventRefused, what+": "+detail)
	http.Error(w, http.StatusText(status), status)
}

// allow answers 405 unless the method is one of those given.
func allow(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	return false
}
