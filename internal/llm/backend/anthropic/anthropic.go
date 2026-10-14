// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// Defaults of the client.
const (
	// DefaultBaseURL is Anthropic's API; the messages endpoint is {base}/v1/messages.
	DefaultBaseURL = "https://api.anthropic.com"
	// APIVersion is the version of the API the client speaks, sent as anthropic-version.
	APIVersion = "2023-06-01"
	// DefaultMaxTokens bounds the length of one answer (the API requires a limit).
	DefaultMaxTokens = 2048
	// noText stands in for the words of an assistant message that had none: the API refuses an empty text block.
	noText = "(no text)"
)

// Config configures the client.
type Config struct {
	// BaseURL is the API's address (DefaultBaseURL when empty); a proxy or compatible server may have a path prefix.
	BaseURL string
	// Model is the model to ask ("claude-sonnet-5-5").
	Model string
	// APIKey is sent as x-api-key.
	APIKey string
	// MaxTokens limits the answer (DefaultMaxTokens when 0).
	MaxTokens int
}

// Backend asks a server that speaks Anthropic's messages API.
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
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("anthropic: no model is named")
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if client == nil {
		client = httpx.New("anthropic")
	}
	if cfg.APIKey != "" {
		key := cfg.APIKey
		client.Redact = func(s string) string { return strings.ReplaceAll(s, key, "[key]") }
	}
	return &Backend{cfg: cfg, base: base, http: client}, nil
}

// Name is "anthropic".
func (*Backend) Name() string { return "anthropic" }

// What is sent and received.
type (
	messagesRequest struct {
		Model       string          `json:"model"`
		MaxTokens   int             `json:"max_tokens"`
		System      string          `json:"system,omitempty"`
		Messages    []message       `json:"messages"`
		Tools       json.RawMessage `json:"tools,omitempty"`
		Temperature float64         `json:"temperature"`
		Stream      bool            `json:"stream"`
	}
	message struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	// block is a piece of a message: text, a picture, a call of a tool, or the result of one.
	block struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		Source    *imageSource    `json:"source,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		ToolUseID string          `json:"tool_use_id,omitempty"`
		Content   string          `json:"content,omitempty"`
	}
	imageSource struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	messagesResponse struct {
		Model   string `json:"model"`
		Type    string `json:"type"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
)

// body makes the JSON of a messages request. The system message is a field of its own; results of tools and the
// observation that follows them are one user message, with the results first, as the API wants.
func (b *Backend) body(req backend.Request) ([]byte, error) {
	mr := messagesRequest{Model: b.cfg.Model, MaxTokens: b.cfg.MaxTokens, Temperature: 0, Stream: false}
	for _, m := range req.Messages {
		switch m.Role {
		case backend.RoleSystem:
			mr.System = m.Text
		case backend.RoleUser:
			mr.Messages = appendUser(mr.Messages, userBlocks(m)...)
		case backend.RoleTool:
			id := m.CallID
			if id == "" {
				id = m.Tool
			}
			mr.Messages = appendUser(mr.Messages, block{Type: "tool_result", ToolUseID: id, Content: orText(m.Text)})
		case backend.RoleAssistant:
			mr.Messages = append(mr.Messages, assistantMessage(m))
		}
	}
	// Anthropic has no response schema: structured mode asks in the prompt, as JSON mode does.
	if req.Mode == backend.ModeNative {
		tools, err := req.Set.Render(actions.FormatAnthropic)
		if err != nil {
			return nil, err
		}
		mr.Tools = tools
	}
	return json.Marshal(mr)
}

// appendUser adds blocks to the last message if it is a user message, else makes a new one: the API wants the roles to
// alternate, and the results of a batch of calls are the start of the next user message.
func appendUser(msgs []message, blocks ...block) []message {
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
		return msgs
	}
	return append(msgs, message{Role: "user", Content: blocks})
}

// userBlocks is a user message: the pictures, then the words (a picture before its question reads best).
func userBlocks(m backend.Message) []block {
	var out []block
	for _, img := range m.Images {
		out = append(out, block{Type: "image", Source: &imageSource{Type: "base64", MediaType: img.MIME, Data: base64.StdEncoding.EncodeToString(img.Data)}})
	}
	if m.Text != "" || len(out) == 0 {
		out = append(out, block{Type: "text", Text: orText(m.Text)})
	}
	return out
}

func assistantMessage(m backend.Message) message {
	var blocks []block
	if m.Text != "" {
		blocks = append(blocks, block{Type: "text", Text: m.Text})
	}
	for i, c := range m.Calls {
		blocks = append(blocks, block{Type: "tool_use", ID: callID(c.ID, i), Name: c.Name, Input: parse.Object(c.Args)})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, block{Type: "text", Text: noText})
	}
	return message{Role: "assistant", Content: blocks}
}

func orText(s string) string {
	if s == "" {
		return noText
	}
	return s
}

func callID(id string, i int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("call_%d", i+1)
}

// Complete asks the model for its next move.
func (b *Backend) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	if err := req.Validate(); err != nil {
		return backend.Response{}, fmt.Errorf("anthropic: %w", err)
	}
	if first := firstNonSystem(req.Messages); first != backend.RoleUser {
		return backend.Response{}, errors.New("anthropic: the conversation must start with a user message")
	}
	body, err := b.body(req)
	if err != nil {
		return backend.Response{}, fmt.Errorf("anthropic: %w", err)
	}
	headers := map[string]string{"anthropic-version": APIVersion}
	if b.cfg.APIKey != "" {
		headers["x-api-key"] = b.cfg.APIKey
	}
	begin := time.Now()
	data, err := b.http.Post(ctx, b.endpoint("/v1/messages"), headers, body)
	if err != nil {
		return backend.Response{}, b.explain(err)
	}
	var mr messagesResponse
	if err := json.Unmarshal(data, &mr); err != nil {
		return backend.Response{}, &backend.Error{Kind: backend.KindProtocol, Backend: "anthropic", Message: "the answer is not JSON"}
	}
	if mr.Type == "error" || (mr.Type != "message" && len(mr.Content) == 0) {
		return backend.Response{}, &backend.Error{Kind: backend.KindProtocol, Backend: "anthropic", Message: "the answer is not a message"}
	}
	var reply parse.Reply
	var texts []string
	for i, c := range mr.Content {
		switch c.Type {
		case "text":
			texts = append(texts, c.Text)
		case "tool_use":
			// The input is an object; the parser takes it as it is.
			reply.ToolCalls = append(reply.ToolCalls, parse.ToolCall{ID: callID(c.ID, i), Name: c.Name, Args: c.Input})
		}
	}
	reply.Text = strings.Join(texts, "\n")
	return backend.Response{
		Reply:     reply,
		Usage:     backend.Usage{PromptTokens: mr.Usage.InputTokens, CompletionTokens: mr.Usage.OutputTokens},
		Model:     mr.Model,
		Truncated: mr.StopReason == "max_tokens",
		Latency:   time.Since(begin),
	}, nil
}

func firstNonSystem(msgs []backend.Message) backend.Role {
	for _, m := range msgs {
		if m.Role != backend.RoleSystem {
			return m.Role
		}
	}
	return ""
}

func (b *Backend) endpoint(path string) string {
	u := *b.base
	u.Path += path
	return u.String()
}

// explain adds to the errors the users of this API meet the thing to do about them.
func (b *Backend) explain(err error) error {
	var e *backend.Error
	if !errors.As(err, &e) {
		return err
	}
	out := *e
	switch e.Kind {
	case backend.KindAuth:
		out.Message = textclean.Line(e.Message+" (is the key in the environment variable named by --api-key-env?)", 300)
	case backend.KindUnavailable:
		if e.Message == "nothing answers at that address" {
			out.Message = "nothing answers at " + b.base.Host + ": is the server running?"
		}
	default:
		return err
	}
	return &out
}
