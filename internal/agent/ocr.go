// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/ocr"
	"github.com/PacifAIst/controlsnack/internal/agent/server"
	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// Limits of an `ocr` request.
const (
	// minOCRRegion is the smallest region worth reading: below it there is no line of text.
	minOCRRegion = 16
	minOCRBudget = 1000
	maxOCRScale  = 3
	maxOCRBudget = 120000
	// maxOCRLines bounds the answer: a screen of small text has a few hundred lines at most.
	maxOCRLines = 300
	defaultLang = "eng"
)

// OCR serves `ocr` requests: it reads the text on the screen (or a region of it) and says where each line is, so a
// model that cannot see well, or that sees only text, can still point at things (PLAN.md section 8).
type OCR struct {
	Screen Screen
	Hasher imaging.Hasher
	Engine ocr.Engine
	// Enlarge is the factor the picture is enlarged by before it is read: the text of a screen is about 12 pixels
	// tall, and recognition does better at twice that.
	Enlarge int
	// MinConf drops words below this confidence (ocr.MinConf when zero).
	MinConf int

	mu     sync.Mutex
	last   int
	cached *ocrCached
}

// ocrCached is the last answer: asked again about the same screen, region and language, the answer is the same, ids
// and generation included.
type ocrCached struct {
	key string
	res protocol.OCRRes
}

// NewOCR returns the handler for a display and a recognition engine.
func NewOCR(s Screen, h imaging.Hasher, e ocr.Engine) *OCR {
	return &OCR{Screen: s, Hasher: h, Engine: e, Enlarge: 2}
}

// Handle answers one `ocr` request.
func (o *OCR) Handle(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.OCRReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	lang := r.Lang
	if lang == "" {
		lang = defaultLang
	}
	if !ocr.ValidLanguage(lang) {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "lang %q is not a language name (eng, deu, eng+deu)", lang)
	}
	factor := max(o.Enlarge, 1)
	if r.Scale != 0 {
		if r.Scale < 1 || r.Scale > maxOCRScale {
			return nil, nil, server.Fail(protocol.CodeInvalidArgs, "scale %d is outside 1..%d (0 means the default)", r.Scale, maxOCRScale)
		}
		factor = r.Scale
	}
	budget := ocr.DefaultTimeout
	if r.BudgetMS != 0 {
		if r.BudgetMS < minOCRBudget || r.BudgetMS > maxOCRBudget {
			return nil, nil, server.Fail(protocol.CodeInvalidArgs, "budget_ms %d is outside %d..%d (0 means the default)", r.BudgetMS, minOCRBudget, maxOCRBudget)
		}
		budget = time.Duration(r.BudgetMS) * time.Millisecond
	}

	res, err := o.read(ctx, r.Region, lang, factor, budget)
	if err != nil {
		return nil, nil, err
	}
	return res, nil, nil
}

// Contains reads the screen, or a region of it, and says whether the text is on it, ignoring case. It shares the
// handler's cache: asked about a screen that has not changed since the last reading, it costs nothing.
func (o *OCR) Contains(ctx context.Context, text string, region *protocol.Rect, budget time.Duration) (bool, error) {
	res, err := o.read(ctx, region, defaultLang, max(o.Enlarge, 1), budget)
	if err != nil {
		return false, err
	}
	want := strings.ToLower(text)
	for _, l := range res.Lines {
		if strings.Contains(strings.ToLower(l.Text), want) {
			return true, nil
		}
	}
	return false, nil
}

// read recognizes the text on the screen or in a region of it, in guest pixels. Errors are already the protocol's.
func (o *OCR) read(ctx context.Context, rg *protocol.Rect, lang string, factor int, budget time.Duration) (protocol.OCRRes, error) {
	img, err := o.Screen.Capture(ctx)
	if err != nil {
		return protocol.OCRRes{}, server.Fail(protocol.CodeInternal, "capturing the screen: %v", err)
	}
	screen := img.Bounds()
	region := screen
	if rg != nil {
		region = image.Rect(rg[0], rg[1], rg[0]+rg[2], rg[1]+rg[3])
		if rg[2] < minOCRRegion || rg[3] < minOCRRegion || !region.In(screen) {
			return protocol.OCRRes{}, server.Fail(protocol.CodeInvalidArgs, "region %v must be at least %dx%d and inside the %dx%d screen",
				[4]int(*rg), minOCRRegion, minOCRRegion, screen.Dx(), screen.Dy())
		}
	}
	key := fmt.Sprintf("%s|%v|%s|%d", o.Hasher.Hash(img), region, lang, factor)
	if res, ok := o.lookup(key); ok {
		return res, nil
	}

	data, err := imaging.Encode(imaging.Enlarge(imaging.Crop(img, region), factor), imaging.PNG, 0)
	if err != nil {
		return protocol.OCRRes{}, server.Fail(protocol.CodeInternal, "preparing the picture: %v", err)
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	words, err := o.Engine.Recognize(bctx, data, lang)
	if err != nil {
		return protocol.OCRRes{}, ocrError(ctx, err, lang, budget)
	}

	// Back to guest pixels, then into lines.
	for i := range words {
		w := &words[i]
		x0, y0 := w.X/factor, w.Y/factor
		x1, y1 := (w.X+w.W+factor-1)/factor, (w.Y+w.H+factor-1)/factor
		w.X, w.Y, w.W, w.H = region.Min.X+x0, region.Min.Y+y0, max(x1-x0, 1), max(y1-y0, 1)
	}
	minConf := o.MinConf
	if minConf <= 0 {
		minConf = ocr.MinConf
	}
	lines := ocr.Group(words, minConf)

	out := protocol.OCRRes{Lines: make([]protocol.OCRLine, 0, len(lines))}
	if len(lines) > maxOCRLines {
		lines, out.Truncated = lines[:maxOCRLines], true
	}
	for i, l := range lines {
		out.Lines = append(out.Lines, protocol.OCRLine{ID: "o" + strconv.Itoa(i+1), Text: l.Text, Bounds: protocol.Rect{l.X, l.Y, l.W, l.H}, Conf: l.Conf})
	}
	if err := ctx.Err(); err != nil {
		return protocol.OCRRes{}, err
	}
	return o.remember(key, out), nil
}

func (o *OCR) lookup(key string) (protocol.OCRRes, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cached != nil && o.cached.key == key {
		return o.cached.res, true
	}
	return protocol.OCRRes{}, false
}

// remember gives the answer a generation and keeps it as the last one.
func (o *OCR) remember(key string, res protocol.OCRRes) protocol.OCRRes {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.last++
	res.Generation = "g" + strconv.Itoa(o.last)
	o.cached = &ocrCached{key: key, res: res}
	return res
}

// ocrError turns what recognition said into the protocol's error codes.
func ocrError(ctx context.Context, err error, lang string, budget time.Duration) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, ocr.ErrUnavailable):
		return server.Fail(protocol.CodeUnsupported, "text recognition (tesseract) is not installed in this image")
	case errors.Is(err, ocr.ErrLanguage):
		return server.Fail(protocol.CodeInvalidArgs, "language %q is not installed", lang)
	case errors.Is(err, ocr.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return server.Fail(protocol.CodeTimeout, "text recognition took longer than %s; read a smaller region", budget)
	}
	return server.Fail(protocol.CodeInternal, "text recognition failed: %v", err)
}
