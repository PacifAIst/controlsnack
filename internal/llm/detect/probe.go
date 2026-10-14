// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// ProbeVersion is the version of the checks. A cached result of another version is not used: the checks changed, so
// what they found may not hold.
const ProbeVersion = 1

// The checks of a probe, in the order they run.
const (
	CheckVision      = "vision"
	CheckWebP        = "webp"
	CheckNativeTools = "native_tools"
	CheckStructured  = "structured"
	CheckJSON        = "json"
)

// Check is the outcome of one question put to the model.
type Check struct {
	Name string
	// OK is whether the model did what was asked.
	OK bool
	// Skipped is set when the check was not made, and Detail says why (the backend already says there is no vision, the
	// backend has no way to hold the model to a schema).
	Skipped bool
	// Detail is one clean line: what the model answered, or why it did not do what was asked.
	Detail  string
	Latency time.Duration
}

// Capabilities is what a model can do, as the probe found out.
type Capabilities struct {
	Version int
	Backend string
	BaseURL string
	Model   string
	// Digest identifies the build of the model when the backend says (Ollama does): a new pull of the same name is probed again.
	Digest string
	// Vision is whether the model sees a screenshot; WebP whether it takes one as WebP (else JPEG is sent).
	Vision, WebP bool
	// NativeTools, Structured and JSONPrompt are the ways the model can name its actions.
	NativeTools, Structured, JSONPrompt bool
	Checks                              []Check
	// ContextLength is the model's context window in tokens, 0 when the backend does not say.
	ContextLength int
	ProbedAt      time.Time
	// Cached is set when the result was read from the cache and not found out now; it is not stored.
	Cached bool `json:"-"`
}

// Mode is the best way the model can name its actions: native tool calls, else a JSON answer held to the schema, else
// a JSON answer asked for in the prompt. It is false when it can do none of them: the model cannot drive a session.
func (c Capabilities) Mode() (backend.Mode, bool) {
	switch {
	case c.NativeTools:
		return backend.ModeNative, true
	case c.Structured:
		return backend.ModeStructured, true
	case c.JSONPrompt:
		return backend.ModeJSON, true
	}
	return "", false
}

// ImageFormat is the format to send screenshots in: WebP when the model took one, else JPEG.
func (c Capabilities) ImageFormat() imaging.Format {
	if c.WebP {
		return imaging.WebP
	}
	return imaging.JPEG
}

// Usable reports whether a session can run on the model: it names its actions some way.
func (c Capabilities) Usable() bool {
	_, ok := c.Mode()
	return ok
}

// probeSide is the side, in pixels, of the test picture.
const probeSide = 128

// probeImage is a white picture with a red square in the middle.
func probeImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, probeSide, probeSide))
	white, red := color.RGBA{R: 255, G: 255, B: 255, A: 255}, color.RGBA{R: 220, A: 255}
	for y := 0; y < probeSide; y++ {
		for x := 0; x < probeSide; x++ {
			c := white
			if x >= probeSide/4 && x < probeSide*3/4 && y >= probeSide/4 && y < probeSide*3/4 {
				c = red
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// The words of the probe's questions. They are exported so that a fake model server can recognize a probe among the
// requests it gets and answer as the capabilities it plays say (internal/llm/fakellm); a test there runs the real probe
// against it, so a change of these words that the fake does not follow is found.
const (
	// ProbeVisionQuestion goes with the test picture.
	ProbeVisionQuestion = `This picture is white with one colored square in its middle. What color is the colored square? Answer with only this JSON object: {"color":"<one word>"}`
	// ProbeDoneAsk is the native-call question.
	ProbeDoneAsk = `Call the done tool with success set to true and the summary "ok". Do nothing else.`
	// ProbeDoneAnswer is the shape asked for in the checks that do not use native calls.
	ProbeDoneAnswer = `Answer with only this JSON object and nothing else: {"thought":"checking","actions":[{"tool":"done","args":{"success":true,"summary":"ok"}}]}`
)

// probeDeadline bounds one question: the first one may have to load the model.
const probeDeadline = 10 * time.Minute

// Probe asks the model, with the smallest possible questions, what it can do: see a screenshot (PNG, then WebP), call a
// tool, answer in JSON held to a schema, answer in JSON asked for in the prompt. A question that fails because the
// server cannot be reached, refuses the key or does not know the model ends the probe with that error: it says nothing
// about the model. One the server rejects, or the model answers wrongly, is a capability the model does not have.
//
// The result is read from the cache when there is a fresh one of the same model and version, unless force is set;
// a probe that ran is written to it.
func (d *Detector) Probe(ctx context.Context, t Target, force bool) (Capabilities, error) {
	if t.Model == "" {
		return Capabilities{}, errors.New("there is no model to probe: name one with --model")
	}
	info, _ := d.modelInfo(ctx, t)
	if !force {
		if c, ok := d.cached(t, info.Digest); ok {
			return c, nil
		}
	}
	caps := Capabilities{
		Version: ProbeVersion, Backend: t.Backend, BaseURL: t.BaseURL, Model: t.Model, Digest: info.Digest,
		ContextLength: info.ContextLength, ProbedAt: d.opts.Now().UTC(),
	}
	client := d.opts.NewClient(string(t.Protocol))
	client.Backoff = nil
	client.First, client.Timeout = probeDeadline, probeDeadline
	b, err := d.backendWith(t, client)
	if err != nil {
		return Capabilities{}, err
	}
	set, err := actions.NewSet(actions.ProfileCompact)
	if err != nil {
		return Capabilities{}, err
	}
	p := prober{b: b, set: set, caps: &caps}
	if err := p.run(ctx, info, t.Protocol); err != nil {
		return Capabilities{}, err
	}
	// A model that can do nothing is not remembered: it may have been a bad moment, and a cached "cannot" would keep a
	// session from starting for two weeks.
	if caps.Usable() {
		d.store(t, caps)
	}
	return caps, nil
}

// modelInfo is what the backend says of the model, when it says anything.
func (d *Detector) modelInfo(ctx context.Context, t Target) (ModelInfo, bool) {
	models, err := d.Models(ctx, t)
	if err != nil {
		return ModelInfo{}, false
	}
	for _, m := range models {
		if m.Name == t.Model {
			return m, true
		}
	}
	return ModelInfo{}, false
}

type prober struct {
	b    backend.Backend
	set  *actions.Set
	caps *Capabilities
}

func (p *prober) run(ctx context.Context, info ModelInfo, proto Protocol) error {
	if err := p.vision(ctx, info); err != nil {
		return err
	}
	if err := p.native(ctx); err != nil {
		return err
	}
	if proto == ProtocolAnthropic {
		p.skip(CheckStructured, "the API has no way to hold a model to a schema")
	} else if err := p.structured(ctx); err != nil {
		return err
	}
	return p.json(ctx)
}

func (p *prober) skip(name, why string) {
	p.caps.Checks = append(p.caps.Checks, Check{Name: name, Skipped: true, Detail: why})
}

// ask makes one call. An error that says nothing about the model (it cannot be reached, the key is refused, the model
// is not there, the user stopped it) is returned; any other becomes the check's detail.
func (p *prober) ask(ctx context.Context, name string, req backend.Request) (backend.Response, Check, error) {
	ctx, cancel := context.WithTimeout(ctx, probeDeadline)
	defer cancel()
	start := time.Now()
	resp, err := p.b.Complete(ctx, req)
	c := Check{Name: name, Latency: time.Since(start)}
	if err != nil {
		var he *httpx.Error
		if !errors.As(err, &he) {
			return resp, c, err // the user stopped it, or our own deadline
		}
		switch he.Kind {
		case httpx.KindUnavailable, httpx.KindAuth, httpx.KindNotFound, httpx.KindTimeout, httpx.KindRateLimited:
			return resp, c, err
		}
		c.Detail = textclean.Line(he.Error(), 200)
	}
	return resp, c, nil
}

func (p *prober) add(c Check) { p.caps.Checks = append(p.caps.Checks, c) }

func answerOf(resp backend.Response) string {
	s := strings.TrimSpace(parse.StripReasoning(resp.Reply.Text))
	return textclean.Line(s, 120)
}

// vision asks what color the square of a PNG is, then of a WebP; a model that does not see the picture does not say red.
func (p *prober) vision(ctx context.Context, info ModelInfo) error {
	if info.VisionKnown && !info.Vision {
		p.caps.Checks = append(p.caps.Checks,
			Check{Name: CheckVision, Skipped: true, Detail: "the backend says the model does not take images"},
			Check{Name: CheckWebP, Skipped: true, Detail: "no vision"})
		return nil
	}
	img := probeImage()
	for _, step := range []struct {
		check  string
		format imaging.Format
	}{{CheckVision, imaging.PNG}, {CheckWebP, imaging.WebP}} {
		if step.check == CheckWebP && !p.caps.Vision {
			p.skip(CheckWebP, "no vision")
			break
		}
		data, err := imaging.Encode(img, step.format, 0)
		if err != nil {
			return fmt.Errorf("making the test picture: %w", err)
		}
		resp, c, err := p.ask(ctx, step.check, backend.Request{
			Messages: []backend.Message{{Role: backend.RoleUser, Text: ProbeVisionQuestion, Images: []backend.Image{{MIME: step.format.MIME(), Data: data}}}},
			Mode:     backend.ModeJSON,
		})
		if err != nil {
			return err
		}
		if c.Detail == "" {
			answer := answerOf(resp)
			c.Detail = "answered " + answer
			c.OK = strings.Contains(strings.ToLower(answer), "red")
		}
		p.add(c)
		if step.check == CheckVision {
			p.caps.Vision = c.OK
		} else {
			p.caps.WebP = c.OK
		}
	}
	return nil
}

// doneIn reports whether the reply holds the one action asked for: the done tool, successful.
func (p *prober) doneIn(resp backend.Response) (string, bool) {
	res := parse.Parse(p.set, resp.Reply, 1)
	if !res.Usable() {
		if res.Source != parse.SourceNone && len(res.Failures) > 0 && res.Failures[0].Err != nil {
			return textclean.Line(strings.TrimPrefix(res.Failures[0].Err.Error(), ": "), 160), false
		}
		return "no action in the answer: " + textclean.Line(answerOf(resp), 100), false
	}
	a := res.Actions[0].Action
	done, ok := a.Args.(*actions.DoneArgs)
	switch {
	case !ok:
		return fmt.Sprintf("called %s, not done", a.Tool), false
	case !done.Success:
		return "called done with success false, not true", false
	}
	return "called done as asked (" + string(res.Source) + ")", true
}

func (p *prober) native(ctx context.Context) error {
	resp, c, err := p.ask(ctx, CheckNativeTools, backend.Request{
		Messages: []backend.Message{{Role: backend.RoleUser, Text: ProbeDoneAsk}},
		Set:      p.set, Mode: backend.ModeNative, MaxActions: 1,
	})
	if err != nil {
		return err
	}
	if c.Detail == "" {
		// A tool call written as text is not a native call: the model may be able to write JSON, but the backend does not
		// pass its tools through.
		if len(resp.Reply.ToolCalls) == 0 {
			c.Detail = "no tool call in the answer: " + textclean.Line(answerOf(resp), 100)
		} else {
			c.Detail, c.OK = p.doneIn(resp)
		}
	}
	p.add(c)
	p.caps.NativeTools = c.OK
	return nil
}

func (p *prober) structured(ctx context.Context) error {
	resp, c, err := p.ask(ctx, CheckStructured, backend.Request{
		Messages: []backend.Message{{Role: backend.RoleUser, Text: ProbeDoneAnswer}},
		Set:      p.set, Mode: backend.ModeStructured, MaxActions: 1,
	})
	if err != nil {
		return err
	}
	if c.Detail == "" {
		c.Detail, c.OK = p.doneIn(resp)
	}
	p.add(c)
	p.caps.Structured = c.OK
	return nil
}

func (p *prober) json(ctx context.Context) error {
	resp, c, err := p.ask(ctx, CheckJSON, backend.Request{
		Messages: []backend.Message{{Role: backend.RoleUser, Text: ProbeDoneAnswer}},
		Mode:     backend.ModeJSON, MaxActions: 1,
	})
	if err != nil {
		return err
	}
	if c.Detail == "" {
		c.Detail, c.OK = p.doneIn(resp)
	}
	p.add(c)
	p.caps.JSONPrompt = c.OK
	return nil
}
