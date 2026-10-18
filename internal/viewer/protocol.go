// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// ProtoVersion is the version of the WebSocket protocol, sent in the hello message.
const ProtoVersion = 1

// The size limits of the protocol.
const (
	// MaxMessageBytes is the largest text message the host sends. A longer one is not sent; the page is told instead.
	MaxMessageBytes = 256 << 10
	// MaxFrameBytes is the largest frame (a screenshot) the host sends; a larger one is dropped.
	MaxFrameBytes = 4 << 20
	// MaxClientBytes is the largest message the host accepts from the page.
	MaxClientBytes = 64 << 10
)

// Message is a text message from the host to the page. Each kind has a struct below with the fields of
// schema/viewer-ws.schema.json; Encode adds the "type" field.
type Message interface {
	// MessageType is the value of the "type" field.
	MessageType() string
}

// The states of a session as the page is told (PLAN.md section 3.4).
const (
	StateBooting         = "booting"
	StateHandshake       = "handshake"
	StateReady           = "ready"
	StateRunning         = "running"
	StateAwaitingConfirm = "awaiting_confirm"
	StateAwaitingUser    = "awaiting_user"
	StatePaused          = "paused"
	StateEnding          = "ending"
	StateEnded           = "ended"
	StateKilled          = "killed"
)

// Accel is how the VM was started.
type Accel struct {
	Requested      string `json:"requested"`
	Used           string `json:"used"`
	FallbackReason string `json:"fallback_reason,omitempty"`
}

// Limits are the bounds the page shows its counters against.
type Limits struct {
	MaxTurns          int `json:"max_turns"`
	MaxActions        int `json:"max_actions"`
	TimeoutSec        int `json:"timeout_s"`
	ConfirmTimeoutSec int `json:"confirm_timeout_s"`
	IdleTimeoutSec    int `json:"idle_timeout_s"`
}

// Resolution is the guest screen.
type Resolution struct {
	W int `json:"w"`
	H int `json:"h"`
}

// Bounds is a rectangle on the guest screen.
type Bounds struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Hello is the first message of every connection: what the session is. CSRF is filled in by the Hub.
type Hello struct {
	Proto      int        `json:"proto"`
	Session    string     `json:"session"`
	Backend    string     `json:"backend"`
	Model      string     `json:"model"`
	Mode       string     `json:"mode"` // "vision" or "text_only"
	Net        bool       `json:"net"`
	Unsafe     bool       `json:"unsafe"`
	RemoteLLM  bool       `json:"remote_llm"`
	Accel      Accel      `json:"accel"`
	Limits     Limits     `json:"limits"`
	Resolution Resolution `json:"resolution"`
	CSRF       string     `json:"csrf"`
	StartedAt  string     `json:"started_at"` // RFC 3339: when the session began, for the page's timer
}

// State is a change of the session's state.
type State struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// Cursor is where the mouse pointer is on the guest screen.
type Cursor struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Plan marks the action that is about to run.
type Plan struct {
	ActionID string  `json:"action_id"`
	Tool     string  `json:"tool"`
	X        *int    `json:"x,omitempty"`
	Y        *int    `json:"y,omitempty"`
	Bounds   *Bounds `json:"bounds,omitempty"`
}

// Trace is the record of one action, sent when it changes.
type Trace struct {
	ActionID   string          `json:"action_id"`
	Turn       int             `json:"turn"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Reason     string          `json:"reason"`
	Verdict    string          `json:"verdict"` // allow, confirm or deny
	Rules      []string        `json:"rules"`
	Status     string          `json:"status"`
	DurationMS int64           `json:"duration_ms"`
	Summary    string          `json:"summary"`
}

// Chat is a line of the conversation: what the person wrote, what the model said, or a note of the system.
type Chat struct {
	ID   string `json:"id"`
	Role string `json:"role"` // user, assistant or system
	Text string `json:"text"`
	TS   string `json:"ts"` // RFC 3339
}

// Rule is a rule an action matched, with the sentence that explains it to the person.
type Rule struct {
	ID          string `json:"id"`
	Explanation string `json:"explanation"`
}

// ConfirmRequest asks the person to approve an action.
type ConfirmRequest struct {
	ID        string          `json:"id"`
	ActionID  string          `json:"action_id"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args"`
	Rules     []Rule          `json:"rules"`
	Reason    string          `json:"reason"`
	ExpiresAt string          `json:"expires_at"` // RFC 3339
}

// The outcomes of a confirmation.
const (
	OutcomeApproved = "approved"
	OutcomeRejected = "rejected"
	OutcomeExpired  = "expired"
	OutcomeCanceled = "canceled"
)

// ConfirmResolved says how a confirmation ended, so that every page that showed it can close it.
type ConfirmResolved struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
}

// Audit is a line of the audit log, for the page's tail of it.
type Audit struct {
	Line string `json:"line"`
}

// Stats are the figures of the header, every two seconds.
type Stats struct {
	KBps       float64 `json:"kbps"`
	FPS        float64 `json:"fps"`
	FrameBytes int     `json:"frame_bytes"`
	LLMMS      int64   `json:"llm_ms"`
	Actions    int     `json:"actions"`
}

// The levels of a notice.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Notice is something the person should read: a fallback, a refusal, a warning.
type Notice struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// Ended says the session is over.
type Ended struct {
	Reason  string `json:"reason"`
	Summary string `json:"summary"`
}

// Pong answers a ping with the same t.
type Pong struct {
	T float64 `json:"t"`
}

// MessageType implements Message.
func (Hello) MessageType() string { return "hello" }

// MessageType implements Message.
func (State) MessageType() string { return "state" }

// MessageType implements Message.
func (Cursor) MessageType() string { return "cursor" }

// MessageType implements Message.
func (Plan) MessageType() string { return "plan" }

// MessageType implements Message.
func (Trace) MessageType() string { return "trace" }

// MessageType implements Message.
func (Chat) MessageType() string { return "chat" }

// MessageType implements Message.
func (ConfirmRequest) MessageType() string { return "confirm_request" }

// MessageType implements Message.
func (ConfirmResolved) MessageType() string { return "confirm_resolved" }

// MessageType implements Message.
func (Audit) MessageType() string { return "audit" }

// MessageType implements Message.
func (Stats) MessageType() string { return "stats" }

// MessageType implements Message.
func (Notice) MessageType() string { return "notice" }

// MessageType implements Message.
func (Ended) MessageType() string { return "ended" }

// MessageType implements Message.
func (Pong) MessageType() string { return "pong" }

// ErrTooLarge is returned by Encode for a message over MaxMessageBytes.
var ErrTooLarge = errors.New("viewer: the message is too large to send")

// Encode makes the JSON text of a message: its fields and, first, its "type".
func Encode(m Message) ([]byte, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(body) < 2 || body[0] != '{' {
		return nil, fmt.Errorf("viewer: %T is not encoded as an object", m)
	}
	head := `{"type":"` + m.MessageType() + `"`
	var out []byte
	if len(body) == 2 { // "{}"
		out = []byte(head + "}")
	} else {
		out = append([]byte(head+","), body[1:]...)
	}
	if len(out) > MaxMessageBytes {
		return nil, ErrTooLarge
	}
	return out, nil
}

// Frames are binary messages: a screenshot of the guest.
const (
	frameKind   = 0x01
	frameHeader = 1 + 4 + 2 + 2
)

// EncodeFrame makes a frame message: 0x01, the sequence number, the size of the picture and the WebP bytes.
func EncodeFrame(seq uint32, w, h int, webp []byte) ([]byte, error) {
	if w < 1 || w > 0xFFFF || h < 1 || h > 0xFFFF {
		return nil, fmt.Errorf("viewer: a frame of %dx%d cannot be sent", w, h)
	}
	if len(webp) == 0 || len(webp)+frameHeader > MaxFrameBytes {
		return nil, fmt.Errorf("viewer: a frame of %d bytes cannot be sent", len(webp))
	}
	out := make([]byte, frameHeader+len(webp))
	out[0] = frameKind
	binary.BigEndian.PutUint32(out[1:], seq)
	binary.BigEndian.PutUint16(out[5:], uint16(w)) //nolint:gosec // checked above
	binary.BigEndian.PutUint16(out[7:], uint16(h)) //nolint:gosec // checked above
	copy(out[frameHeader:], webp)
	return out, nil
}

// DecodeFrame reads a frame message.
func DecodeFrame(b []byte) (seq uint32, w, h int, webp []byte, err error) {
	if len(b) <= frameHeader || b[0] != frameKind {
		return 0, 0, 0, nil, errors.New("viewer: not a frame")
	}
	return binary.BigEndian.Uint32(b[1:]), int(binary.BigEndian.Uint16(b[5:])), int(binary.BigEndian.Uint16(b[7:])), b[frameHeader:], nil
}

// MarshalJSON writes no rules and no arguments as an empty list and an empty object, which is what the page iterates.
func (t Trace) MarshalJSON() ([]byte, error) {
	type plain Trace
	if t.Rules == nil {
		t.Rules = []string{}
	}
	if len(t.Args) == 0 {
		t.Args = json.RawMessage("{}")
	}
	return json.Marshal(plain(t))
}

// MarshalJSON writes no rules and no arguments as an empty list and an empty object.
func (c ConfirmRequest) MarshalJSON() ([]byte, error) {
	type plain ConfirmRequest
	if c.Rules == nil {
		c.Rules = []Rule{}
	}
	if len(c.Args) == 0 {
		c.Args = json.RawMessage("{}")
	}
	return json.Marshal(plain(c))
}

// Agent says whether the guest agent is answering. When it is not, the session works through the fallback (the screen
// through QEMU, keys through QMP) and the page says so over the picture.
type Agent struct {
	Online bool   `json:"online"`
	Detail string `json:"detail"`
}

// MessageType implements Message.
func (Agent) MessageType() string { return "agent" }
