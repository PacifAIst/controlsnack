// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// Keeper saves what --keep asks to keep, and nothing else: the conversation as the person saw it (what was said, each action and
// how it went, every notice) in transcript.jsonl, and the pictures the model was shown in shots/. The rest of a session's files
// (the disk, the sockets, the browser profile) are always deleted at its end. The audit log is kept apart and always, and has
// hashes of the pictures, not the pictures (D-18).
type Keeper struct {
	dir string
	now func() time.Time

	mu         sync.Mutex
	transcript *os.File
	shots      int
	err        error
}

// transcriptName and shotsName are what a kept directory holds.
const (
	transcriptName = "transcript.jsonl"
	shotsName      = "shots"
	requestsName   = "requests"
)

// NewKeeper makes the directory (owner only) and the transcript in it.
func NewKeeper(dir string) (*Keeper, error) {
	if err := platform.EnsureDir(filepath.Join(dir, shotsName)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, transcriptName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the directory just made
	if err != nil {
		return nil, err
	}
	return &Keeper{dir: dir, now: time.Now, transcript: f}, nil
}

// Dir is the directory the kept files are in.
func (k *Keeper) Dir() string { return k.dir }

// fail remembers the first thing that went wrong; keeping is never a reason to stop a session.
func (k *Keeper) fail(err error) {
	if k.err == nil {
		k.err = err
	}
}

// Send implements Sink: the conversation, the actions and the notices go to the transcript.
func (k *Keeper) Send(m viewer.Message) {
	var line map[string]any
	switch v := m.(type) {
	case viewer.Chat:
		line = map[string]any{"kind": "chat", "role": v.Role, "text": v.Text}
	case viewer.Notice:
		line = map[string]any{"kind": "notice", "level": v.Level, "text": v.Text}
	case viewer.Trace:
		if v.Status == "running" {
			return // the line that says how it went is the one worth keeping
		}
		line = map[string]any{"kind": "action", "turn": v.Turn, "tool": v.Tool, "id": v.ActionID, "status": v.Status, "summary": v.Summary}
	default:
		return
	}
	line["time"] = k.now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(line)
	k.mu.Lock()
	defer k.mu.Unlock()
	if err != nil {
		k.fail(err)
		return
	}
	if k.transcript == nil {
		return
	}
	if _, err := k.transcript.Write(append(data, '\n')); err != nil {
		k.fail(err)
	}
}

// Shot saves the picture the model was shown on a turn.
func (k *Keeper) Shot(turn int, img backend.Image) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.shots++
	name := fmt.Sprintf("turn-%04d%s", turn, extensionOf(img.MIME))
	path := filepath.Join(k.dir, shotsName, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // inside the directory this keeper made
	if err != nil {
		k.fail(err)
		return
	}
	if _, err := f.Write(img.Data); err != nil {
		k.fail(err)
	}
	if err := f.Close(); err != nil {
		k.fail(err)
	}
}

// Request saves what the model was asked on a turn, as text: each message after the system prompt (whose hash the log has) with
// the number of pictures it carried. It is what to read to see why a model did what it did.
func (k *Keeper) Request(turn int, req backend.Request) {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == backend.RoleSystem {
			continue
		}
		fmt.Fprintf(&b, "## %s", m.Role)
		if m.Tool != "" {
			fmt.Fprintf(&b, " (%s)", m.Tool)
		}
		if len(m.Images) > 0 {
			fmt.Fprintf(&b, " [%d picture(s)]", len(m.Images))
		}
		b.WriteString("\n")
		for _, c := range m.Calls {
			fmt.Fprintf(&b, "call %s %s\n", c.Name, c.Args)
		}
		b.WriteString(m.Text)
		b.WriteString("\n\n")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	dir := filepath.Join(k.dir, requestsName)
	if err := platform.EnsureDir(dir); err != nil {
		k.fail(err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("turn-%04d.txt", turn)), []byte(b.String()), 0o600); err != nil {
		k.fail(err)
	}
}

// Shots is how many pictures were saved.
func (k *Keeper) Shots() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.shots
}

// Close ends the transcript and says whether everything that was meant to be kept was.
func (k *Keeper) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.transcript != nil {
		if err := k.transcript.Close(); err != nil {
			k.fail(err)
		}
		k.transcript = nil
	}
	return k.err
}

// extensionOf is the file extension of a picture's type.
func extensionOf(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	}
	return ".img"
}
