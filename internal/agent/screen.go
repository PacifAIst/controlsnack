// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"fmt"
	"image"
	"regexp"

	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// Screen is what the screenshot handler needs from the display.
type Screen interface {
	// Capture reads the whole screen. The image may be reused by the next call, so the handler must be done with it by
	// then (the capture pool serves one request at a time).
	Capture(ctx context.Context) (*image.RGBA, error)
	// Pointer is the mouse position in screen pixels.
	Pointer() (x, y int, err error)
}

// Limits of the screenshot parameters.
const (
	minLongEdge = 32
	maxLongEdge = 7680
)

var reHash = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Screenshots serves `screenshot` requests (PLAN.md section 5.1).
type Screenshots struct {
	Screen Screen
	Hasher imaging.Hasher
}

// NewScreenshots returns the handler for a display.
func NewScreenshots(s Screen) *Screenshots {
	return &Screenshots{Screen: s, Hasher: imaging.NewHasher()}
}

// Handle answers one screenshot request: the picture as the response's attachment and a description of it, or just
// the hash and "unchanged" when the screen is as the host last saw it.
func (s *Screenshots) Handle(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.ScreenshotReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	format, err := imaging.ParseFormat(r.Format)
	if err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "format: %v", err)
	}
	switch {
	case r.Quality < 0 || r.Quality > imaging.MaxQuality:
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "quality %d is outside 1..100 (0 means the default, %d)", r.Quality, imaging.DefaultQuality)
	case r.MaxLongEdge != 0 && (r.MaxLongEdge < minLongEdge || r.MaxLongEdge > maxLongEdge):
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "max_long_edge %d is outside %d..%d (0 means the whole screen)", r.MaxLongEdge, minLongEdge, maxLongEdge)
	case r.IfChangedSince != "" && !reHash.MatchString(r.IfChangedSince):
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "if_changed_since must be a hash from an earlier response")
	case r.MarksGeneration != "":
		return nil, nil, server.Fail(protocol.CodeUnsupported, "the set-of-marks overlay needs the accessibility service, which this agent does not have yet")
	}

	img, err := s.Screen.Capture(ctx)
	if err != nil {
		return nil, nil, server.Fail(protocol.CodeInternal, "capturing the screen: %v", err)
	}
	hash := s.Hasher.Hash(img)
	cursor := s.cursor()
	if r.IfChangedSince == hash {
		return protocol.ScreenshotRes{Hash: hash, Unchanged: true, Cursor: cursor}, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	scaled, scale := imaging.Scale(img, r.MaxLongEdge)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	data, err := imaging.Encode(scaled, format, r.Quality)
	if err != nil {
		return nil, nil, server.Fail(protocol.CodeInternal, "encoding the screenshot: %v", err)
	}
	if len(data) > protocol.MaxBlobBytes {
		return nil, nil, server.Fail(protocol.CodeUnsupported, "the %s image is %s, over the %s limit; ask for webp or jpeg, or a smaller max_long_edge",
			format, size(len(data)), size(protocol.MaxBlobBytes))
	}
	b := scaled.Bounds()
	return protocol.ScreenshotRes{W: b.Dx(), H: b.Dy(), Format: string(format), Hash: hash, Cursor: cursor, Scale: scale}, data, nil
}

// cursor is the pointer position, or the origin when the display cannot say (the position is a convenience).
func (s *Screenshots) cursor() protocol.Point {
	x, y, err := s.Screen.Pointer()
	if err != nil {
		return protocol.Point{}
	}
	return protocol.Point{X: x, Y: y}
}

func size(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n>>10)
}
