// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package fakellm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Call is a tool call, in a request (what the conversation holds) or in a reply (what the fake model makes).
type Call struct {
	// ID is the id of the call; the server makes one when a reply leaves it empty.
	ID   string
	Name string
	// Args is the arguments as JSON text, which is how the API carries them.
	Args string
}

// Message is a message of the conversation, as the server decoded it.
type Message struct {
	Role string
	// Text is the message's words (the text parts, joined by line breaks).
	Text string
	// Images are the kinds of the pictures it carries: png, webp, jpeg or unknown.
	Images []string
	// Calls are the tool calls of an assistant message.
	Calls []Call
	// CallID is the call a tool message answers.
	CallID string
}

// Request is what the fake model was asked.
type Request struct {
	Model    string
	Messages []Message
	// Tools are the names of the tools offered.
	Tools []string
	// Format is "", "json_object" or "json_schema" (the response_format the client asked for).
	Format string
	// MaxTokens is max_tokens or max_completion_tokens, 0 when neither was sent; Temperature is nil when not sent.
	MaxTokens   int
	Temperature *float64
	// Stream is whether the client asked for a streamed answer, which this server does not give.
	Stream bool
	// Auth is the Authorization header.
	Auth string
	// Raw is the body of the request.
	Raw []byte
}

// Last returns the last message of a role, or the zero Message.
func (r *Request) Last(role string) Message {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == role {
			return r.Messages[i]
		}
	}
	return Message{}
}

// System is the words of the system message.
func (r *Request) System() string { return r.Last("system").Text }

// Observation is the words of the last user message: the observation of the turn, as the model reads it.
func (r *Request) Observation() string { return r.Last("user").Text }

// Pictures counts the pictures in the whole conversation.
func (r *Request) Pictures() int {
	n := 0
	for _, m := range r.Messages {
		n += len(m.Images)
	}
	return n
}

// HasTool reports whether the tool is offered.
func (r *Request) HasTool(name string) bool {
	for _, t := range r.Tools {
		if t == name {
			return true
		}
	}
	return false
}

// wire is the request as the OpenAI-compatible API carries it.
type wire struct {
	Model    string `json:"model"`
	Messages []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
	MaxTokens           int      `json:"max_tokens"`
	MaxCompletionTokens int      `json:"max_completion_tokens"`
	Temperature         *float64 `json:"temperature"`
	Stream              bool     `json:"stream"`
}

func decode(body []byte, auth string) (*Request, error) {
	var w wire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	r := &Request{Model: w.Model, Format: w.ResponseFormat.Type, Temperature: w.Temperature, Stream: w.Stream, Auth: auth, Raw: body}
	r.MaxTokens = max(w.MaxTokens, w.MaxCompletionTokens)
	for _, t := range w.Tools {
		r.Tools = append(r.Tools, t.Function.Name)
	}
	for _, m := range w.Messages {
		msg := Message{Role: m.Role, CallID: m.ToolCallID}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			msg.Text = text
		} else {
			var parts []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if json.Unmarshal(m.Content, &parts) == nil {
				var texts []string
				for _, p := range parts {
					switch {
					case p.ImageURL.URL != "":
						msg.Images = append(msg.Images, imageKind(p.ImageURL.URL))
					case p.Text != "":
						texts = append(texts, p.Text)
					}
				}
				msg.Text = strings.Join(texts, "\n")
			}
		}
		for _, c := range m.ToolCalls {
			args := string(c.Function.Arguments)
			var s string
			if json.Unmarshal(c.Function.Arguments, &s) == nil {
				args = s
			}
			msg.Calls = append(msg.Calls, Call{ID: c.ID, Name: c.Function.Name, Args: args})
		}
		r.Messages = append(r.Messages, msg)
	}
	return r, nil
}

// imageKind says what a data: URL holds by the first bytes of the picture.
func imageKind(url string) string {
	_, b64, ok := strings.Cut(url, ",")
	if !ok {
		return "unknown"
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	switch {
	case err != nil:
		return "unknown"
	case bytes.HasPrefix(data, []byte("\x89PNG")):
		return "png"
	case len(data) > 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8}):
		return "jpeg"
	}
	return "unknown"
}
