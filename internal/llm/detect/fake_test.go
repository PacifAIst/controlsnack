// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeModel is a model on a fake server: what the server says about it, and what it does when asked.
type fakeModel struct {
	Name, Digest string
	// What the server reports (Ollama's /api/show, LM Studio's /api/v0/models); Reported says it reports anything.
	Reported      bool
	Vision, Tools bool
	Loaded        bool
	ContextLen    int
	Embeddings    bool
	// What the model does when it is asked.
	Sees       map[string]bool // image kind (png, webp, jpeg) -> answers red
	Native     bool
	Structured bool
	JSONPrompt bool
	// RejectImages makes the server refuse a request that has a picture; NativeAsText makes the model write its tool call
	// as text, as one does that the server does not pass tools to.
	// Points is how the model says where something is: "" or "pixels", "relative" (0 to 1000), "neither" or "words" (no position at all).
	Points       string
	RejectImages bool
	NativeAsText bool
	// FailWhen, when set, gives the HTTP status of a request it wants to fail (0 for one it lets through).
	FailWhen func(seen) int
	// ChatStatus, when not 0, is the HTTP status of every chat call, with ChatBody as its body.
	ChatStatus int
	ChatBody   string
}

// strongModel does everything.
func strongModel(name string) fakeModel {
	return fakeModel{
		Name: name, Digest: "sha256:aaaa", Reported: true, Vision: true, Tools: true, ContextLen: 32768,
		Sees: map[string]bool{"png": true, "webp": true, "jpeg": true}, Native: true, Structured: true, JSONPrompt: true,
	}
}

// seen is what one chat request asked, whatever the protocol.
type seen struct {
	Path      string
	ImageKind string // "" when no picture was attached
	Tools     bool
	Format    string // "", "json" or "schema"
	Auth      string
	Key       string
	Version   string
	Prompt    string
}

const doneJSON = `{"thought":"checking","actions":[{"tool":"done","args":{"success":true,"summary":"ok"}}]}`

// fakeServer serves one protocol ("ollama", "openai" or "anthropic") for a list of models.
type fakeServer struct {
	*httptest.Server
	mu       sync.Mutex
	models   []fakeModel
	chats    []seen
	lists    int
	noV0     bool // LM Studio's own list is not there
	noList   bool // no list of models at all
	apiKey   string
	protocol string
}

func newFake(t *testing.T, protocol string, models ...fakeModel) *fakeServer {
	t.Helper()
	f := &fakeServer{models: models, protocol: protocol}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) chatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chats)
}

func (f *fakeServer) seenChats() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.chats...)
}

func (f *fakeServer) setModels(m ...fakeModel) {
	f.mu.Lock()
	f.models = m
	f.mu.Unlock()
}

func (f *fakeServer) find(name string) (fakeModel, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.models {
		if m.Name == name {
			return m, true
		}
	}
	return fakeModel{}, false
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if s, ok := body.(string); ok {
		_, _ = io.WriteString(w, s)
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if f.apiKey != "" {
		got := r.Header.Get("Authorization")
		if f.protocol == "anthropic" {
			got = "Bearer " + r.Header.Get("x-api-key")
		}
		if got != "Bearer "+f.apiKey {
			reply(w, http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`)
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
		f.listOllama(w)
	case r.Method == http.MethodPost && r.URL.Path == "/api/show":
		f.showOllama(w, body)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v0/models":
		if f.noV0 || f.noList {
			reply(w, http.StatusNotFound, `{"error":"not found"}`)
			return
		}
		f.listLMStudio(w)
	case r.Method == http.MethodGet && (r.URL.Path == "/models" || r.URL.Path == "/v1/models"):
		if f.noList {
			reply(w, http.StatusNotFound, `{"error":"not found"}`)
			return
		}
		f.listPlain(w)
	case r.Method == http.MethodPost && (r.URL.Path == "/api/chat" || r.URL.Path == "/chat/completions" || r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/messages"):
		f.chat(w, r, body)
	default:
		reply(w, http.StatusNotFound, `{"error":"not found"}`)
	}
}

func (f *fakeServer) listOllama(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	var list []map[string]any
	for _, m := range f.models {
		list = append(list, map[string]any{"name": m.Name, "model": m.Name, "digest": m.Digest})
	}
	reply(w, 200, map[string]any{"models": list})
}

func (f *fakeServer) showOllama(w http.ResponseWriter, body []byte) {
	var req struct{ Model string }
	_ = json.Unmarshal(body, &req)
	m, ok := f.find(req.Model)
	if !ok {
		reply(w, 404, `{"error":"model not found"}`)
		return
	}
	out := map[string]any{"model_info": map[string]any{"general.architecture": "qwen2", "qwen2.context_length": m.ContextLen}}
	if m.Reported {
		caps := []string{"completion"}
		if m.Vision {
			caps = append(caps, "vision")
		}
		if m.Tools {
			caps = append(caps, "tools")
		}
		out["capabilities"] = caps
	}
	reply(w, 200, out)
}

func (f *fakeServer) listLMStudio(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	var list []map[string]any
	for _, m := range f.models {
		typ, state := "llm", "not-loaded"
		switch {
		case m.Embeddings:
			typ = "embeddings"
		case m.Vision:
			typ = "vlm"
		}
		if m.Loaded {
			state = "loaded"
		}
		e := map[string]any{"id": m.Name, "type": typ, "state": state, "max_context_length": m.ContextLen}
		if m.Tools {
			e["capabilities"] = []string{"tool_use"}
		} else {
			e["capabilities"] = []string{}
		}
		list = append(list, e)
	}
	reply(w, 200, map[string]any{"object": "list", "data": list})
}

func (f *fakeServer) listPlain(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	var list []map[string]any
	for _, m := range f.models {
		list = append(list, map[string]any{"id": m.Name, "object": "model"})
	}
	reply(w, 200, map[string]any{"object": "list", "data": list})
}

// imageKind says what a base64 picture is by its first bytes.
func imageKind(b64 string) string {
	data, err := base64.StdEncoding.DecodeString(b64)
	switch {
	case err != nil:
		return "garbage"
	case bytes.HasPrefix(data, []byte("\x89PNG")):
		return "png"
	case len(data) > 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8}):
		return "jpeg"
	}
	return "unknown"
}

// readChat turns the request of any of the three protocols into what it asked.
func (f *fakeServer) readChat(r *http.Request, body []byte) (model string, s seen) {
	s.Path = r.URL.Path
	s.Auth = r.Header.Get("Authorization")
	s.Key = r.Header.Get("x-api-key")
	s.Version = r.Header.Get("anthropic-version")
	var req struct {
		Model          string            `json:"model"`
		Messages       []json.RawMessage `json:"messages"`
		Tools          json.RawMessage   `json:"tools"`
		Format         json.RawMessage   `json:"format"`
		ResponseFormat struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	_ = json.Unmarshal(body, &req)
	s.Tools = len(req.Tools) > 0
	switch {
	case len(req.Format) > 0 && string(req.Format) == `"json"`:
		s.Format = "json"
	case len(req.Format) > 0 && req.Format[0] == '{':
		s.Format = "schema"
	case req.ResponseFormat.Type == "json_schema":
		s.Format = "schema"
	case req.ResponseFormat.Type == "json_object":
		s.Format = "json"
	}
	for _, raw := range req.Messages {
		var m struct {
			Content json.RawMessage `json:"content"`
			Images  []string        `json:"images"`
		}
		_ = json.Unmarshal(raw, &m)
		for _, img := range m.Images {
			s.ImageKind = imageKind(img)
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			s.Prompt += text
			continue
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
			Source struct {
				Data string `json:"data"`
			} `json:"source"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, p := range parts {
				s.Prompt += p.Text
				switch {
				case p.ImageURL.URL != "":
					s.ImageKind = imageKind(p.ImageURL.URL[strings.Index(p.ImageURL.URL, ",")+1:])
				case p.Source.Data != "":
					s.ImageKind = imageKind(p.Source.Data)
				}
			}
		}
	}
	return req.Model, s
}

// answer is what the model says to a request: the text, or the tool it calls.
type answer struct {
	text string
	tool bool
}

func (m fakeModel) answer(s seen) (answer, int, string) {
	if m.ChatStatus != 0 {
		return answer{}, m.ChatStatus, m.ChatBody
	}
	if m.FailWhen != nil {
		if status := m.FailWhen(s); status != 0 {
			return answer{}, status, `{"error":{"message":"invalid api key"}}`
		}
	}
	switch {
	case s.ImageKind != "":
		if m.RejectImages {
			return answer{}, 400, `{"error":"this model does not accept images"}`
		}
		if strings.Contains(s.Prompt, ProbePointQuestion) {
			switch m.Points {
			case "relative":
				return answer{text: `{"x":718,"y":350}`}, 0, ""
			case "neither":
				return answer{text: `{"x":10,"y":390}`}, 0, ""
			case "words":
				return answer{text: "It is somewhere in the picture."}, 0, ""
			}
			return answer{text: `{"x":458,"y":142}`}, 0, ""
		}
		if m.Sees[s.ImageKind] {
			return answer{text: `{"color":"red"}`}, 0, ""
		}
		return answer{text: `{"color":"white"}`}, 0, ""
	case s.Tools:
		if m.NativeAsText {
			return answer{text: doneJSON}, 0, ""
		}
		if !m.Native {
			return answer{}, 400, `{"error":"` + m.Name + ` does not support tools"}`
		}
		return answer{tool: true}, 0, ""
	case s.Format == "schema":
		if m.Structured {
			return answer{text: doneJSON}, 0, ""
		}
		return answer{text: "I would rather explain it in words."}, 0, ""
	}
	if m.JSONPrompt {
		return answer{text: "```json\n" + doneJSON + "\n```"}, 0, ""
	}
	return answer{text: "Sure! I have done it."}, 0, ""
}

func (f *fakeServer) chat(w http.ResponseWriter, r *http.Request, body []byte) {
	name, s := f.readChat(r, body)
	f.mu.Lock()
	f.chats = append(f.chats, s)
	f.mu.Unlock()
	m, ok := f.find(name)
	if !ok {
		reply(w, 404, `{"error":"model '`+name+`' not found"}`)
		return
	}
	a, status, errBody := m.answer(s)
	if status != 0 {
		reply(w, status, errBody)
		return
	}
	args := map[string]any{"success": true, "summary": "ok"}
	switch f.protocol {
	case "ollama":
		msg := map[string]any{"role": "assistant", "content": a.text}
		if a.tool {
			msg["tool_calls"] = []any{map[string]any{"function": map[string]any{"name": "done", "arguments": args}}}
		}
		reply(w, 200, map[string]any{"model": name, "message": msg, "done": true, "done_reason": "stop"})
	case "openai":
		msg := map[string]any{"role": "assistant", "content": a.text}
		if a.tool {
			raw, _ := json.Marshal(args)
			msg["tool_calls"] = []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "done", "arguments": string(raw)}}}
		}
		reply(w, 200, map[string]any{
			"model":   name,
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	case "anthropic":
		var content []any
		if a.text != "" {
			content = append(content, map[string]any{"type": "text", "text": a.text})
		}
		if a.tool {
			content = append(content, map[string]any{"type": "tool_use", "id": "toolu_1", "name": "done", "input": args})
		}
		reply(w, 200, map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": name, "content": content,
			"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}
}
