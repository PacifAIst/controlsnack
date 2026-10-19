// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/ocr"
	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// fakeEngine returns fixed words, in the pixels of whatever picture it is given, and notes what it was asked.
type fakeEngine struct {
	words    []ocr.Word
	err      error
	calls    int
	gotLang  string
	gotW     int
	gotH     int
	deadline time.Duration
	block    bool
}

func (f *fakeEngine) Recognize(ctx context.Context, data []byte, lang string) ([]ocr.Word, error) {
	f.calls++
	f.gotLang = lang
	if cfg, err := png.DecodeConfig(bytes.NewReader(data)); err == nil {
		f.gotW, f.gotH = cfg.Width, cfg.Height
	}
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return slices.Clone(f.words), f.err
}

func newOCRRig(w, h int, words ...ocr.Word) (*OCR, *fakeEngine, *fakeScreen) {
	scr := testScreen(w, h)
	eng := &fakeEngine{words: words}
	return NewOCR(scr, imaging.NewHasher(), eng), eng, scr
}

func ocrWord(block, line, num, x, y, w, h, conf int, text string) ocr.Word {
	return ocr.Word{Block: block, Par: 1, Line: line, Num: num, X: x, Y: y, W: w, H: h, Conf: conf, Text: text}
}

func ocrCall(t *testing.T, o *OCR, req protocol.OCRReq) (protocol.OCRRes, error) {
	t.Helper()
	res, _, err := o.Handle(context.Background(), frame(t, protocol.TypeOCR, req))
	if err != nil {
		return protocol.OCRRes{}, err
	}
	return res.(protocol.OCRRes), nil
}

func TestOCRReadsTheScreen(t *testing.T) {
	// The engine sees a picture twice as big, so its boxes are twice the guest's.
	o, eng, _ := newOCRRig(320, 200,
		ocrWord(1, 1, 1, 20, 40, 60, 24, 95, "Save"), ocrWord(1, 1, 2, 90, 40, 80, 24, 90, "changes?"),
		ocrWord(2, 1, 1, 20, 200, 100, 24, 30, "noise"),
		ocrWord(3, 1, 1, 400, 40, 50, 24, 88, "Cancel"))
	res, err := ocrCall(t, o, protocol.OCRReq{})
	if err != nil {
		t.Fatal(err)
	}
	if eng.gotW != 640 || eng.gotH != 400 || eng.gotLang != "eng" {
		t.Errorf("the engine got a %dx%d picture in %q", eng.gotW, eng.gotH, eng.gotLang)
	}
	if res.Generation != "g1" || res.Truncated || len(res.Lines) != 2 {
		t.Fatalf("%+v", res)
	}
	want := []protocol.OCRLine{
		{ID: "o1", Text: "Save changes?", Bounds: protocol.Rect{10, 20, 75, 12}, Conf: 92},
		{ID: "o2", Text: "Cancel", Bounds: protocol.Rect{200, 20, 25, 12}, Conf: 88},
	}
	if !slices.Equal(res.Lines, want) {
		t.Errorf("got %+v\nwant %+v", res.Lines, want)
	}
}

func TestOCRRegion(t *testing.T) {
	o, eng, _ := newOCRRig(320, 200, ocrWord(1, 1, 1, 10, 6, 40, 20, 90, "Hello"))
	res, err := ocrCall(t, o, protocol.OCRReq{Region: &protocol.Rect{100, 50, 120, 60}, Lang: "deu"})
	if err != nil {
		t.Fatal(err)
	}
	if eng.gotW != 240 || eng.gotH != 120 || eng.gotLang != "deu" {
		t.Errorf("only the region is read, enlarged: %dx%d in %q", eng.gotW, eng.gotH, eng.gotLang)
	}
	if len(res.Lines) != 1 || res.Lines[0].Bounds != (protocol.Rect{105, 53, 20, 10}) {
		t.Errorf("the boxes are in screen pixels, not region pixels: %+v", res.Lines)
	}
}

func TestOCRCachesByScreenRegionAndLanguage(t *testing.T) {
	o, eng, scr := newOCRRig(320, 200, ocrWord(1, 1, 1, 10, 10, 40, 20, 90, "Hello"))
	first, _ := ocrCall(t, o, protocol.OCRReq{})
	again, _ := ocrCall(t, o, protocol.OCRReq{})
	if eng.calls != 1 || again.Generation != first.Generation || len(again.Lines) != 1 {
		t.Errorf("the same screen gives the same answer without reading again: %d calls, %s vs %s", eng.calls, first.Generation, again.Generation)
	}
	if _, err := ocrCall(t, o, protocol.OCRReq{Region: &protocol.Rect{0, 0, 100, 100}}); err != nil || eng.calls != 2 {
		t.Errorf("another region reads again (%d calls): %v", eng.calls, err)
	}
	if _, err := ocrCall(t, o, protocol.OCRReq{Lang: "deu"}); err != nil || eng.calls != 3 {
		t.Errorf("another language reads again (%d calls): %v", eng.calls, err)
	}
	scr.img.Pix[0] ^= 0xff
	res, err := ocrCall(t, o, protocol.OCRReq{Lang: "deu"})
	if err != nil || eng.calls != 4 || res.Generation == first.Generation {
		t.Errorf("a screen that changed reads again, with a new generation (%d calls): %+v %v", eng.calls, res, err)
	}
}

func TestOCRRefusals(t *testing.T) {
	o, eng, _ := newOCRRig(320, 200)
	for name, req := range map[string]protocol.OCRReq{
		"language":      {Lang: "eng;rm"},
		"region small":  {Region: &protocol.Rect{0, 0, 8, 100}},
		"region flat":   {Region: &protocol.Rect{0, 0, 100, 8}},
		"region right":  {Region: &protocol.Rect{300, 0, 100, 100}},
		"region bottom": {Region: &protocol.Rect{0, 150, 100, 100}},
		"region left":   {Region: &protocol.Rect{-5, 0, 100, 100}},
		"budget low":    {BudgetMS: 10},
		"budget high":   {BudgetMS: 999999},
	} {
		if _, err := ocrCall(t, o, req); code(err) != protocol.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
	if eng.calls != 0 {
		t.Errorf("a refused request reads nothing (%d calls)", eng.calls)
	}
	if _, _, err := o.Handle(context.Background(), frame(t, protocol.TypeOCR, map[string]any{"mode": "fast"})); code(err) != protocol.CodeInvalidArgs {
		t.Errorf("an unknown field: %v", err)
	}
}

func TestOCRErrorsCarryTheProtocolCodes(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		code string
		text string
	}{
		"missing":  {ocr.ErrUnavailable, protocol.CodeUnsupported, "not installed"},
		"language": {fmt.Errorf("%w: %q", ocr.ErrLanguage, "deu"), protocol.CodeInvalidArgs, "deu"},
		"timeout":  {ocr.ErrTimeout, protocol.CodeTimeout, "smaller region"},
		"deadline": {context.DeadlineExceeded, protocol.CodeTimeout, "took longer"},
		"other":    {errors.New("pixReadStream failed"), protocol.CodeInternal, "pixReadStream"},
	} {
		o, eng, _ := newOCRRig(320, 200)
		eng.err = tt.err
		_, err := ocrCall(t, o, protocol.OCRReq{Lang: "deu"})
		if code(err) != tt.code || !strings.Contains(err.Error(), tt.text) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOCRBudget(t *testing.T) {
	o, eng, _ := newOCRRig(320, 200)
	if _, err := ocrCall(t, o, protocol.OCRReq{}); err != nil || eng.deadline < 9*time.Second || eng.deadline > ocr.DefaultTimeout {
		t.Errorf("the default budget is %s: %v %v", ocr.DefaultTimeout, eng.deadline, err)
	}
	o.cached = nil
	if _, err := ocrCall(t, o, protocol.OCRReq{BudgetMS: 40000}); err != nil || eng.deadline < 39*time.Second || eng.deadline > 40*time.Second {
		t.Errorf("a longer budget is passed on: %v %v", eng.deadline, err)
	}
	o.cached = nil
	eng.block = true
	begin := time.Now()
	_, err := ocrCall(t, o, protocol.OCRReq{BudgetMS: 1000})
	if code(err) != protocol.CodeTimeout || time.Since(begin) > 10*time.Second {
		t.Errorf("a recognition that runs over its budget times out: %v after %s", err, time.Since(begin))
	}
}

func TestOCRStopsWhenCanceled(t *testing.T) {
	o, eng, _ := newOCRRig(320, 200)
	eng.block = true
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if _, _, err := o.Handle(ctx, frame(t, protocol.TypeOCR, protocol.OCRReq{})); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}

func TestOCRCapsTheAnswer(t *testing.T) {
	var words []ocr.Word
	for i := range maxOCRLines + 40 {
		words = append(words, ocrWord(i+1, 1, 1, 10, 4+i*26, 40, 10, 90, fmt.Sprintf("line%d", i)))
	}
	o, _, _ := newOCRRig(320, 200, words...)
	res, err := ocrCall(t, o, protocol.OCRReq{})
	if err != nil || len(res.Lines) != maxOCRLines || !res.Truncated || res.Lines[0].ID != "o1" || res.Lines[maxOCRLines-1].ID != fmt.Sprintf("o%d", maxOCRLines) {
		t.Errorf("%d lines, truncated=%v, %v", len(res.Lines), res.Truncated, err)
	}
}

func TestOCRCaptureFailure(t *testing.T) {
	o, _, scr := newOCRRig(320, 200)
	scr.capErr = errors.New("BadMatch")
	if _, err := ocrCall(t, o, protocol.OCRReq{}); code(err) != protocol.CodeInternal || !strings.Contains(err.Error(), "BadMatch") {
		t.Errorf("%v", err)
	}
}

func TestOCRScale(t *testing.T) {
	// At scale 3 the engine's boxes are three times the guest's; at scale 1 they are the same.
	o, eng, _ := newOCRRig(320, 200, ocrWord(1, 1, 1, 30, 31, 60, 20, 90, "Hello"))
	res, err := ocrCall(t, o, protocol.OCRReq{Scale: 3})
	if err != nil || eng.gotW != 960 || eng.gotH != 600 || len(res.Lines) != 1 || res.Lines[0].Bounds != (protocol.Rect{10, 10, 20, 7}) {
		t.Errorf("scale 3: the engine got %dx%d: %+v %v", eng.gotW, eng.gotH, res.Lines, err)
	}
	res, err = ocrCall(t, o, protocol.OCRReq{Scale: 1})
	if err != nil || eng.gotW != 320 || eng.gotH != 200 || res.Lines[0].Bounds != (protocol.Rect{30, 31, 60, 20}) || eng.calls != 2 {
		t.Errorf("scale 1: the engine got %dx%d: %+v %v (%d calls)", eng.gotW, eng.gotH, res.Lines, err, eng.calls)
	}
	for _, s := range []int{-1, 4, 100} {
		if _, err := ocrCall(t, o, protocol.OCRReq{Scale: s}); code(err) != protocol.CodeInvalidArgs {
			t.Errorf("scale %d: %v", s, err)
		}
	}
}

func TestOCRContains(t *testing.T) {
	o, eng, _ := newOCRRig(320, 200, ocrWord(1, 1, 1, 20, 40, 60, 24, 95, "Save"), ocrWord(1, 1, 2, 90, 40, 80, 24, 90, "Changes?"))
	for text, want := range map[string]bool{"save changes": true, "CHANGES": true, "ve ch": true, "nope": false} {
		got, err := o.Contains(context.Background(), text, nil, 10*time.Second)
		if err != nil || got != want {
			t.Errorf("%q: %v %v, want %v", text, got, err, want)
		}
	}
	if eng.calls != 1 {
		t.Errorf("an unchanged screen is read once (%d readings)", eng.calls)
	}
	if _, err := o.Contains(context.Background(), "x", &protocol.Rect{0, 0, 4, 4}, time.Second); code(err) != protocol.CodeInvalidArgs {
		t.Errorf("a region too small: %v", err)
	}
	eng.err = ocr.ErrUnavailable
	o2, eng2, _ := newOCRRig(320, 200)
	eng2.err = ocr.ErrUnavailable
	if _, err := o2.Contains(context.Background(), "x", nil, time.Second); code(err) != protocol.CodeUnsupported {
		t.Errorf("%v", err)
	}
}
