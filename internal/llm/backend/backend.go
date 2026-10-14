// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package backend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// Errors of a call to a backend, as the HTTP client reports them.
type (
	// Error is a failed call; its Kind says what to do about it.
	Error = httpx.Error
	// Kind is the sort of failure.
	Kind = httpx.Kind
)

// The kinds of failure.
const (
	KindUnavailable = httpx.KindUnavailable
	KindTimeout     = httpx.KindTimeout
	KindRateLimited = httpx.KindRateLimited
	KindServer      = httpx.KindServer
	KindAuth        = httpx.KindAuth
	KindBadRequest  = httpx.KindBadRequest
	KindNotFound    = httpx.KindNotFound
	KindProtocol    = httpx.KindProtocol
)

// Role of a message in the conversation.
type Role string

// The roles. A tool message carries the result of a native tool call back to the model.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Image types the backends take.
const (
	MIMEJPEG = "image/jpeg"
	MIMEPNG  = "image/png"
	MIMEWebP = "image/webp"
)

// Image is a picture attached to a user message.
type Image struct {
	MIME string
	Data []byte
}

// Message is one turn of the conversation.
type Message struct {
	Role Role
	Text string
	// Images are pictures of the screen; only a user message has them.
	Images []Image
	// Calls are the native tool calls an assistant message made, so that the history can be replayed to the backend with
	// the results that follow it.
	Calls []parse.ToolCall
	// CallID and Tool say which call a tool message answers.
	CallID string
	Tool   string
}

// Mode is how the model is asked to name its actions.
type Mode string

const (
	// ModeNative offers the tools of the set as functions and reads the tool calls of the answer.
	ModeNative Mode = "native"
	// ModeStructured asks for a JSON answer that follows the strict response schema of the set, for a backend that builds
	// a grammar from a schema.
	ModeStructured Mode = "structured"
	// ModeJSON asks for a JSON answer in the prompt (and, where there is a way to, for no other kind of text).
	ModeJSON Mode = "json"
)

// Request is what a model is asked: the conversation so far, the tools it may use, and how it names its actions.
type Request struct {
	Messages []Message
	// Set is the tools of the session (it makes the tool definitions and the response schema).
	Set  *actions.Set
	Mode Mode
	// MaxActions is the limit on actions in one answer (the response schema holds it); 0 means the default.
	MaxActions int
}

// Usage is the tokens a call used, as the backend counts them (0 when it does not say).
type Usage struct{ PromptTokens, CompletionTokens int }

// Response is what a model answered.
type Response struct {
	// Reply is the model's words and native tool calls, for parse.Parse.
	Reply parse.Reply
	Usage Usage
	// Model is the model that answered, as the backend names it.
	Model string
	// Truncated is set when the model stopped because its answer reached the limit on length: the answer may be cut off.
	Truncated bool
	// Latency is how long the call took, retries included.
	Latency time.Duration
}

// Backend is a model the session can ask.
type Backend interface {
	// Name is the kind of backend: "ollama", "openai" or "anthropic".
	Name() string
	// Complete asks the model for its next move. A context that ends stops the call and its error is the context's;
	// any other failure is an *Error.
	Complete(ctx context.Context, req Request) (Response, error)
}

// Limits on a conversation, so that a bug upstream cannot send an endless request.
const (
	maxMessages = 400
	maxImages   = 4
)

// Validate checks that a request is one a backend can make sense of: messages in an order the APIs accept, images only
// where a picture can be, tool results that say which call they answer, and a mode that has what it needs.
func (r Request) Validate() error {
	if len(r.Messages) == 0 {
		return errors.New("the request has no messages")
	}
	if len(r.Messages) > maxMessages {
		return fmt.Errorf("the request has %d messages, more than the %d allowed", len(r.Messages), maxMessages)
	}
	switch r.Mode {
	case ModeNative, ModeStructured:
		if r.Set == nil || len(r.Set.Tools()) == 0 {
			return fmt.Errorf("mode %s needs the tools of the session", r.Mode)
		}
	case ModeJSON:
	default:
		return fmt.Errorf("mode %q is not native, structured or json", r.Mode)
	}
	images, owed := 0, 0 // owed: the calls of the last assistant message that no tool message has answered yet
	for i, m := range r.Messages {
		switch m.Role {
		case RoleSystem:
			if i != 0 {
				return fmt.Errorf("message %d: a system message comes first, and only once", i+1)
			}
		case RoleUser:
			if owed > 0 {
				return fmt.Errorf("message %d: %d of the tool calls before it have no tool message (every call is answered, the APIs refuse a conversation where one is not)", i+1, owed)
			}
		case RoleAssistant:
			if owed > 0 {
				return fmt.Errorf("message %d: %d of the tool calls before it have no tool message (every call is answered, the APIs refuse a conversation where one is not)", i+1, owed)
			}
			if len(m.Calls) > 0 && r.Mode != ModeNative {
				return fmt.Errorf("message %d: tool calls are only part of a conversation in native mode", i+1)
			}
			owed = len(m.Calls)
		case RoleTool:
			if m.CallID == "" && m.Tool == "" {
				return fmt.Errorf("message %d: a tool message says which call it answers", i+1)
			}
			if i == 0 || (r.Messages[i-1].Role != RoleAssistant && r.Messages[i-1].Role != RoleTool) {
				return fmt.Errorf("message %d: a tool message follows the assistant message that made the call, or another tool message", i+1)
			}
			if owed--; owed < 0 {
				return fmt.Errorf("message %d: there are more tool messages than the assistant message before them made calls", i+1)
			}
		default:
			return fmt.Errorf("message %d: the role %q is not system, user, assistant or tool", i+1, m.Role)
		}
		if len(m.Images) > 0 && m.Role != RoleUser {
			return fmt.Errorf("message %d: only a user message carries images", i+1)
		}
		for _, img := range m.Images {
			if img.MIME != MIMEJPEG && img.MIME != MIMEPNG && img.MIME != MIMEWebP {
				return fmt.Errorf("message %d: an image of type %q is not JPEG, PNG or WebP", i+1, img.MIME)
			}
			if len(img.Data) == 0 {
				return fmt.Errorf("message %d: an image has no data", i+1)
			}
			images++
		}
	}
	if images > maxImages {
		return fmt.Errorf("the request has %d images, more than the %d allowed (only the latest screenshot stays in context)", images, maxImages)
	}
	if owed > 0 {
		return fmt.Errorf("%d of the last tool calls have no tool message", owed)
	}
	if r.Messages[len(r.Messages)-1].Role == RoleAssistant {
		return errors.New("the conversation ends with the assistant's message: there is nothing to answer")
	}
	return nil
}

// ActionLimit is the limit on actions in one answer: the request's, or the default.
func (r Request) ActionLimit() int {
	if r.MaxActions < 1 {
		return actions.DefaultMaxActions
	}
	return r.MaxActions
}
