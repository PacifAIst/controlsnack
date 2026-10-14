// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"fmt"
	"image"
	"math"
	"regexp"
	"slices"
	"strconv"

	"github.com/PacifAIst/controlsnack-local/internal/agent/a11y"
	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// Screen is what the screenshot handler needs from the display.
type Screen interface {
	// Capture reads the whole screen. The image belongs to the caller: other workers capture at the same time.
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

// MarkSource gives the boxes to draw for a generation of the accessibility tree; *a11y.Service is the real one.
type MarkSource interface {
	Marks(generation string) ([]a11y.Mark, bool)
}

var reGenerationName = regexp.MustCompile(`^g[0-9]{1,9}$`)

// Screenshots serves `screenshot` requests (PLAN.md section 5.1).
type Screenshots struct {
	Screen Screen
	Hasher imaging.Hasher
	// Marks, when set, lets a request ask for the set-of-marks overlay (D-30). Without it that request is unsupported.
	Marks MarkSource
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
	case r.MarksGeneration != "" && !reGenerationName.MatchString(r.MarksGeneration):
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "marks_generation must be a generation from an accessibility tree (g and a number)")
	case r.MarksGeneration != "" && s.Marks == nil:
		return nil, nil, server.Fail(protocol.CodeUnsupported, "the set-of-marks overlay needs the accessibility service, which is not available in this session")
	}
	var marks []a11y.Mark
	if r.MarksGeneration != "" {
		var ok bool
		if marks, ok = s.Marks.Marks(r.MarksGeneration); !ok {
			return nil, nil, server.Fail(protocol.CodeStaleElement, "generation %s is stale; read the accessibility tree again", r.MarksGeneration)
		}
	}

	img, err := s.Screen.Capture(ctx)
	if err != nil {
		return nil, nil, server.Fail(protocol.CodeInternal, "capturing the screen: %v", err)
	}
	hash := s.Hasher.Hash(img)
	cursor := s.cursor()
	// The marks belong to a generation, not to the screen: with marks the picture is always sent.
	if r.IfChangedSince == hash && r.MarksGeneration == "" {
		return protocol.ScreenshotRes{Hash: hash, Unchanged: true, Cursor: cursor}, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	scaled, scale := imaging.Scale(img, r.MaxLongEdge)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var shown image.Image = scaled
	var marked []string
	if len(marks) > 0 {
		scaledMarks := scaleMarks(marks, scale)
		shown = imaging.DrawMarks(scaled, scaledMarks)
		marked = drawnIDs(marks, imaging.Drawn(scaled.Bounds(), scaledMarks))
	}
	data, err := imaging.Encode(shown, format, r.Quality)
	if err != nil {
		return nil, nil, server.Fail(protocol.CodeInternal, "encoding the screenshot: %v", err)
	}
	if len(data) > protocol.MaxBlobBytes {
		return nil, nil, server.Fail(protocol.CodeUnsupported, "the %s image is %s, over the %s limit; ask for webp or jpeg, or a smaller max_long_edge",
			format, size(len(data)), size(protocol.MaxBlobBytes))
	}
	b := shown.Bounds()
	return protocol.ScreenshotRes{W: b.Dx(), H: b.Dy(), Format: string(format), Hash: hash, Cursor: cursor, Scale: scale, Marked: marked}, data, nil
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

// scaleMarks converts boxes from guest pixels to the pixels of the picture, which is scale times as big as the screen.
// The label is the id without its letter: a12 is drawn as 12.
func scaleMarks(marks []a11y.Mark, scale float64) []imaging.Mark {
	out := make([]imaging.Mark, 0, len(marks))
	for _, m := range marks {
		if len(m.ID) < 2 {
			continue
		}
		x0, y0 := int(math.Round(float64(m.X)*scale)), int(math.Round(float64(m.Y)*scale))
		x1, y1 := int(math.Round(float64(m.X+m.W)*scale)), int(math.Round(float64(m.Y+m.H)*scale))
		out = append(out, imaging.Mark{Label: m.ID[1:], X: x0, Y: y0, W: x1 - x0, H: y1 - y0})
	}
	return out
}

// drawnIDs names the elements whose boxes are on the picture, in the order of their numbers: the model is told which
// ids it can look for as a number on the image.
func drawnIDs(marks []a11y.Mark, drawn []imaging.Mark) []string {
	byLabel := make(map[string]string, len(marks))
	for _, m := range marks {
		if len(m.ID) >= 2 {
			byLabel[m.ID[1:]] = m.ID
		}
	}
	ids := make([]string, 0, len(drawn))
	for _, d := range drawn {
		if id, ok := byLabel[d.Label]; ok {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int {
		na, _ := strconv.Atoi(a[1:])
		nb, _ := strconv.Atoi(b[1:])
		return na - nb
	})
	return ids
}
