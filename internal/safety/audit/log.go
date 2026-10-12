// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// timestampLayout is the record timestamp format: UTC with microseconds.
const timestampLayout = "2006-01-02T15:04:05.000000Z"

// ErrClosed is returned when appending to a closed log.
var ErrClosed = errors.New("audit log is closed")

var reSession = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)

// Options configure a Log.
type Options struct {
	// Now is the clock (default time.Now); injectable for tests.
	Now func() time.Time
	// Redact is applied to every string that is written (reasons, and the strings inside args, result, llm and
	// data). The session passes the secret redactor here; the default leaves text unchanged, so a caller that
	// forgets still gets the length caps but not secret masking.
	Redact func(string) string
}

// Log is the append-only, hash-chained audit log of one session (PLAN.md section 6.9).
//
// It is always on: a write error is sticky and every later Append fails with it, so a session whose audit trail
// can no longer be written must stop (the orchestrator treats an Append error as fatal) rather than carry on
// unrecorded. Appends are serialized and each record is flushed to stable storage before Append returns.
type Log struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	session string
	seq     uint64
	prev    string
	err     error
	closed  bool
	opts    Options
}

// Open creates the log file <dir>/<YYYYMMDD>-<session>.jsonl, mode 0600, failing if it already exists: a
// session has exactly one log and its chain must start fresh.
func Open(dir, session string, o Options) (*Log, error) {
	if !reSession.MatchString(session) {
		return nil, fmt.Errorf("invalid session id %q", session)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Redact == nil {
		o.Redact = func(s string) string { return s }
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating audit directory: %w", err)
	}
	name := o.Now().UTC().Format("20060102") + "-" + session + ".jsonl"
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600) //nolint:gosec // path is built from a validated session id under the audit directory
	if err != nil {
		return nil, fmt.Errorf("creating audit log: %w", err)
	}
	return &Log{f: f, path: path, session: session, prev: firstPrev(session), opts: o}, nil
}

// Path is the log file.
func (l *Log) Path() string { return l.path }

// Session is the session id the log belongs to.
func (l *Log) Session() string { return l.session }

// Append completes r (version, sequence number, timestamp, session, chain hashes), redacts and caps it, writes it
// and flushes it. It returns the record as written.
func (l *Log) Append(r Record) (Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Record{}, ErrClosed
	}
	if l.err != nil {
		return Record{}, l.err
	}

	r.V = Version
	r.Seq = l.seq + 1
	r.TS = l.opts.Now().UTC().Format(timestampLayout)
	r.Session = l.session
	r.Prev = l.prev
	r.Hash = ""
	l.sanitize(&r)

	hash, err := computeHash(r)
	if err != nil {
		return Record{}, fmt.Errorf("encoding audit record: %w", err)
	}
	r.Hash = hash
	line, err := json.Marshal(r)
	if err != nil {
		return Record{}, fmt.Errorf("encoding audit record: %w", err)
	}
	line = append(line, '\n')

	if _, err := l.f.Write(line); err != nil {
		l.err = fmt.Errorf("writing audit log: %w", err)
		return Record{}, l.err
	}
	if err := l.f.Sync(); err != nil {
		l.err = fmt.Errorf("flushing audit log: %w", err)
		return Record{}, l.err
	}
	l.seq, l.prev = r.Seq, r.Hash
	return r, nil
}

// Close closes the file. Further appends fail with ErrClosed.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.f.Close()
}

// Err returns the sticky write error, if any.
func (l *Log) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// sanitize redacts secrets and enforces the size caps.
func (l *Log) sanitize(r *Record) {
	r.Reason = capText(l.opts.Redact(r.Reason), MaxReasonBytes)
	r.Args = l.redactJSON(r.Args, MaxArgsBytes)
	r.Result = l.redactJSON(r.Result, MaxArgsBytes)
	r.LLM = l.redactJSON(r.LLM, MaxArgsBytes)
	r.Data = l.redactJSON(r.Data, MaxArgsBytes)
}

// redactJSON applies the redactor to every string inside a JSON value and replaces a value above the cap with a
// small stand-in that still records its size and hash.
func (l *Log) redactJSON(raw json.RawMessage, limit int) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		sum := sha256.Sum256(raw)
		out, _ := json.Marshal(map[string]any{"_invalid_json": true, "bytes": len(raw), "sha256": hex.EncodeToString(sum[:])})
		return out
	}
	out, err := json.Marshal(l.redactValue(v))
	if err != nil {
		out = []byte(`{"_unencodable":true}`)
	}
	if len(out) > limit {
		sum := sha256.Sum256(out)
		out, _ = json.Marshal(map[string]any{"_truncated": true, "bytes": len(out), "sha256": hex.EncodeToString(sum[:])})
	}
	return out
}

func (l *Log) redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return l.opts.Redact(t)
	case []any:
		for i := range t {
			t[i] = l.redactValue(t[i])
		}
		return t
	case map[string]any:
		for k, e := range t {
			t[k] = l.redactValue(e)
		}
		return t
	default:
		return v
	}
}

// capText limits s to max bytes, cutting at a character boundary and marking the cut.
func capText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const marker = "…[truncated]"
	cut := max - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.ToValidUTF8(s[:cut], "") + marker
}
