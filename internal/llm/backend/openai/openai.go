// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package openai

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

// Where the servers that speak this API listen unless told otherwise (the base address includes /v1).
const (
	DefaultBaseURL  = "https://api.openai.com/v1"
	LMStudioBaseURL = "http://127.0.0.1:1234/v1"
	LlamaCppBaseURL = "http://127.0.0.1:8080/v1"
	// DefaultMaxTokens bounds the length of one answer.
	DefaultMaxTokens = 2048
)

// Config configures the client.
type Config struct {
	// BaseURL is the server's address, with /v1 (DefaultBaseURL when empty).
	BaseURL string
	// Model is the model to ask, as the server names it.
	Model string
	// APIKey is sent as a bearer token when it is not empty. A local server wants none.
	APIKey string
	// MaxTokens limits the answer (DefaultMaxTokens when 0).
	MaxTokens int
	// NoTemperature leaves temperature out of the request, for the models that refuse any setting of it (OpenAI's
	// reasoning models). Otherwise the temperature is 0.
	NoTemperature bool
}

// Backend asks a server that speaks the OpenAI chat completions API.
type Backend struct {
	cfg  Config
	base *url.URL
	http *httpx.Client
	// official is the OpenAI service itself: it names the limit on the answer max_completion_tokens, the others
	// max_tokens.
	official bool
}

var _ backend.Backend = (*Backend)(nil)

// New makes a client. client is the HTTP client to use (nil: one with the defaults of the plan).
func New(cfg Config, client *httpx.Client) (*Backend, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	base, err := httpx.ValidateBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("openai: no model is named")
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if client == nil {
		client = httpx.New("openai")
	}
	if cfg.APIKey != "" {
		key := cfg.APIKey
		client.Redact = func(s string) string { return strings.ReplaceAll(s, key, "[key]") }
	}
	return &Backend{cfg: cfg, base: base, http: client, official: strings.EqualFold(base.Hostname(), "api.openai.com")}, nil
}

// Name is "openai".
func (*Backend) Name() string { return "openai" }

// What is sent and received.
type (
	chatRequest struct {
		Model               string          `json:"model"`
		Messages            []chatMessage   `json:"messages"`
		Tools               json.RawMessage `json:"tools,omitempty"`
		ToolChoice          string          `json:"tool_choice,omitempty"`
		ResponseFormat      *responseFormat `json:"response_format,omitempty"`
		Temperature         *float64        `json:"temperature,omitempty"`
		MaxTokens           int             `json:"max_tokens,omitempty"`
		MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
		Stream              bool            `json:"stream"`
	}
	responseFormat struct {
		Type       string      `json:"type"`
		JSONSchema *jsonSchema `json:"json_schema,omitempty"`
	}
	jsonSchema struct {
		Name   string          `json:"name"`
		Strict bool            `json:"strict"`
		Schema json.RawMessage `json:"schema"`
	}
	chatMessage struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		ToolCalls  []toolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	contentPart struct {
		Type     string    `json:"type"`
		Text     string    `json:"text,omitempty"`
		ImageURL *imageURL `json:"image_url,omitempty"`
	}
	imageURL struct {
		URL string `json:"url"`
	}
	toolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	chatResponse struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   json.RawMessage `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
)

// body makes the JSON of a chat completions request: no streaming, temperature 0, a limit on the length of the answer.
func (b *Backend) body(req backend.Request) ([]byte, error) {
	cr := chatRequest{Model: b.cfg.Model, Stream: false}
	if !b.cfg.NoTemperature {
		zero := 0.0
		cr.Temperature = &zero
	}
	if b.official {
		cr.MaxCompletionTokens = b.cfg.MaxTokens
	} else {
		cr.MaxTokens = b.cfg.MaxTokens
	}
	for _, m := range req.Messages {
		cr.Messages = append(cr.Messages, message(m))
	}
	switch req.Mode {
	case backend.ModeNative:
		tools, err := req.Set.Render(actions.FormatOpenAI)
		if err != nil {
			return nil, err
		}
		cr.Tools, cr.ToolChoice = tools, "auto"
	case backend.ModeStructured:
		schema, err := req.Set.Envelope(req.ActionLimit(), true)
		if err != nil {
			return nil, err
		}
		cr.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: &jsonSchema{Name: "controlsnack_response", Schema: schema}}
	case backend.ModeJSON:
		// Prompt-only: the servers do not agree on json_object, so none is asked for.
	}
	return json.Marshal(cr)
}

// message maps a message of the conversation: a user message with pictures is a list of parts (the pictures as data
// URLs), an assistant message replays its calls with their arguments as JSON text, a tool result names its call.
func message(m backend.Message) chatMessage {
	cm := chatMessage{Role: string(m.Role)}
	switch {
	case m.Role == backend.RoleUser && len(m.Images) > 0:
		parts := []contentPart{{Type: "text", Text: m.Text}}
		for _, img := range m.Images {
			parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}})
		}
		cm.Content = parts
	case m.Role == backend.RoleAssistant && len(m.Calls) > 0 && m.Text == "":
		cm.Content = nil // an assistant message that only calls tools has no content
	default:
		cm.Content = m.Text
	}
	for i, c := range m.Calls {
		var tc toolCall
		tc.ID, tc.Type, tc.Function.Name = callID(c.ID, i), "function", c.Name
		tc.Function.Arguments = string(parse.Object(c.Args))
		cm.ToolCalls = append(cm.ToolCalls, tc)
	}
	if m.Role == backend.RoleTool {
		cm.ToolCallID = m.CallID
		if cm.ToolCallID == "" {
			cm.ToolCallID = m.Tool
		}
	}
	return cm
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
		return backend.Response{}, fmt.Errorf("openai: %w", err)
	}
	body, err := b.body(req)
	if err != nil {
		return backend.Response{}, fmt.Errorf("openai: %w", err)
	}
	var headers map[string]string
	if b.cfg.APIKey != "" {
		headers = map[string]string{"Authorization": "Bearer " + b.cfg.APIKey}
	}
	begin := time.Now()
	data, err := b.http.Post(ctx, b.endpoint("/chat/completions"), headers, body)
	if err != nil {
		return backend.Response{}, b.explain(err)
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return backend.Response{}, &backend.Error{Kind: backend.KindProtocol, Backend: "openai", Message: "the answer is not JSON"}
	}
	if msg := errorText(cr.Error); msg != "" {
		return backend.Response{}, &backend.Error{Kind: backend.KindBadRequest, Backend: "openai", Message: textclean.Line(msg, 300)}
	}
	if len(cr.Choices) == 0 {
		return backend.Response{}, &backend.Error{Kind: backend.KindProtocol, Backend: "openai", Message: "the answer has no choices"}
	}
	choice := cr.Choices[0]
	reply := parse.Reply{Text: contentText(choice.Message.Content)}
	for i, c := range choice.Message.ToolCalls {
		reply.ToolCalls = append(reply.ToolCalls, parse.ToolCall{ID: callID(c.ID, i), Name: c.Function.Name, Args: c.Function.Arguments})
	}
	return backend.Response{
		Reply:     reply,
		Usage:     backend.Usage{PromptTokens: cr.Usage.PromptTokens, CompletionTokens: cr.Usage.CompletionTokens},
		Model:     cr.Model,
		Truncated: choice.FinishReason == "length",
		Latency:   time.Since(begin),
	}, nil
}

// contentText reads the content of a message: a string, null, or (from a few servers) a list of parts with text.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var out []string
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// errorText reads an error object that came with a good status: {"message": "..."} or a plain string.
func errorText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	return "the server reported an error"
}

func (b *Backend) endpoint(path string) string {
	u := *b.base
	u.Path += path
	return u.String()
}

// explain adds to the errors the users of these servers meet the thing to do about them.
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
