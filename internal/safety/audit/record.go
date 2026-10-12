// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Version is the audit record format version.
const Version = 1

// Record kinds (PLAN.md section 5.5).
const (
	KindLifecycle = "lifecycle" // start, accel_selected, accel_fallback, end, kill
	KindConfig    = "config"    // effective configuration, secrets redacted
	KindUser      = "user"      // chat, confirmations, controls
	KindLLM       = "llm"       // latency, token counts, response hash, truncated rationale
	KindAction    = "action"    // one model action with its verdict and result
	KindNet       = "net"       // egress decisions and byte counts
	KindError     = "error"
)

// Lifecycle events.
const (
	EventStart         = "start"
	EventAccelSelected = "accel_selected"
	EventAccelFallback = "accel_fallback"
	EventEnd           = "end"
	EventKill          = "kill"
)

// Caps on what one record may carry (PLAN.md section 6.9).
const (
	MaxArgsBytes   = 8 << 10
	MaxReasonBytes = 1 << 10
)

// Record is one audit log line. Fields that do not apply to a kind are left out.
//
// The wire form is exactly the JSON this struct marshals to: Verify requires every line to equal the marshaled
// form of what it decodes to, so an edit that adds or reorders anything is caught even if it leaves the known
// fields alone.
type Record struct {
	V       int    `json:"v"`
	Seq     uint64 `json:"seq"`
	TS      string `json:"ts"`
	Session string `json:"session"`
	Kind    string `json:"kind"`
	// Event refines the kind (a lifecycle event, a user action, an egress decision...).
	Event    string          `json:"event,omitempty"`
	Actor    string          `json:"actor,omitempty"` // llm, user or system
	Turn     int             `json:"turn,omitempty"`
	ActionID string          `json:"action_id,omitempty"`
	Tool     string          `json:"tool,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	// Verdict is the final outcome of an action: allowed, denied, confirmed, rejected, rate_limited, ...
	Verdict          string          `json:"verdict,omitempty"`
	Rules            []string        `json:"rules,omitempty"`
	ScreenshotSHA256 string          `json:"screenshot_sha256,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	LLM              json.RawMessage `json:"llm,omitempty"`
	Data             json.RawMessage `json:"data,omitempty"`

	Prev string `json:"prev"`
	Hash string `json:"hash,omitempty"`
}

// firstPrev is the prev of a log's first record: it binds the chain to its session.
func firstPrev(session string) string {
	sum := sha256.Sum256([]byte("controlsnack-audit-v1|" + session))
	return hex.EncodeToString(sum[:])
}

// computeHash is the SHA-256 of the record's canonical JSON without the hash field.
func computeHash(r Record) (string, error) {
	r.Hash = ""
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ToRaw marshals any value into a json.RawMessage field, for building records.
func ToRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(map[string]string{"_unmarshalable": fmt.Sprintf("%T", v)})
	}
	return data
}

// decodeStrict parses one log line into a Record, rejecting unknown fields and trailing data.
func decodeStrict(line []byte) (Record, error) {
	var r Record
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Record{}, err
	}
	if dec.More() {
		return Record{}, fmt.Errorf("trailing data after the record")
	}
	return r, nil
}
