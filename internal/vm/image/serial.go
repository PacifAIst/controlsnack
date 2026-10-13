// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Marker starts the progress lines the guest's provisioning script prints on its serial console.
const Marker = "CS-PROVISION: "

// maxLine bounds one console line; the console is untrusted text from a guest that is being provisioned.
const maxLine = 8 << 10

// Progress is one parsed provisioning line.
type Progress struct {
	Text   string // what the script said after the marker
	Done   bool   // "done": the image is complete
	Failed bool   // "FAILED ...": a step failed and the VM is about to power off
}

var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// ParseProgress finds a progress line in a console line. Console lines come with carriage returns, escape sequences
// and, from cloud-init, a prefix; the marker may be anywhere in the line.
func ParseProgress(line string) (Progress, bool) {
	line = reANSI.ReplaceAllString(line, "")
	i := strings.Index(line, Marker)
	if i < 0 {
		return Progress{}, false
	}
	text := strings.TrimSpace(line[i+len(Marker):])
	if text == "" {
		return Progress{}, false
	}
	p := Progress{Text: text}
	switch {
	case text == "done":
		p.Done = true
	case strings.HasPrefix(text, "FAILED"):
		p.Failed = true
	}
	return p, true
}

// LineTail keeps the last lines of a console, for error messages.
type LineTail struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// NewLineTail keeps up to max lines.
func NewLineTail(max int) *LineTail { return &LineTail{max: max} }

// Add records a line.
func (t *LineTail) Add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == t.max {
		copy(t.lines, t.lines[1:])
		t.lines = t.lines[:t.max-1]
	}
	t.lines = append(t.lines, line)
}

// Lines returns the kept lines, oldest first.
func (t *LineTail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}

// Follow reads path as it grows and calls onLine for every complete line, in order, until ctx ends. A file that does
// not exist yet is waited for. Lines longer than maxLine are cut. Bytes the file gains while onLine runs are not
// lost.
func Follow(ctx context.Context, path string, poll time.Duration, onLine func(string)) error {
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	var offset int64
	var partial []byte
	buf := make([]byte, 64<<10)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		f, err := os.Open(path) //nolint:gosec // a build directory file
		if err == nil {
			if _, serr := f.Seek(offset, io.SeekStart); serr == nil {
				for {
					n, rerr := f.Read(buf)
					if n > 0 {
						offset += int64(n)
						partial = append(partial, buf[:n]...)
						for {
							i := indexNewline(partial)
							if i < 0 {
								break
							}
							onLine(cut(partial[:i]))
							partial = partial[i+1:]
						}
						if len(partial) > 2*maxLine { // a "line" that never ends: do not hold it all
							onLine(cut(partial[:maxLine]))
							partial = nil
						}
					}
					if rerr != nil {
						break
					}
				}
			}
			_ = f.Close()
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			// One last look, so lines written just before the end are not lost.
			if f, err := os.Open(path); err == nil { //nolint:gosec // as above
				defer f.Close()
				if _, serr := f.Seek(offset, io.SeekStart); serr == nil {
					rest, _ := io.ReadAll(io.LimitReader(f, 1<<20))
					partial = append(partial, rest...)
					for {
						i := indexNewline(partial)
						if i < 0 {
							break
						}
						onLine(cut(partial[:i]))
						partial = partial[i+1:]
					}
				}
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func indexNewline(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

func cut(b []byte) string {
	if len(b) > maxLine {
		b = b[:maxLine]
	}
	return strings.ToValidUTF8(strings.TrimRight(string(b), "\r"), "?")
}
