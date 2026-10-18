// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
)

// maxLinkLen is the longest login token the server looks at; its own are 64 characters.
const maxLinkLen = 256

// handleAuth turns a login link into the session cookie. The link is good once: using it a second time means that
// another process on this machine got hold of it, and the session is ended.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) { // HEAD would use the link up
		return
	}
	tokens := r.URL.Query()["t"]
	if len(tokens) != 1 || tokens[0] == "" || len(tokens[0]) > maxLinkLen {
		s.events.send(EventAuthDenied, "a login request without a usable link")
		http.Error(w, "this link is not valid; open the viewer from the link the program printed", http.StatusUnauthorized)
		return
	}
	cookie, out, err := s.auth.consume(tokens[0])
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch out {
	case linkOK:
		// No Secure attribute: the page is plain http on loopback, where a browser such as Safari would not keep a Secure cookie.
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode}) //nolint:gosec // see above
		s.events.send(EventAuth, "a window logged in")
		w.Header().Set("Location", PathIndex)
		w.WriteHeader(http.StatusSeeOther)
	case linkConsumed:
		s.events.send(EventTripwire, "a login link was used a second time")
		s.opts.Killer.TriggerAsync(killswitch.SourceInternal, "the viewer's login link was used twice: another process on this machine may have raced the viewer")
		http.Error(w, "this link was already used", http.StatusForbidden)
	default: // unknown or expired
		s.events.send(EventAuthDenied, "a login link that is "+map[outcome]string{linkUnknown: "unknown", linkExpired: "expired"}[out])
		http.Error(w, "this link is not valid or has expired", http.StatusUnauthorized)
	}
}

// fromThisPage reports whether a request that changes something comes from the viewer's own page: the Origin header,
// when the browser sends one (a socket must), is the viewer's, and the browser's fetch metadata, when it sends it, says
// the request is same-origin.
func (s *Server) fromThisPage(r *http.Request, originRequired bool) bool {
	switch o := r.Header.Values("Origin"); {
	case len(o) > 1, len(o) == 1 && o[0] != s.Origin(), len(o) == 0 && originRequired:
		return false
	}
	if v := r.Header.Get("Sec-Fetch-Site"); v != "" && v != "same-origin" {
		return false
	}
	return true
}

// handleWS lets the logged-in window open the one socket.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	life, ok := s.auth.valid(r)
	if !ok {
		s.refuse(w, http.StatusUnauthorized, "cookie", "a socket without the viewer's cookie")
		return
	}
	if !s.fromThisPage(r, true) {
		s.refuse(w, http.StatusForbidden, "origin", "a socket from another origin")
		return
	}
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	default:
		s.refuse(w, http.StatusConflict, "second connection", "a viewer is already connected")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(life, cancel) // a window that logs in over this one ends it
	defer stop()
	s.opts.WebSocket.ServeHTTP(w, r.WithContext(ctx))
}

// handleKill is the big red button for when the socket is down: the cookie, the CSRF value and the origin all have to
// be right.
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	if _, ok := s.auth.valid(r); !ok {
		s.refuse(w, http.StatusUnauthorized, "cookie", "a kill request without the viewer's cookie")
		return
	}
	if !secret.Equal(r.Header.Get(HeaderCSRF), s.csrf) {
		s.refuse(w, http.StatusForbidden, "csrf", "a kill request with a wrong CSRF value")
		return
	}
	if !s.fromThisPage(r, false) {
		s.refuse(w, http.StatusForbidden, "origin", "a kill request from another origin")
		return
	}
	start := s.opts.Now()
	k := s.opts.Killer
	k.TriggerAsync(killswitch.SourceAPI, "POST /api/kill")
	gone := false
	select {
	case <-k.QEMUGone():
		gone = true
	case <-time.After(800 * time.Millisecond):
	case <-r.Context().Done():
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"killed": true, "qemu_gone": gone, "latency_ms": s.opts.Now().Sub(start).Milliseconds()})
}

// contentTypes are the only kinds of file the server serves, with the type each is sent as. The table is fixed because
// the operating system's own (the Windows registry's) can name a script as plain text.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".json":  "application/json",
	".woff2": "font/woff2",
}

// handleStatic serves the front-end to the window that logged in, and nothing else: a name that is not a file of the
// embedded tree, a directory or a kind not in the table is not found.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	if _, ok := s.auth.valid(r); !ok {
		s.refuse(w, http.StatusUnauthorized, "cookie", "the page without the viewer's cookie")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	ct, ok := contentTypes[path.Ext(name)]
	if !ok || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.opts.Static, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data) //nolint:gosec // the bytes are a file of our own embedded front-end; the request only picks one of them
	}
}

// handleCSRF gives the page the CSRF value for the kill button when the socket has not (yet) told it in the hello message,
// so that the button works from the first moment the page is open. Only the logged-in window can ask, and no other site can
// read the answer: no CORS header is ever sent, and the cookie is SameSite=Strict.
func (s *Server) handleCSRF(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	if _, ok := s.auth.valid(r); !ok {
		s.refuse(w, http.StatusUnauthorized, "cookie", "the CSRF value without the viewer's cookie")
		return
	}
	if !s.fromThisPage(r, false) {
		s.refuse(w, http.StatusForbidden, "origin", "the CSRF value for another origin")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"csrf": s.csrf})
}
