// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/agent"
	"github.com/PacifAIst/controlsnack-local/internal/agent/a11y"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// theRealAgent answers the pipeline with the agent's own screenshot handler on a fake display.
type theRealAgent struct{ h *agent.Screenshots }

func (a theRealAgent) Call(ctx context.Context, typ string, req any, _ []byte, res any) ([]byte, error) {
	f, err := protocol.NewFrame(1, typ, req, nil)
	if err != nil {
		return nil, err
	}
	out, blob, err := a.h.Handle(ctx, f)
	if err != nil {
		return nil, err
	}
	*res.(*protocol.ScreenshotRes) = out.(protocol.ScreenshotRes)
	return blob, nil
}

type display struct {
	img    *image.RGBA
	px, py int
}

func (d display) Capture(context.Context) (*image.RGBA, error) { return d.img, nil }
func (d display) Pointer() (int, int, error)                   { return d.px, d.py, nil }

type markSource struct{ marks []a11y.Mark }

func (m markSource) Marks(g string) ([]a11y.Mark, bool) { return m.marks, g == "g1" }

// block is a colored rectangle on the fake screen, in guest pixels.
type block struct {
	r  image.Rectangle
	c  color.RGBA
	id string
}

func TestWhatTheModelPointsAtInThePictureIsWhatIsOnTheScreen(t *testing.T) {
	const gw, gh = 1920, 1080
	screen := image.NewRGBA(image.Rect(0, 0, gw, gh))
	for i := range screen.Pix {
		screen.Pix[i] = 255
	}
	blocks := []block{
		{image.Rect(100, 100, 400, 220), color.RGBA{200, 0, 0, 255}, "a1"},
		{image.Rect(1500, 900, 1900, 1060), color.RGBA{0, 0, 200, 255}, "a2"},
		{image.Rect(937, 501, 983, 579), color.RGBA{0, 160, 0, 255}, "a3"}, // small and off the grid
	}
	var marks []a11y.Mark
	for _, b := range blocks {
		for y := b.r.Min.Y; y < b.r.Max.Y; y++ {
			for x := b.r.Min.X; x < b.r.Max.X; x++ {
				screen.SetRGBA(x, y, b.c)
			}
		}
		marks = append(marks, a11y.Mark{ID: b.id, X: b.r.Min.X, Y: b.r.Min.Y, W: b.r.Dx(), H: b.r.Dy()})
	}
	h := agent.NewScreenshots(display{img: screen, px: 960, py: 540})
	h.Marks = markSource{marks: marks}

	for _, edge := range []int{1280, 1024, 800, 640} {
		p, err := NewPipeline(theRealAgent{h}, Config{Guest: prompt.Size{W: gw, H: gh}, Format: imaging.PNG, MaxLongEdge: edge, Marks: true})
		if err != nil {
			t.Fatal(err)
		}
		shot, err := p.Capture(context.Background(), "g1")
		if err != nil {
			t.Fatalf("edge %d: %v", edge, err)
		}
		if shot.Note.W != edge || shot.Note.Marks != "a1..a3" || len(shot.Marked) != 3 {
			t.Fatalf("edge %d: note = %+v marked = %v", edge, shot.Note, shot.Marked)
		}
		if shot.Cursor != shot.Mapper.Point(960, 540) {
			t.Errorf("edge %d: the cursor is %v, the middle of the screen is %v", edge, shot.Cursor, shot.Mapper.Point(960, 540))
		}
		pic, err := png.Decode(bytes.NewReader(shot.Image.Data))
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range blocks[:2] { // the third is too small to find at the smallest sizes; it has its own check below
			found := boundsOfColor(pic, b.c)
			if found.Empty() {
				t.Fatalf("edge %d: no %v block in the picture", edge, b.c)
			}
			// 1. What the model reads: the block's bounds on the screen, converted, lie where the block is in the picture.
			want := shot.Mapper.Rect(protocol.Rect{b.r.Min.X, b.r.Min.Y, b.r.Dx(), b.r.Dy()})
			if abs(found.Min.X-want[0]) > 2 || abs(found.Min.Y-want[1]) > 2 || abs(found.Dx()-want[2]) > 4 || abs(found.Dy()-want[3]) > 4 {
				t.Errorf("edge %d: block %s is at %v in the picture but its bounds were converted to %v", edge, b.id, found, want)
			}
			// 2. What the model says: it clicks the middle of the block as it sees it, and the click lands on the block.
			cx, cy := (found.Min.X+found.Max.X)/2, (found.Min.Y+found.Max.Y)/2
			a := parseAction(t, "mouse", fmt.Sprintf(`{"op":"click","x":%d,"y":%d}`, cx, cy))
			g, err := shot.Mapper.Guest(a)
			if err != nil {
				t.Fatal(err)
			}
			m := g.Args.(*actions.MouseArgs)
			if !image.Pt(m.X, m.Y).In(b.r) {
				t.Errorf("edge %d: a click at %d,%d of the picture is at %d,%d of the screen, which is not on block %s %v", edge, cx, cy, m.X, m.Y, b.id, b.r)
			}
		}
	}
}

// boundsOfColor is the smallest rectangle of the picture that holds every pixel of the color (nearly: the edges of a
// scaled block are blended with their surroundings).
func boundsOfColor(img image.Image, c color.RGBA) image.Rectangle {
	var out image.Rectangle
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if near(uint8(r>>8), c.R) && near(uint8(g>>8), c.G) && near(uint8(bl>>8), c.B) {
				out = out.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return out
}

func near(a, b uint8) bool { return abs(int(a)-int(b)) < 12 }
