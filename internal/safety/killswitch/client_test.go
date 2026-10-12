// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package killswitch

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// scene is a running "session": a lock file, a fake process table, a Killer whose QEMU dies for real in the table,
// and an HTTP server with the real control handler.
type scene struct {
	procs  *fakeProcs
	reg    *Registry
	killer *Killer
	srv    *httptest.Server
	hits   atomic.Int32
	token  string
}

type tableQEMU struct {
	procs  *fakeProcs
	pid    int
	exited chan struct{}
}

func (q *tableQEMU) Kill() error {
	err := q.procs.Kill(q.pid)
	close(q.exited)
	return err
}
func (q *tableQEMU) Exited() <-chan struct{} { return q.exited }

func newScene(t *testing.T, mutateEntry func(*Entry), handlerWrap func(http.Handler) http.Handler) *scene {
	t.Helper()
	s := &scene{procs: newFakeProcs(), token: "the-right-token-0123456789abcdef"}
	s.reg = newRegistry(t, s.procs)
	s.procs.add(100, "c-start")
	s.procs.add(101, "q-start")
	s.killer = New(Options{})
	s.killer.SetQEMU(&tableQEMU{procs: s.procs, pid: 101, exited: make(chan struct{})})

	h := ControlHandler(s.killer, s.token)
	if handlerWrap != nil {
		h = handlerWrap(h)
	}
	mux := http.NewServeMux()
	mux.Handle(ControlKillPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.hits.Add(1); h.ServeHTTP(w, r) }))
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)

	e := entry("s_live", 100)
	e.ControlURL = s.srv.URL
	e.KillToken = s.token
	e.QEMUPID, e.QEMUStarted = 101, "q-start"
	if mutateEntry != nil {
		mutateEntry(&e)
	}
	if _, _, err := s.reg.Acquire(e); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *scene) client() *ControlClient {
	c := NewControlClient(s.reg)
	c.AckTimeout = 500 * time.Millisecond
	c.GoneTimeout = time.Second
	return c
}

func TestKillThroughTheControlEndpoint(t *testing.T) {
	s := newScene(t, nil, nil)
	hookRan := false
	addHook(t, s.killer, "t", func(context.Context) error { hookRan = true; return nil })
	var out bytes.Buffer
	start := time.Now()
	if err := s.client().Kill(context.Background(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("kill took %v; the target is well under a second", took)
	}
	if !strings.Contains(out.String(), "killed session s_live") || !strings.Contains(out.String(), "QEMU gone after") {
		t.Errorf("output: %q", out.String())
	}
	<-s.killer.Done()
	res, _ := s.killer.Result()
	if res.Source != SourceCLI || !res.QEMUKilled || !hookRan {
		t.Errorf("%+v hook=%v", res, hookRan)
	}
	if s.procs.Alive(101) {
		t.Error("QEMU must be gone")
	}
	if got := s.procs.killedList(); len(got) != 1 {
		t.Errorf("killed %v: the controller shut itself down, the client must not have killed anything", got)
	}
}

func TestKillFallsBackWhenTheTokenIsRefused(t *testing.T) {
	s := newScene(t, func(e *Entry) { e.KillToken = "a-wrong-token-0123456789abcdef" }, nil)
	var out bytes.Buffer
	if err := s.client().Kill(context.Background(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if s.killer.Killed() {
		t.Error("a wrong token must not fire the kill switch")
	}
	if !strings.Contains(out.String(), "did not answer") || !strings.Contains(out.String(), "401") {
		t.Errorf("output should say why it fell back: %q", out.String())
	}
	if s.procs.Alive(101) || s.procs.Alive(100) {
		t.Error("the fallback must end QEMU and the controller")
	}
	if cur, _ := s.reg.Read(); cur != nil {
		t.Errorf("the lock must be cleaned up: %+v", cur)
	}
}

func TestKillFallsBackWhenTheSessionIsUnreachable(t *testing.T) {
	s := newScene(t, nil, nil)
	s.srv.Close()
	var out bytes.Buffer
	if err := s.client().Kill(context.Background(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if s.procs.Alive(101) || s.procs.Alive(100) {
		t.Errorf("unreachable session: both processes must be ended\n%s", out.String())
	}
	if got := s.procs.killedList(); len(got) != 2 || got[0] != 101 || got[1] != 100 {
		t.Errorf("killed %v; QEMU must go first, then the controller", got)
	}
}

func TestKillFallsBackWhenTheSessionIsTooSlow(t *testing.T) {
	s := newScene(t, nil, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		})
	})
	c := s.client()
	c.AckTimeout = 100 * time.Millisecond
	var out bytes.Buffer
	start := time.Now()
	if err := c.Kill(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("the fallback must not wait for a session that does not answer")
	}
	if s.procs.Alive(101) || s.procs.Alive(100) {
		t.Error("the unresponsive session must be ended directly")
	}
}

func TestFallbackNeverKillsAProcessThatIsNotTheRecordedOne(t *testing.T) {
	s := newScene(t, func(e *Entry) { e.KillToken = "wrong-token-0123456789abcdef0123" }, nil)
	// The recorded QEMU pid is now a different process (reused pid); the controller is the real one.
	s.procs.mu.Lock()
	s.procs.procs[101].start = "someone-else"
	s.procs.mu.Unlock()
	var out bytes.Buffer
	if err := s.client().Kill(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if !s.procs.Alive(101) {
		t.Error("a process that only shares the recorded pid must survive")
	}
	if s.procs.Alive(100) {
		t.Error("the real controller must be ended")
	}
}

func TestTheTokenIsNeverSentOffMachine(t *testing.T) {
	var received atomic.Int32
	spy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer spy.Close()
	for _, url := range []string{"http://192.0.2.7:8080", "http://example.com", "https://127.0.0.1:1", "http://localhost:1", "ftp://127.0.0.1:1", "http://[2001:db8::1]:80", "", "not a url", "http://"} {
		s := newScene(t, func(e *Entry) { e.ControlURL = url }, nil)
		var out bytes.Buffer
		if err := s.client().Kill(context.Background(), &out); err != nil {
			t.Errorf("%q: %v", url, err)
		}
		if s.hits.Load() != 0 {
			t.Errorf("%q: the real server was contacted", url)
		}
		if !strings.Contains(out.String(), "ending the processes directly") {
			t.Errorf("%q: %q", url, out.String())
		}
	}
	if received.Load() != 0 {
		t.Error("the spy server received a request")
	}
}

func TestControlURL(t *testing.T) {
	good := map[string]string{
		"http://127.0.0.1:5000":           "http://127.0.0.1:5000/control/kill",
		"http://127.0.0.1:5000/":          "http://127.0.0.1:5000/control/kill",
		"http://127.0.0.1:5000/evil/path": "http://127.0.0.1:5000/control/kill",
		"http://127.0.0.1:5000/?x=1#f":    "http://127.0.0.1:5000/control/kill",
		"http://u:p@127.0.0.1:5000":       "http://127.0.0.1:5000/control/kill",
		"http://[::1]:5000":               "http://[::1]:5000/control/kill",
		"http://127.5.5.5:80":             "http://127.5.5.5:80/control/kill",
	}
	for in, want := range good {
		got, err := controlURL(in)
		if err != nil || got != want {
			t.Errorf("controlURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"http://localhost:5000", "http://10.0.0.1:5", "http://0.0.0.0:5", "https://127.0.0.1:5", "", "http:///x", "%zz"} {
		if got, err := controlURL(in); err == nil {
			t.Errorf("controlURL(%q) = %q; must be refused", in, got)
		}
	}
}

func TestKillWithNothingRunning(t *testing.T) {
	reg := newRegistry(t, newFakeProcs())
	var out bytes.Buffer
	if err := NewControlClient(reg).Kill(context.Background(), &out); err != nil || !strings.Contains(out.String(), "no session is running") {
		t.Errorf("%v %q", err, out.String())
	}
}

func TestKillCleansUpAStaleSession(t *testing.T) {
	procs := newFakeProcs()
	reg := newRegistry(t, procs)
	procs.add(101, "q-start")
	e := entry("s_crashed", 100)
	e.QEMUPID, e.QEMUStarted = 101, "q-start"
	if _, _, err := reg.Acquire(e); err != nil { // controller 100 is not in the table: it died
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := NewControlClient(reg).Kill(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no longer running") || !strings.Contains(out.String(), "orphaned QEMU") {
		t.Errorf("%q", out.String())
	}
	if procs.Alive(101) {
		t.Error("the orphaned QEMU must be ended")
	}
	if cur, _ := reg.Read(); cur != nil {
		t.Error("lock must be removed")
	}
	// Idempotent: a second kill finds nothing.
	out.Reset()
	if err := NewControlClient(reg).Kill(context.Background(), &out); err != nil || !strings.Contains(out.String(), "no session is running") {
		t.Errorf("second kill: %v %q", err, out.String())
	}
}

func TestControlHandlerAuthentication(t *testing.T) {
	k := New(Options{})
	h := ControlHandler(k, "the-right-token-0123456789abcdef")
	do := func(method, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, ControlKillPath, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for name, tc := range map[string]struct {
		method, auth string
		code         int
	}{
		"no credentials":       {http.MethodPost, "", http.StatusUnauthorized},
		"wrong token":          {http.MethodPost, "Bearer nope", http.StatusUnauthorized},
		"token without scheme": {http.MethodPost, "the-right-token-0123456789abcdef", http.StatusUnauthorized},
		"basic auth":           {http.MethodPost, "Basic dGhlLXJpZ2h0LXRva2Vu", http.StatusUnauthorized},
		"empty bearer":         {http.MethodPost, "Bearer ", http.StatusUnauthorized},
		"prefix of the token":  {http.MethodPost, "Bearer the-right-token", http.StatusUnauthorized},
		"token with suffix":    {http.MethodPost, "Bearer the-right-token-0123456789abcdefX", http.StatusUnauthorized},
		"GET is refused":       {http.MethodGet, "Bearer the-right-token-0123456789abcdef", http.StatusMethodNotAllowed},
		"PUT is refused":       {http.MethodPut, "Bearer the-right-token-0123456789abcdef", http.StatusMethodNotAllowed},
	} {
		rec := do(tc.method, tc.auth)
		if rec.Code != tc.code {
			t.Errorf("%s: status %d, want %d", name, rec.Code, tc.code)
		}
		if k.Killed() {
			t.Fatalf("%s fired the kill switch", name)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: responses must not be cached", name)
		}
	}
	rec := do(http.MethodPost, "Bearer the-right-token-0123456789abcdef")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"killed":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !k.Killed() {
		t.Error("a correct request fires the kill switch")
	}
	// Repeating the request is harmless.
	if rec := do(http.MethodPost, "Bearer the-right-token-0123456789abcdef"); rec.Code != http.StatusOK {
		t.Errorf("repeat: %d", rec.Code)
	}
}
