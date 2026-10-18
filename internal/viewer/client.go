// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits on what the page may send.
const (
	// MaxChatRunes is the longest chat message, in characters.
	MaxChatRunes = 8000
	// MaxNoteRunes is the longest note on a confirmation.
	MaxNoteRunes = 500
)

// The operations of a control message.
const (
	OpPause   = "pause"
	OpResume  = "resume"
	OpStop    = "stop"
	OpKill    = "kill"
	OpStepOn  = "step_on"
	OpStepOff = "step_off"
)

// What the person pressed to kill.
const (
	SrcButton = "button"
	SrcHotkey = "hotkey"
)

// ChatIn is something the person wrote to the model.
type ChatIn struct{ Text string }

// ConfirmIn is the person's answer to a confirmation.
type ConfirmIn struct {
	ID      string
	Approve bool
	Note    string
}

// ControlIn is one of the controls: pause, resume, stop, kill, step mode on or off.
type ControlIn struct {
	Op  string
	Src string // for kill: SrcButton or SrcHotkey
}

// PingIn asks for a pong with the same t.
type PingIn struct{ T float64 }

// ErrUnknownType is returned for a message whose type is not one a page sends. The host drops these.
var ErrUnknownType = errors.New("unknown message type")

// InvalidError is a message of a known type that does not meet the rules.
type InvalidError struct {
	Type   string
	Reason string
}

func (e *InvalidError) Error() string { return "invalid " + e.Type + " message: " + e.Reason }

func invalid(typ, format string, a ...any) error {
	return &InvalidError{Type: typ, Reason: fmt.Sprintf(format, a...)}
}

var reID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// ParseClient reads and checks one message of the page. It returns a ChatIn, ConfirmIn, ControlIn or PingIn, or an
// error: ErrUnknownType, or an *InvalidError for a known type that breaks the rules (a field that is missing, extra,
// of the wrong kind or out of range), or a plain error for something that is not a JSON object at all. Nothing in the
// message is trusted: sizes are checked, text may not hold control characters, and the object may have no other fields.
func ParseClient(data []byte) (any, error) {
	if len(data) > MaxClientBytes {
		return nil, errors.New("message too large")
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, errors.New("not a JSON object")
	}
	switch head.Type {
	case "chat":
		var m struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := strict(data, &m); err != nil {
			return nil, invalid("chat", "%v", err)
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			return nil, invalid("chat", "no text")
		}
		if err := checkText(text, MaxChatRunes); err != nil {
			return nil, invalid("chat", "%v", err)
		}
		return ChatIn{Text: text}, nil
	case "confirm":
		var m struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Approve *bool  `json:"approve"`
			Note    string `json:"note"`
		}
		if err := strict(data, &m); err != nil {
			return nil, invalid("confirm", "%v", err)
		}
		if !reID.MatchString(m.ID) {
			return nil, invalid("confirm", "the id is not one")
		}
		if m.Approve == nil {
			return nil, invalid("confirm", "approve is missing")
		}
		note := strings.TrimSpace(m.Note)
		if err := checkText(note, MaxNoteRunes); err != nil {
			return nil, invalid("confirm", "note: %v", err)
		}
		return ConfirmIn{ID: m.ID, Approve: *m.Approve, Note: note}, nil
	case "control":
		var m struct {
			Type string `json:"type"`
			Op   string `json:"op"`
			Src  string `json:"src"`
		}
		if err := strict(data, &m); err != nil {
			return nil, invalid("control", "%v", err)
		}
		switch m.Op {
		case OpPause, OpResume, OpStop, OpStepOn, OpStepOff:
			if m.Src != "" {
				return nil, invalid("control", "src belongs to kill")
			}
			return ControlIn{Op: m.Op}, nil
		case OpKill:
			switch m.Src {
			case "", SrcButton:
				return ControlIn{Op: OpKill, Src: SrcButton}, nil
			case SrcHotkey:
				return ControlIn{Op: OpKill, Src: SrcHotkey}, nil
			}
			return nil, invalid("control", "src is neither %s nor %s", SrcButton, SrcHotkey)
		}
		return nil, invalid("control", "op is not one")
	case "ping":
		var m struct {
			Type string   `json:"type"`
			T    *float64 `json:"t"`
		}
		if err := strict(data, &m); err != nil {
			return nil, invalid("ping", "%v", err)
		}
		if m.T == nil || math.IsNaN(*m.T) || math.IsInf(*m.T, 0) {
			return nil, invalid("ping", "t is not a number")
		}
		return PingIn{T: *m.T}, nil
	}
	return nil, ErrUnknownType
}

// strict decodes exactly one JSON object into v: no unknown fields, nothing after it.
func strict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("something follows the object")
	}
	return nil
}

// checkText accepts valid UTF-8 of at most max characters whose only control characters are line breaks and tabs.
func checkText(s string, max int) error {
	if !utf8.ValidString(s) {
		return errors.New("not valid UTF-8")
	}
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("more than %d characters", max)
	}
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7F || (r >= 0x80 && r < 0xA0) {
			return fmt.Errorf("a control character (U+%04X)", r)
		}
	}
	return nil
}
