// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package fakellm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/platform"
)

// Profile is what the fake model can do. The probe is answered from it, and a request that asks for what the profile lacks
// is refused as a real server refuses it (a 400), so a session that picks a mode the model does not have fails in the test.
type Profile struct {
	// Vision is whether it takes pictures; WebP whether it reads them as WebP (otherwise it does not see the picture).
	Vision, WebP bool
	// Native, Structured and JSONPrompt are the ways it can name its actions.
	Native, Structured, JSONPrompt bool
	// Relative makes it point on the scale of 0 to 1000 across each axis (as the Qwen-VL family does) and not in pixels.
	Relative bool
}

// AllCapabilities is a model that does everything.
func AllCapabilities() Profile {
	return Profile{Vision: true, WebP: true, Native: true, Structured: true, JSONPrompt: true}
}

// Options are what a fake model server is made from.
type Options struct {
	// Model is the name it answers to (fake-vlm when empty).
	Model string
	// Profile is what it can do (AllCapabilities when nil).
	Profile *Profile
	// APIKey, when not empty, must be sent as a bearer token.
	APIKey string
	// Script is the answers to the conversation, in order. The probe is not part of it.
	Script []Step
	// Then answers the requests that come after the script has ended; without it they are an error of the test.
	Then func(*Request) Reply
	// Context is the context length it reports (32768 when 0).
	Context int
}

// Server is a fake model server on a loopback port.
type Server struct {
	opts    Options
	profile Profile
	l       net.Listener
	srv     *http.Server
	closing chan struct{}
	once    sync.Once

	mu     sync.Mutex
	next   int
	calls  int
	reqs   []*Request
	probes int
	errs   []string
}

// Serve starts a server on a free port of the loopback interface.
func Serve(opts Options) (*Server, error) {
	if opts.Model == "" {
		opts.Model = "fake-vlm"
	}
	if opts.Context == 0 {
		opts.Context = 32768
	}
	s := &Server{opts: opts, profile: AllCapabilities(), closing: make(chan struct{})}
	if opts.Profile != nil {
		s.profile = *opts.Profile
	}
	l, err := platform.ListenLoopback(context.Background(), 0)
	if err != nil {
		return nil, err
	}
	s.l = l
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(l) }()
	return s, nil
}

// Start starts a server for a test: it is closed when the test ends, and a request that went against the script fails the
// test then.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	s, err := Serve(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		for _, e := range s.Errors() {
			t.Errorf("fakellm: %s", e)
		}
	})
	return s
}

// URL is the base address for --base-url: it ends with /v1, as the OpenAI-compatible servers' do.
func (s *Server) URL() string { return "http://" + s.l.Addr().String() + "/v1" }

// Model is the name the server answers to.
func (s *Server) Model() string { return s.opts.Model }

// Close stops the server and ends the requests that hang.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.closing)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
		_ = s.srv.Close()
	})
}

// Requests are the conversation requests the server got, oldest first (the probe's are not among them).
func (s *Server) Requests() []*Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Request(nil), s.reqs...)
}

// Probes counts the probe's questions the server answered.
func (s *Server) Probes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probes
}

// Errors lists what went against the script: a request the step did not expect, a request after the script ended.
func (s *Server) Errors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.errs...)
}

// ClearErrors forgets them, for a test that makes the server diverge on purpose.
func (s *Server) ClearErrors() {
	s.mu.Lock()
	s.errs = nil
	s.mu.Unlock()
}

// Remaining is how many steps of the script have not been used.
func (s *Server) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(len(s.opts.Script)-s.next, 0)
}

func (s *Server) fail(format string, args ...any) {
	s.mu.Lock()
	s.errs = append(s.errs, fmt.Sprintf(format, args...))
	s.mu.Unlock()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if s.opts.APIKey != "" && r.Header.Get("Authorization") != "Bearer "+s.opts.APIKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "Incorrect API key provided", "type": "invalid_request_error", "code": "invalid_api_key"}})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	switch {
	case r.Method == http.MethodGet && path == "/models":
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]any{{"id": s.opts.Model, "object": "model", "owned_by": "fakellm"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v0/models":
		s.lmstudioList(w)
	case r.Method == http.MethodPost && path == "/chat/completions":
		s.chat(w, r, body)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "unknown route " + r.Method + " " + r.URL.Path}})
	}
}

// lmstudioList is LM Studio's own list of models, which says whether a model sees and calls tools.
func (s *Server) lmstudioList(w http.ResponseWriter) {
	typ := "llm"
	if s.profile.Vision {
		typ = "vlm"
	}
	caps := []string{}
	if s.profile.Native {
		caps = append(caps, "tool_use")
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]any{{
		"id": s.opts.Model, "object": "model", "type": typ, "state": "loaded", "max_context_length": s.opts.Context, "loaded_context_length": s.opts.Context, "capabilities": caps,
	}}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errorBody(msg string) string {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error"}})
	return string(raw)
}

func (s *Server) chat(w http.ResponseWriter, hr *http.Request, body []byte) {
	req, err := decode(body, hr.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "the request is not JSON: " + err.Error()}})
		return
	}
	if req.Model != s.opts.Model {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": fmt.Sprintf("The model `%s` does not exist", req.Model), "code": "model_not_found"}})
		return
	}
	var reply Reply
	if rep, ok := s.probe(req); ok {
		s.mu.Lock()
		s.probes++
		s.mu.Unlock()
		reply = rep
	} else if refusal, refused := s.refuse(req); refused {
		reply = refusal
		s.fail("a request asked for what the model's profile lacks: %s", refusal.Body)
	} else {
		reply = s.scripted(req)
	}
	s.send(w, hr, req, reply)
}

// refuse answers as a real server does a request for what the model cannot do.
func (s *Server) refuse(r *Request) (Reply, bool) {
	switch {
	case r.Pictures() > 0 && !s.profile.Vision:
		return Reply{Status: http.StatusBadRequest, Body: errorBody("this model does not accept images")}, true
	case len(r.Tools) > 0 && !s.profile.Native:
		return Reply{Status: http.StatusBadRequest, Body: errorBody(s.opts.Model + " does not support tools")}, true
	case r.Format == "json_schema" && !s.profile.Structured:
		return Reply{Status: http.StatusBadRequest, Body: errorBody("response_format json_schema is not supported by this model")}, true
	}
	return Reply{}, false
}

// scripted takes the next step of the script for a conversation request.
func (s *Server) scripted(r *Request) Reply {
	s.mu.Lock()
	idx := s.next
	s.next++
	s.reqs = append(s.reqs, r)
	s.mu.Unlock()
	if idx >= len(s.opts.Script) {
		if s.opts.Then != nil {
			return s.opts.Then(r)
		}
		s.fail("request %d came after the script of %d answers had ended", idx+1, len(s.opts.Script))
		return Reply{Status: http.StatusInternalServerError, Body: errorBody("the script of fakellm has no more answers")}
	}
	step := s.opts.Script[idx]
	if step.When != nil && !step.When(r) {
		last := r.Observation()
		if len(last) > 200 {
			last = last[:200] + "..."
		}
		s.fail("request %d did not match what step %d expected; its last user message is %q", idx+1, idx+1, last)
		return Reply{Status: http.StatusConflict, Body: errorBody("the request is not the one the script expected")}
	}
	if step.Reply == nil {
		return Reply{}
	}
	return step.Reply(r)
}

// send writes a reply.
func (s *Server) send(w http.ResponseWriter, hr *http.Request, req *Request, rep Reply) {
	if rep.Hang {
		select {
		case <-hr.Context().Done():
		case <-s.closing:
		}
		return
	}
	if rep.Delay > 0 {
		select {
		case <-time.After(rep.Delay):
		case <-hr.Context().Done():
			return
		case <-s.closing:
			return
		}
	}
	if rep.Status != 0 {
		body := rep.Body
		if body == "" {
			body = errorBody("fakellm: HTTP " + strconv.Itoa(rep.Status))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.Status)
		_, _ = io.WriteString(w, body)
		return
	}
	if rep.Garbage != "" {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, rep.Garbage)
		return
	}
	writeJSON(w, http.StatusOK, s.completion(req, rep))
}

// completion is the answer in the shape of the API.
func (s *Server) completion(req *Request, rep Reply) map[string]any {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()

	msg := map[string]any{"role": "assistant", "content": rep.Text}
	if rep.Text == "" && len(rep.Calls) > 0 {
		msg["content"] = nil
	}
	if len(rep.Calls) > 0 {
		calls := make([]map[string]any, len(rep.Calls))
		for i, c := range rep.Calls {
			id := c.ID
			if id == "" {
				id = fmt.Sprintf("call_%d_%d", n, i+1)
			}
			args := c.Args
			if args == "" {
				args = "{}"
			}
			calls[i] = map[string]any{"id": id, "type": "function", "function": map[string]any{"name": c.Name, "arguments": args}}
		}
		msg["tool_calls"] = calls
	}
	finish := rep.Finish
	if finish == "" {
		finish = "stop"
		if len(rep.Calls) > 0 {
			finish = "tool_calls"
		}
	}
	prompt := 0
	for _, m := range req.Messages {
		prompt += len(m.Text)/4 + 4 + 300*len(m.Images)
	}
	completion := len(rep.Text) / 4
	for _, c := range rep.Calls {
		completion += (len(c.Name) + len(c.Args)) / 4
	}
	return map[string]any{
		"id": fmt.Sprintf("chatcmpl-fake-%d", n), "object": "chat.completion", "created": time.Now().Unix(), "model": s.opts.Model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion},
	}
}
