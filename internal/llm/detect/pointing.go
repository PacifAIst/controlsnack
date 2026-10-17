// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"regexp"
	"strconv"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// The ways a model says where something is on a picture.
const (
	// CoordsPixels: in pixels of the picture, as the system prompt asks.
	CoordsPixels = "pixels"
	// CoordsRelative: on a scale of 0 to RelativeScale across each axis, whatever the size of the picture. The Qwen-VL
	// family and others answer this way; asked for pixels, they still do not.
	CoordsRelative = "relative"
	// RelativeScale is the top of that scale.
	RelativeScale = 1000
)

// CheckPointing is the check that finds out which of the two a model does.
const CheckPointing = "pointing"

// ProbePointQuestion goes with the picture of the pointing check. It states the size of the picture, as the system prompt
// does in a session, so that a model that reads it can answer in pixels.
const ProbePointQuestion = `This picture is 640 pixels wide and 400 pixels high. It is white with one colored square. Where is the middle of the colored square? Answer with only this JSON object, x across from the left edge and y down from the top edge: {"x":<number>,"y":<number>}`

// The square of the pointing picture runs from (400, 80) to (520, 200) pixels; its middle is (460, 140) pixels, which is
// (718, 350) on the relative scale. An answer counts as being on a scale when it lands on the square, with a margin, as that
// scale reads it: a small model may point at a corner and not the middle, and the scale shows all the same. The two read
// the same answer so differently that none lands on the square on both.
const (
	pointW, pointH                 = 640, 400
	pointMidX, pointMidY           = 460, 140
	pointLeft, pointTop            = 400, 80
	pointRight, pointBottom        = 520, 200
	pointMargin                    = 25
	pointRelativeX, pointRelativeY = pointMidX * RelativeScale / pointW, pointMidY * RelativeScale / pointH
)

func pointImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, pointW, pointH))
	for y := 0; y < pointH; y++ {
		for x := 0; x < pointW; x++ {
			c := color.RGBA{R: 255, G: 255, B: 255, A: 255}
			if x >= 400 && x < 520 && y >= 80 && y < 200 {
				c = color.RGBA{R: 220, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

var reNumber = regexp.MustCompile(`-?[0-9]+(?:\.[0-9]+)?`)

// parsePoint reads the two numbers of an answer: the object that was asked for, else the first two numbers in the words.
func parsePoint(text string) (x, y float64, ok bool) {
	text = strings.TrimSpace(text)
	var obj struct {
		X, Y json.Number
	}
	dec := json.NewDecoder(strings.NewReader(text[max(strings.IndexByte(text, '{'), 0):]))
	if err := dec.Decode(&obj); err == nil && obj.X != "" && obj.Y != "" {
		fx, errx := obj.X.Float64()
		fy, erry := obj.Y.Float64()
		if errx == nil && erry == nil {
			return fx, fy, true
		}
	}
	nums := reNumber.FindAllString(text, 3)
	if len(nums) != 2 {
		return 0, 0, false
	}
	x, _ = strconv.ParseFloat(nums[0], 64)
	y, _ = strconv.ParseFloat(nums[1], 64)
	return x, y, true
}

// classifyPoint says which scale an answer is on, or "" when it is on neither.
func classifyPoint(x, y float64) string {
	switch {
	case onTheSquare(x, y):
		return CoordsPixels
	case onTheSquare(x*pointW/RelativeScale, y*pointH/RelativeScale):
		return CoordsRelative
	}
	return ""
}

// onTheSquare reports whether a position in pixels of the pointing picture is on the square, within the margin.
func onTheSquare(x, y float64) bool {
	return x >= pointLeft-pointMargin && x <= pointRight+pointMargin && y >= pointTop-pointMargin && y <= pointBottom+pointMargin
}

// pointing asks where the middle of a square is and finds out whether the model answers in pixels or on the scale of 0 to
// 1000. A session needs to know: a model asked for pixels that answers on the other scale would click in the wrong place
// every time. A model that answers neither is left at pixels, and the check says so.
func (p *prober) pointing(ctx context.Context) error {
	if !p.caps.Vision {
		p.skip(CheckPointing, "no vision")
		return nil
	}
	p.caps.Coords = CoordsPixels
	data, err := imaging.Encode(pointImage(), imaging.PNG, 0)
	if err != nil {
		return fmt.Errorf("making the test picture: %w", err)
	}
	resp, c, err := p.ask(ctx, CheckPointing, backend.Request{
		Messages: []backend.Message{{Role: backend.RoleUser, Text: ProbePointQuestion, Images: []backend.Image{{MIME: backend.MIMEPNG, Data: data}}}},
		Mode:     backend.ModeJSON,
	})
	if err != nil {
		return err
	}
	if c.Detail == "" {
		x, y, ok := parsePoint(parse.StripReasoning(resp.Reply.Text))
		switch kind := classifyPoint(x, y); {
		case !ok:
			c.Detail = "no position in the answer: " + answerOf(resp)
		case kind == "":
			c.Detail = fmt.Sprintf("pointed at (%g, %g), which is on the square on neither scale (pixels: around (%d, %d); 0 to %d: around (%d, %d)): clicks by position may miss", x, y, pointMidX, pointMidY, RelativeScale, pointRelativeX, pointRelativeY)
		default:
			c.OK, p.caps.Coords = true, kind
			c.Detail = fmt.Sprintf("pointed at (%g, %g): %s", x, y, kind)
		}
	}
	p.add(c)
	return nil
}
