// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package ollama

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack/internal/llm/parse"
)

// Defaults of the client.
const (
	// DefaultBaseURL is where Ollama listens unless told otherwise.
	DefaultBaseURL = "http://127.0.0.1:11434"
	// DefaultNumCtx is the context window asked for (Ollama's own default is much smaller and silently cuts the start of
	// a long conversation).
	DefaultNumCtx = 16384
	// DefaultMaxTokens bounds the length of one answer, so a model that does not stop does not run for minutes.
	DefaultMaxTokens = 2048
	// DefaultKeepAlive is how long Ollama keeps the model in memory after a call.
	DefaultKeepAlive = "30m"
)

// Config configures the client.
type Config struct {
	// BaseURL is Ollama's address (DefaultBaseURL when empty).
	BaseURL string
	// Model is the model to ask, as Ollama names it ("qwen2.5vl:7b").
	Model string
	// NumCtx is the context window to ask for (DefaultNumCtx when 0); the caller caps it at the model's own length.
	NumCtx int
	// MaxTokens limits the answer (DefaultMaxTokens when 0).
	MaxTokens int
	// KeepAlive is how long the model stays loaded after a call (DefaultKeepAlive when empty).
	KeepAlive string
}

// Backend asks an Ollama server.
type Backend struct {
	cfg  Config
	base *url.URL
	http *httpx.Client
}

var _ backend.Backend = (*Backend)(nil)

// New makes a client. client is the HTTP client to use (nil: one with the defaults of the plan).
func New(cfg Config, client *httpx.Client) (*Backend, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	base, err := httpx.ValidateBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("ollama: no model is named")
	}
	if cfg.NumCtx <= 0 {
		cfg.NumCtx = DefaultNumCtx
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.KeepAlive == "" {
		cfg.KeepAlive = DefaultKeepAlive
	}
	if client == nil {
		client = httpx.New("ollama")
	}
	return &Backend{cfg: cfg, base: base, http: client}, nil
}

// Name is "ollama".
func (*Backend) Name() string { return "ollama" }

// What is sent and received.
type (
	chatRequest struct {
		Model     string          `json:"model"`
		Messages  []chatMessage   `json:"messages"`
		Tools     json.RawMessage `json:"tools,omitempty"`
		Format    json.RawMessage `json:"format,omitempty"`
		Stream    bool            `json:"stream"`
		Options   options         `json:"options"`
		KeepAlive string          `json:"keep_alive,omitempty"`
	}
	options struct {
		NumCtx      int     `json:"num_ctx"`
		Temperature float64 `json:"temperature"`
		NumPredict  int     `json:"num_predict"`
	}
	chatMessage struct {
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		Images    []string   `json:"images,omitempty"`
		ToolCalls []toolCall `json:"tool_calls,omitempty"`
		ToolName  string     `json:"tool_name,omitempty"`
	}
	toolCall struct {
		ID       string `json:"id,omitempty"`
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	chatResponse struct {
		Model           string      `json:"model"`
		Message         chatMessage `json:"message"`
		Done            bool        `json:"done"`
		DoneReason      string      `json:"done_reason"`
		PromptEvalCount int         `json:"prompt_eval_count"`
		EvalCount       int         `json:"eval_count"`
		Error           string      `json:"error"`
	}
)

// body makes the JSON of a chat request. Temperature is 0 and streaming is off: the answer is read whole.
func (b *Backend) body(req backend.Request) ([]byte, error) {
	cr := chatRequest{
		Model:     b.cfg.Model,
		Stream:    false,
		KeepAlive: b.cfg.KeepAlive,
		Options:   options{NumCtx: b.cfg.NumCtx, Temperature: 0, NumPredict: b.cfg.MaxTokens},
	}
	for _, m := range req.Messages {
		cr.Messages = append(cr.Messages, message(m))
	}
	switch req.Mode {
	case backend.ModeNative:
		tools, err := req.Set.Render(actions.FormatOllama)
		if err != nil {
			return nil, err
		}
		cr.Tools = tools
	case backend.ModeStructured:
		schema, err := req.Set.Envelope(req.ActionLimit(), true)
		if err != nil {
			return nil, err
		}
		cr.Format = schema
	case backend.ModeJSON:
		cr.Format = json.RawMessage(`"json"`)
	}
	return json.Marshal(cr)
}

// message maps a message of the conversation to Ollama's: the images as base64, the calls of an assistant message with
// their arguments as objects, the result of a call with the name of the tool.
func message(m backend.Message) chatMessage {
	cm := chatMessage{Role: string(m.Role), Content: m.Text}
	for _, img := range m.Images {
		cm.Images = append(cm.Images, base64.StdEncoding.EncodeToString(img.Data))
	}
	for _, c := range m.Calls {
		var tc toolCall
		tc.ID, tc.Function.Name, tc.Function.Arguments = c.ID, c.Name, parse.Object(c.Args)
		cm.ToolCalls = append(cm.ToolCalls, tc)
	}
	if m.Role == backend.RoleTool {
		cm.ToolName = m.Tool
	}
	return cm
}

// Complete asks the model for its next move.
func (b *Backend) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	if err := req.Validate(); err != nil {
		return backend.Response{}, fmt.Errorf("ollama: %w", err)
	}
	body, err := b.body(req)
	if err != nil {
		return backend.Response{}, fmt.Errorf("ollama: %w", err)
	}
	begin := time.Now()
	data, err := b.http.Post(ctx, b.endpoint("/api/chat"), nil, body)
	if err != nil {
		return backend.Response{}, b.explain(err)
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return backend.Response{}, &backend.Error{Kind: backend.KindProtocol, Backend: "ollama", Message: "the answer is not JSON"}
	}
	if cr.Error != "" {
		return backend.Response{}, &backend.Error{Kind: backend.KindBadRequest, Backend: "ollama", Message: textclean.Line(cr.Error, 300)}
	}
	if cr.Message.Role != "assistant" && cr.Message.Content == "" && len(cr.Message.ToolCalls) == 0 {
		return backend.Response{}, &backend.Error{Kind: backend.KindProtocol, Backend: "ollama", Message: "the answer has no assistant message"}
	}
	reply := parse.Reply{Text: cr.Message.Content}
	for i, c := range cr.Message.ToolCalls {
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1) // Ollama's calls have no id of their own
		}
		reply.ToolCalls = append(reply.ToolCalls, parse.ToolCall{ID: id, Name: c.Function.Name, Args: c.Function.Arguments})
	}
	return backend.Response{
		Reply:     reply,
		Usage:     backend.Usage{PromptTokens: cr.PromptEvalCount, CompletionTokens: cr.EvalCount},
		Model:     cr.Model,
		Truncated: cr.DoneReason == "length",
		Latency:   time.Since(begin),
	}, nil
}

func (b *Backend) endpoint(path string) string {
	u := *b.base
	u.Path += path
	return u.String()
}

// explain adds to the errors Ollama's users meet the thing to do about them.
func (b *Backend) explain(err error) error {
	var e *backend.Error
	if !errors.As(err, &e) {
		return err
	}
	switch e.Kind {
	case backend.KindNotFound:
		if strings.Contains(strings.ToLower(e.Message), "model") {
			out := *e
			out.Message = textclean.Line(e.Message+" (pull it with: ollama pull "+b.cfg.Model+")", 300)
			return &out
		}
	case backend.KindUnavailable:
		out := *e
		out.Message = "nothing answers at " + b.base.Host + ": is Ollama running?"
		return &out
	}
	return err
}
