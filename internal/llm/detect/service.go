// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/platform"
)

// Service is what `controlsnack llm` uses; all output goes to w.
type Service interface {
	// Probe tests the configured (or auto-detected) backend and model for vision, WebP and tool calling.
	Probe(ctx context.Context, w io.Writer, cfg config.Config) error
	// Models lists the models the backend offers.
	Models(ctx context.Context, w io.Writer, cfg config.Config) error
}

// NewService returns the service of the real machine: servers on the network, results cached under the user's cache
// directory (no cache when it cannot be located).
func NewService() Service {
	opts := Options{}
	if dirs, err := platform.DefaultDirs(); err == nil {
		opts.CacheDir = dirs.Cache
	}
	return NewServiceFor(New(opts))
}

// NewServiceFor returns the service of a detector; tests use it with fake servers.
func NewServiceFor(d *Detector) Service { return service{d: d} }

type service struct{ d *Detector }

// header says where the model is, and, when it is not on this machine, what that means.
func header(w io.Writer, t Target) {
	where := "this machine"
	if t.Remote() {
		where = "NOT this machine"
	}
	fmt.Fprintf(w, "Backend  %s at %s (%s)\n", t.Backend, textclean.Line(t.BaseURL, 200), where)
	if n := t.Notice(); n != "" {
		fmt.Fprintf(w, "         %s\n", n)
	}
}

func (s service) Models(ctx context.Context, w io.Writer, cfg config.Config) error {
	t, err := s.d.ResolveBackend(ctx, cfg)
	if err != nil {
		return err
	}
	header(w, t)
	models, err := s.d.Models(ctx, t)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		fmt.Fprintln(w, "\nThe backend offers no models: load or pull one.")
		return nil
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tVISION\tTOOLS\tCONTEXT\tLOADED")
	for _, m := range models {
		ctxLen := "?"
		if m.ContextLength > 0 {
			ctxLen = fmt.Sprint(m.ContextLength)
		}
		loaded := ""
		if m.Loaded {
			loaded = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.Name, yesNo(m.Vision, m.VisionKnown), yesNo(m.Tools, m.ToolsKnown), ctxLen, loaded)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if t.Model == "" {
		if m, ok := pickVision(models); ok {
			fmt.Fprintf(w, "\nWithout --model, a session would use %s.\n", m.Name)
		} else {
			fmt.Fprintln(w, "\nNo model reports vision: name one with --model (a model that cannot see runs in text-only mode).")
		}
	}
	return nil
}

func yesNo(v, known bool) string {
	switch {
	case v:
		return "yes"
	case known:
		return "no"
	}
	return "?"
}

func (s service) Probe(ctx context.Context, w io.Writer, cfg config.Config) error {
	t, _, err := s.d.Resolve(ctx, cfg)
	if err != nil {
		return err
	}
	header(w, t)
	fmt.Fprintf(w, "Model    %s\n\n", textclean.Line(t.Model, 120))
	caps, err := s.d.Probe(ctx, t, true)
	if err != nil {
		return err
	}
	writeReport(w, caps)
	if !caps.Usable() {
		return fmt.Errorf("%s cannot name its actions any of the ways a session needs (tool calls, JSON held to a schema, JSON asked for in the prompt): use another model", textclean.Line(caps.Model, 120))
	}
	return nil
}

// writeReport prints what a probe found and what a session would do with it.
func writeReport(w io.Writer, c Capabilities) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, ch := range c.Checks {
		state := "ok"
		switch {
		case ch.Skipped:
			state = "skipped"
		case !ch.OK:
			state = "FAILED"
		}
		took := ""
		if r := ch.Latency.Round(100 * time.Millisecond); r > 0 {
			took = r.String()
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", ch.Name, state, took, textclean.Line(ch.Detail, 120))
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	if c.ContextLength > 0 {
		fmt.Fprintf(w, "Context window   %d tokens\n", c.ContextLength)
	}
	if c.Vision {
		fmt.Fprintf(w, "Screenshots      %s\n", c.ImageFormat())
		fmt.Fprintf(w, "Coordinates      %s\n", coordsWords(c.Coords))
	} else {
		fmt.Fprintln(w, "Screenshots      none: the model does not see images, so a session runs in text-only mode (screen text instead of pictures)")
	}
	if mode, ok := c.Mode(); ok {
		fmt.Fprintf(w, "Actions          %s\n", modeWords(string(mode)))
	} else {
		fmt.Fprintln(w, "Actions          none of the ways worked")
	}
	if c.Cached {
		fmt.Fprintln(w, "(an earlier probe)")
	}
}

func modeWords(mode string) string {
	switch mode {
	case "native":
		return "native tool calls"
	case "structured":
		return "a JSON answer held to the schema by the server"
	}
	return "a JSON answer asked for in the prompt"
}

func coordsWords(coords string) string {
	if coords == CoordsRelative {
		return fmt.Sprintf("relative, 0 to %d across each axis (the model's own scale; positions are converted both ways)", RelativeScale)
	}
	return "pixels of the picture"
}
