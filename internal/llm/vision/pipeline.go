// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// Caller is the host's end of the agent protocol (agentclient.Client).
type Caller interface {
	Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error)
}

// Config is how the pictures for the model are made.
type Config struct {
	// Guest is the size of the guest screen, from the handshake.
	Guest prompt.Size
	// Format is the image format the model takes (Capabilities.ImageFormat): WebP, else JPEG.
	Format imaging.Format
	// Quality is the WebP or JPEG quality (the default when 0).
	Quality int
	// MaxLongEdge is llm.image_max, the longest side of the picture in pixels.
	MaxLongEdge int
	// Marks draws numbered boxes over the elements of the accessibility tree whose generation is given to Capture.
	Marks bool
}

// Shot is a picture for the model with what goes with it.
type Shot struct {
	Image backend.Image
	// Note is what the observation says about the picture.
	Note prompt.ImageNote
	// Mapper converts between the screen and this picture.
	Mapper Mapper
	// Cursor is the pointer, in the picture's coordinates.
	Cursor prompt.Point
	// Hash identifies the screen contents (not the marks) to the agent's change detection.
	Hash string
	// Marked lists the elements that carry a box.
	Marked []string
	// Reused is set when the screen had not changed and the picture is the one of the last shot.
	Reused bool
}

// Pipeline asks the agent for the pictures the model sees.
type Pipeline struct {
	c    Caller
	cfg  Config
	last *Shot
}

// NewPipeline checks the configuration and makes a Pipeline.
func NewPipeline(c Caller, cfg Config) (*Pipeline, error) {
	if c == nil {
		return nil, errors.New("the pipeline needs a connection to the agent")
	}
	if _, err := Identity(cfg.Guest); err != nil {
		return nil, fmt.Errorf("the guest screen: %w", err)
	}
	switch cfg.Format {
	case imaging.WebP, imaging.JPEG, imaging.PNG:
	default:
		return nil, fmt.Errorf("the image format %q is not webp, jpeg or png", cfg.Format)
	}
	if cfg.MaxLongEdge < 0 {
		return nil, fmt.Errorf("the longest side of the picture is %d", cfg.MaxLongEdge)
	}
	return &Pipeline{c: c, cfg: cfg}, nil
}

var reMarked = regexp.MustCompile(`^[aw][0-9]{1,6}$`)

// maxMarked is the most boxes the agent draws on one picture.
const maxMarked = 100

// Capture takes a picture of the screen. With marks configured and a generation of the accessibility tree given, the
// picture carries a numbered box on each element of that tree that can be operated; otherwise it is the plain screen.
// Everything the agent reports is checked before it is used: the format and size of the picture against what it said, the
// size against the screen, the ids of the marks.
func (p *Pipeline) Capture(ctx context.Context, generation string) (Shot, error) {
	marks := p.cfg.Marks && generation != ""
	req := protocol.ScreenshotReq{Format: string(p.cfg.Format), Quality: p.cfg.Quality, MaxLongEdge: p.cfg.MaxLongEdge}
	if marks {
		req.MarksGeneration = generation
	} else if p.last != nil {
		req.IfChangedSince = p.last.Hash
	}
	var res protocol.ScreenshotRes
	blob, err := p.c.Call(ctx, "screenshot", req, nil, &res)
	if err != nil {
		return Shot{}, fmt.Errorf("taking the screenshot: %w", err)
	}
	if res.Unchanged {
		return p.reuse(res, req)
	}
	return p.accept(res, blob, marks)
}

func (p *Pipeline) reuse(res protocol.ScreenshotRes, req protocol.ScreenshotReq) (Shot, error) {
	if req.IfChangedSince == "" || p.last == nil || res.Hash != req.IfChangedSince {
		return Shot{}, errors.New("the agent said the screen had not changed, but nothing was asked about the last picture")
	}
	s := *p.last
	s.Reused = true
	s.Cursor = s.Mapper.Cursor(res.Cursor)
	return s, nil
}

func (p *Pipeline) accept(res protocol.ScreenshotRes, blob []byte, marks bool) (Shot, error) {
	if len(blob) == 0 {
		return Shot{}, errors.New("the agent sent no picture")
	}
	if res.Format != string(p.cfg.Format) {
		return Shot{}, fmt.Errorf("a %s picture was asked for and the agent says it is %q", p.cfg.Format, res.Format)
	}
	if err := imaging.Inspect(blob, p.cfg.Format, res.W, res.H); err != nil {
		return Shot{}, err
	}
	sc := res.Scale
	if sc == 0 {
		sc = 1
	}
	if math.IsNaN(sc) || sc <= 0 || sc > 1.0001 {
		return Shot{}, fmt.Errorf("the agent reports a scale of %v; a picture is never larger than the screen", res.Scale)
	}
	wantW, wantH := int(math.Round(float64(p.cfg.Guest.W)*sc)), int(math.Round(float64(p.cfg.Guest.H)*sc))
	if abs(res.W-wantW) > 1 || abs(res.H-wantH) > 1 {
		return Shot{}, fmt.Errorf("the picture is %dx%d but a screen of %dx%d at scale %v would give %dx%d", res.W, res.H, p.cfg.Guest.W, p.cfg.Guest.H, sc, wantW, wantH)
	}
	if hash := res.Hash; len(hash) == 0 || len(hash) > 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		return Shot{}, fmt.Errorf("the agent's fingerprint of the screen, %q, is not hexadecimal", res.Hash)
	}
	marked, err := checkMarked(res.Marked, marks)
	if err != nil {
		return Shot{}, err
	}
	mapper, err := NewMapper(p.cfg.Guest, prompt.Size{W: res.W, H: res.H})
	if err != nil {
		return Shot{}, err
	}
	s := Shot{
		Image:  backend.Image{MIME: p.cfg.Format.MIME(), Data: blob},
		Note:   prompt.ImageNote{W: res.W, H: res.H, Marks: MarkRange(marked)},
		Mapper: mapper, Cursor: mapper.Cursor(res.Cursor), Hash: res.Hash, Marked: marked,
	}
	if !marks {
		// A picture with boxes is not the screen: only a plain one stands for it when the agent is asked if it changed.
		p.last = &s
	}
	return s, nil
}

func checkMarked(ids []string, asked bool) ([]string, error) {
	switch {
	case len(ids) > 0 && !asked:
		return nil, errors.New("the agent drew marks that were not asked for")
	case len(ids) > maxMarked:
		return nil, fmt.Errorf("the agent says it drew %d marks, the most it draws is %d", len(ids), maxMarked)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !reMarked.MatchString(id) || seen[id] {
			return nil, fmt.Errorf("the agent names a mark %q, which is not an element id or is named twice", id)
		}
		seen[id] = true
	}
	out := append([]string(nil), ids...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		a, _ := strconv.Atoi(out[i][1:])
		b, _ := strconv.Atoi(out[j][1:])
		return a < b
	})
	return out, nil
}

// MarkRange names the elements that carry a box for the observation's picture line: "a1..a23", "a17", or one range for
// each kind of id ("a1..a9_w1..w4"). Elements between the ends that carry no box are not operable, so the range does not
// promise one for each.
func MarkRange(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	var parts []string
	start := 0
	for i := 1; i <= len(ids); i++ {
		if i == len(ids) || ids[i][0] != ids[start][0] {
			if i-1 == start {
				parts = append(parts, ids[start])
			} else {
				parts = append(parts, ids[start]+".."+ids[i-1])
			}
			start = i
		}
	}
	return strings.Join(parts, "_")
}

// MarksEnabled decides whether the pictures carry numbered boxes: --marks on does when there are pictures, auto does for a
// local model with vision (D-30), off never does.
func MarksEnabled(setting string, local, vision bool) bool {
	if !vision {
		return false
	}
	switch setting {
	case "on":
		return true
	case "auto", "":
		return local
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
