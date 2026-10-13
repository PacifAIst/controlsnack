// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// Limits of a `file_peek` request: the host looks at the head of a script to analyze what it would run (PLAN.md
// section 6.3), so 64 KiB is plenty.
const (
	defaultPeekBytes = 64 << 10
	maxPeekBytes     = 64 << 10
	maxPathBytes     = 4096
)

// FilePeek serves `file_peek`: whether a file exists, how big it is, and its first bytes as the attachment.
type FilePeek struct{}

// Handle answers one request. Only regular files are read: a directory, a device or a pipe exists but has no head to
// show, and is never opened (opening a pipe could block the worker).
func (FilePeek) Handle(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.FilePeekReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	switch {
	case r.Path == "" || len(r.Path) > maxPathBytes || strings.ContainsRune(r.Path, 0):
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "path is not usable")
	case !filepath.IsAbs(r.Path):
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "path must be absolute")
	case r.MaxBytes < 0 || r.MaxBytes > maxPeekBytes:
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "max_bytes is 1 to %d (0 means %d)", maxPeekBytes, defaultPeekBytes)
	}
	n := r.MaxBytes
	if n == 0 {
		n = defaultPeekBytes
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	fi, err := os.Stat(r.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return protocol.FilePeekRes{}, nil, nil
	case err != nil:
		return nil, nil, server.Fail(protocol.CodeUnsupported, "cannot look at the file: %v", err)
	case !fi.Mode().IsRegular():
		return protocol.FilePeekRes{Exists: true}, nil, nil
	}
	f, err := os.Open(r.Path)
	if err != nil {
		return nil, nil, server.Fail(protocol.CodeUnsupported, "cannot read the file: %v", err)
	}
	defer f.Close()
	head := make([]byte, min(int64(n), fi.Size()))
	got, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, nil, server.Fail(protocol.CodeInternal, "reading the file: %v", err)
	}
	return protocol.FilePeekRes{Exists: true, Size: fi.Size()}, head[:got], nil
}
