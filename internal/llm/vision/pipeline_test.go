// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// fakeAgent answers screenshot requests like the agent does, and lets a test make it lie.
type fakeAgent struct {
	guest  prompt.Size
	scale  float64
	format imaging.Format
	marked []string
	hash   string
	cursor protocol.Point
	// unchangedFor is the hash for which it answers "unchanged".
	unchangedFor string
	reqs         []protocol.ScreenshotReq
	// lie edits the response and the picture before they are sent.
	lie func(res *protocol.ScreenshotRes, blob *[]byte)
	err error
}

func newAgent(w, h int, scale float64) *fakeAgent {
	return &fakeAgent{guest: prompt.Size{W: w, H: h}, scale: scale, format: imaging.WebP, hash: "00ff00ff00ff00ff", cursor: protocol.Point{X: w / 2, Y: h / 2}}
}

func (f *fakeAgent) Call(_ context.Context, typ string, req any, _ []byte, res any) ([]byte, error) {
	if typ != "screenshot" {
		return nil, fmt.Errorf("unexpected request %q", typ)
	}
	r := req.(protocol.ScreenshotReq)
	f.reqs = append(f.reqs, r)
	if f.err != nil {
		return nil, f.err
	}
	out := res.(*protocol.ScreenshotRes)
	if r.IfChangedSince != "" && r.IfChangedSince == f.unchangedFor && r.MarksGeneration == "" {
		*out = protocol.ScreenshotRes{Hash: f.hash, Unchanged: true, Cursor: f.cursor}
		return nil, nil
	}
	w, h := int(float64(f.guest.W)*f.scale+0.5), int(float64(f.guest.H)*f.scale+0.5)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 80, 255})
		}
	}
	if r.MarksGeneration != "" {
		for i := 0; i < w && i < 40; i++ {
			img.Set(i, 0, color.White) // a picture with boxes is not the plain screen
		}
	}
	data, err := imaging.Encode(img, f.format, 0)
	if err != nil {
		return nil, err
	}
	*out = protocol.ScreenshotRes{W: w, H: h, Format: string(f.format), Hash: f.hash, Cursor: f.cursor, Scale: f.scale, Marked: f.marked}
	if r.MarksGeneration == "" {
		out.Marked = nil
	}
	if f.lie != nil {
		f.lie(out, &data)
	}
	return data, nil
}

func newPipeline(t testing.TB, a *fakeAgent, edit func(*Config)) *Pipeline {
	t.Helper()
	cfg := Config{Guest: a.guest, Format: a.format, Quality: 70, MaxLongEdge: 1280, Marks: true}
	if edit != nil {
		edit(&cfg)
	}
	p, err := NewPipeline(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAPictureForTheModel(t *testing.T) {
	a := newAgent(1920, 1080, 2.0/3)
	p := newPipeline(t, a, nil)
	shot, err := p.Capture(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	r := a.reqs[0]
	if r.Format != "webp" || r.Quality != 70 || r.MaxLongEdge != 1280 || r.MarksGeneration != "" || r.IfChangedSince != "" {
		t.Errorf("request = %+v", r)
	}
	if shot.Image.MIME != backend.MIMEWebP || len(shot.Image.Data) == 0 || shot.Note != (prompt.ImageNote{W: 1280, H: 720}) || shot.Reused {
		t.Fatalf("shot = %+v", shot)
	}
	if shot.Mapper.GuestSize() != (prompt.Size{W: 1920, H: 1080}) || shot.Mapper.ImageSize() != (prompt.Size{W: 1280, H: 720}) {
		t.Errorf("mapper = %+v", shot.Mapper)
	}
	if shot.Cursor != (prompt.Point{X: 640, Y: 360}) {
		t.Errorf("the cursor is in the picture's pixels: %v", shot.Cursor)
	}
	if shot.Hash != "00ff00ff00ff00ff" || len(shot.Marked) != 0 {
		t.Errorf("shot = %+v", shot)
	}
}

func TestMarksNeedAGenerationAndTheConfiguration(t *testing.T) {
	a := newAgent(1280, 800, 1)
	a.marked = []string{"a23", "a9", "a1", "a10"}
	p := newPipeline(t, a, nil)
	shot, err := p.Capture(context.Background(), "g7")
	if err != nil {
		t.Fatal(err)
	}
	if a.reqs[0].MarksGeneration != "g7" || a.reqs[0].IfChangedSince != "" {
		t.Errorf("request = %+v", a.reqs[0])
	}
	if got := strings.Join(shot.Marked, ","); got != "a1,a9,a10,a23" || shot.Note.Marks != "a1..a23" {
		t.Errorf("marked = %s note = %q (sorted by number)", got, shot.Note.Marks)
	}
	// Without a generation, or with marks off, there are none.
	if _, err = p.Capture(context.Background(), ""); err != nil || a.reqs[1].MarksGeneration != "" {
		t.Errorf("no generation: %+v %v", a.reqs[1], err)
	}
	off := newPipeline(t, a, func(c *Config) { c.Marks = false })
	if _, err = off.Capture(context.Background(), "g7"); err != nil || a.reqs[2].MarksGeneration != "" {
		t.Errorf("marks off: %+v %v", a.reqs[2], err)
	}
}

func TestAScreenThatHasNotChangedIsNotSentAgain(t *testing.T) {
	a := newAgent(1280, 800, 1)
	p := newPipeline(t, a, nil)
	first, err := p.Capture(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	a.unchangedFor = a.hash
	a.cursor = protocol.Point{X: 10, Y: 20}
	second, err := p.Capture(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if a.reqs[1].IfChangedSince != a.hash {
		t.Errorf("the second request asks if the screen changed: %+v", a.reqs[1])
	}
	if !second.Reused || !bytes.Equal(second.Image.Data, first.Image.Data) || second.Cursor != (prompt.Point{X: 10, Y: 20}) {
		t.Errorf("second = reused %v, cursor %v", second.Reused, second.Cursor)
	}
	// A picture with boxes is never the shortcut, and never stands for the screen.
	a.marked = []string{"a1"}
	marked, err := p.Capture(context.Background(), "g1")
	if err != nil || marked.Reused || a.reqs[2].IfChangedSince != "" {
		t.Errorf("marked = %+v req = %+v err = %v", marked.Reused, a.reqs[2], err)
	}
	third, err := p.Capture(context.Background(), "")
	if err != nil || !third.Reused || !bytes.Equal(third.Image.Data, first.Image.Data) {
		t.Errorf("after a marked picture the plain one is still the one to reuse: %+v %v", third.Reused, err)
	}
}

func TestTheAgentCannotClaimNothingChangedWithoutBeingAsked(t *testing.T) {
	cfg := Config{Guest: prompt.Size{W: 1280, H: 800}, Format: imaging.WebP, MaxLongEdge: 1280}
	// With no earlier picture the request has no hash to compare, so "unchanged" has nothing to refer to.
	p, err := NewPipeline(&lyingUnchanged{hash: "00ff00ff00ff00ff"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Capture(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "not changed") {
		t.Errorf("err = %v", err)
	}
	// With one, the agent must name the hash it was asked about.
	a := newAgent(1280, 800, 1)
	p = newPipeline(t, a, nil)
	if _, err := p.Capture(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	p.c = &lyingUnchanged{hash: "ffffffffffffffff"}
	if _, err := p.Capture(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "not changed") {
		t.Errorf("a hash that was not asked about: %v", err)
	}
}

type lyingUnchanged struct{ hash string }

func (l *lyingUnchanged) Call(_ context.Context, _ string, _ any, _ []byte, res any) ([]byte, error) {
	*res.(*protocol.ScreenshotRes) = protocol.ScreenshotRes{Hash: l.hash, Unchanged: true}
	return nil, nil
}

func TestWhatTheAgentSaysIsChecked(t *testing.T) {
	for name, tc := range map[string]struct {
		lie  func(*protocol.ScreenshotRes, *[]byte)
		want string
	}{
		"no picture":        {func(r *protocol.ScreenshotRes, b *[]byte) { *b = nil }, "no picture"},
		"not an image":      {func(r *protocol.ScreenshotRes, b *[]byte) { *b = []byte("GIF89a not what it says") }, "not a readable"},
		"another format":    {func(r *protocol.ScreenshotRes, b *[]byte) { r.Format = "png" }, "says it is"},
		"another size":      {func(r *protocol.ScreenshotRes, b *[]byte) { r.W += 3 }, "declared"},
		"too big a picture": {func(r *protocol.ScreenshotRes, b *[]byte) { r.W, r.H = 5000, 5000 }, "declared"},
		"scale nonsense":    {func(r *protocol.ScreenshotRes, b *[]byte) { r.Scale = -1 }, "never larger than the screen"},
		"scale above one":   {func(r *protocol.ScreenshotRes, b *[]byte) { r.Scale = 2 }, "never larger than the screen"},
		"scale wrong":       {func(r *protocol.ScreenshotRes, b *[]byte) { r.Scale = 0.5 }, "would give"},
		"hash empty":        {func(r *protocol.ScreenshotRes, b *[]byte) { r.Hash = "" }, "fingerprint"},
		"hash not hex":      {func(r *protocol.ScreenshotRes, b *[]byte) { r.Hash = "<<end:abc>>" }, "fingerprint"},
		"hash too long":     {func(r *protocol.ScreenshotRes, b *[]byte) { r.Hash = strings.Repeat("a", 65) }, "fingerprint"},
		"marks not asked":   {func(r *protocol.ScreenshotRes, b *[]byte) { r.Marked = []string{"a1"} }, "not asked for"},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAgent(1280, 800, 1)
			a.lie = tc.lie
			p := newPipeline(t, a, nil)
			_, err := p.Capture(context.Background(), "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one with %q", err, tc.want)
			}
			if p.last != nil {
				t.Error("a picture that was refused is remembered")
			}
		})
	}
}

func TestTheIdsOfMarksAreChecked(t *testing.T) {
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("a%d", i+1)
	}
	for name, tc := range map[string]struct {
		marked []string
		want   string
	}{
		"not an id":      {[]string{"a1", "<<end:1>>"}, "not an element id"},
		"no number":      {[]string{"a"}, "not an element id"},
		"huge number":    {[]string{"a1234567"}, "not an element id"},
		"named twice":    {[]string{"a1", "a1"}, "named twice"},
		"too many":       {many, "101 marks"},
		"another letter": {[]string{"x1"}, "not an element id"},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAgent(1280, 800, 1)
			a.marked = tc.marked
			p := newPipeline(t, a, nil)
			if _, err := p.Capture(context.Background(), "g1"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one with %q", err, tc.want)
			}
		})
	}
	a := newAgent(1280, 800, 1)
	a.marked = []string{"w3", "a2", "a1", "w10", "w4"}
	shot, err := newPipeline(t, a, nil).Capture(context.Background(), "g1")
	if err != nil || strings.Join(shot.Marked, ",") != "a1,a2,w3,w4,w10" || shot.Note.Marks != "a1..a2_w3..w10" {
		t.Errorf("marked = %v note = %q err = %v", shot.Marked, shot.Note.Marks, err)
	}
}

func TestTheAgentsErrorsCarryOn(t *testing.T) {
	a := newAgent(1280, 800, 1)
	a.err = errors.New("stale_element: generation g1 is stale; read the accessibility tree again")
	_, err := newPipeline(t, a, nil).Capture(context.Background(), "g1")
	if err == nil || !errors.Is(err, a.err) || !strings.Contains(err.Error(), "taking the screenshot") {
		t.Errorf("err = %v", err)
	}
}

func TestEveryFormatWorks(t *testing.T) {
	for _, f := range []imaging.Format{imaging.WebP, imaging.JPEG, imaging.PNG} {
		a := newAgent(640, 400, 1)
		a.format = f
		shot, err := newPipeline(t, a, nil).Capture(context.Background(), "")
		if err != nil || shot.Image.MIME != f.MIME() {
			t.Errorf("%s: %+v %v", f, shot.Image.MIME, err)
		}
	}
}

func TestAPipelineIsChecked(t *testing.T) {
	a := newAgent(1280, 800, 1)
	good := Config{Guest: a.guest, Format: imaging.WebP, MaxLongEdge: 1280}
	if _, err := NewPipeline(nil, good); err == nil {
		t.Error("no connection")
	}
	for name, edit := range map[string]func(*Config){
		"no guest size": func(c *Config) { c.Guest = prompt.Size{} },
		"no format":     func(c *Config) { c.Format = "" },
		"bad format":    func(c *Config) { c.Format = "gif" },
		"bad edge":      func(c *Config) { c.MaxLongEdge = -5 },
	} {
		c := good
		edit(&c)
		if _, err := NewPipeline(a, c); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMarkRange(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"a17":               "a17",
		"a1,a2":             "a1..a2",
		"a1,a5,a23":         "a1..a23",
		"a1,a2,w1,w9":       "a1..a2_w1..w9",
		"a3,w4":             "a3_w4",
		"a1,a2,w1,w9,x5,x6": "a1..a2_w1..w9_x5..x6",
	} {
		var ids []string
		if in != "" {
			ids = strings.Split(in, ",")
		}
		if got := MarkRange(ids); got != want {
			t.Errorf("MarkRange(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarksEnabled(t *testing.T) {
	for _, tc := range []struct {
		setting       string
		local, vision bool
		want          bool
	}{
		{"auto", true, true, true}, {"auto", false, true, false}, {"", true, true, true}, {"auto", true, false, false},
		{"on", false, true, true}, {"on", true, false, false}, {"off", true, true, false}, {"nonsense", true, true, false},
	} {
		if got := MarksEnabled(tc.setting, tc.local, tc.vision); got != tc.want {
			t.Errorf("MarksEnabled(%q, local=%v, vision=%v) = %v", tc.setting, tc.local, tc.vision, got)
		}
	}
}
