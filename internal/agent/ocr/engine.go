// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
)

// Errors an Engine reports.
var (
	// ErrUnavailable means the OCR program is not installed.
	ErrUnavailable = errors.New("tesseract is not installed")
	// ErrLanguage means the language is not installed.
	ErrLanguage = errors.New("language is not installed")
	// ErrTimeout means recognition took longer than allowed.
	ErrTimeout = errors.New("recognition timed out")
)

// Engine reads the text in a picture (PNG data) and returns its words, in the pixels of that picture.
type Engine interface {
	Recognize(ctx context.Context, png []byte, lang string) ([]Word, error)
}

// DefaultTimeout is how long one recognition may take (PLAN.md section 8).
const DefaultTimeout = 10 * time.Second

// Limits on what tesseract may say.
const (
	maxStdout = 8 << 20
	maxStderr = 4 << 10
)

var reLang = regexp.MustCompile(`^[a-z]{3}(_[a-z]+)?(\+[a-z]{3}(_[a-z]+)?){0,3}$`)

// ValidLanguage reports whether s looks like tesseract's language names: eng, deu, chi_sim, eng+deu.
func ValidLanguage(s string) bool { return reLang.MatchString(s) }

// Tesseract runs the tesseract command line program.
type Tesseract struct {
	// Path is the program (tesseract, found in PATH, when empty).
	Path string
	// Timeout bounds one recognition (DefaultTimeout when zero).
	Timeout time.Duration
	// Env is added to the program's environment.
	Env []string
}

// Recognize runs `tesseract stdin stdout -l <lang> --psm 11 tsv` on the picture: page segmentation mode 11 is "sparse
// text", which finds text wherever it is on a screen rather than expecting a page.
func (t Tesseract) Recognize(ctx context.Context, png []byte, lang string) ([]Word, error) {
	if !ValidLanguage(lang) {
		return nil, fmt.Errorf("%w: %q", ErrLanguage, lang)
	}
	path := t.Path
	if path == "" {
		path = "tesseract"
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := ctx, context.CancelFunc(func() {})
	if _, has := ctx.Deadline(); !has {
		cctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(cctx, path, "stdin", "stdout", "-l", lang, "--psm", "11", "tsv")
	// One thread: tesseract's own threads fight over the few CPUs of the virtual machine and make it slower.
	cmd.Env = append(append(os.Environ(), "OMP_THREAD_LIMIT=1"), t.Env...)
	cmd.Stdin = bytes.NewReader(png)
	var stdout, stderr limited
	stdout.max, stderr.max = maxStdout, maxStderr
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Run(); err != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return nil, ctx.Err()
		case errors.Is(cctx.Err(), context.DeadlineExceeded):
			return nil, ErrTimeout
		case errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist):
			return nil, ErrUnavailable
		case stdout.over:
			return nil, errors.New("tesseract wrote too much")
		}
		msg := firstLine(stderr.String())
		if strings.Contains(stderr.String(), "Failed loading language") || strings.Contains(stderr.String(), "Error opening data file") {
			return nil, fmt.Errorf("%w: %q", ErrLanguage, lang)
		}
		return nil, fmt.Errorf("tesseract failed: %w: %s", err, msg)
	}
	if stdout.over {
		return nil, errors.New("tesseract wrote too much")
	}
	words, err := ParseTSV(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("reading tesseract's output: %w", err)
	}
	return words, nil
}

// limited collects at most max bytes and notes that more came. It does not embed a bytes.Buffer: io.Copy would find
// the buffer's ReadFrom and fill it without asking.
type limited struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); len(p) > room {
		l.over = true
		if room > 0 {
			l.buf.Write(p[:room])
		}
		return len(p), nil // swallow the rest: the program is not failed, the output is cut
	}
	return l.buf.Write(p)
}

func (l *limited) Bytes() []byte  { return l.buf.Bytes() }
func (l *limited) String() string { return l.buf.String() }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return textclean.Line(s, 200)
}
