// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build live

package live

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/detect"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
)

func settings(t *testing.T) config.Config {
	t.Helper()
	c := config.Defaults()
	c.LLM.Backend = os.Getenv("CS_LIVE_BACKEND")
	c.LLM.BaseURL = os.Getenv("CS_LIVE_BASE_URL")
	c.LLM.Model = os.Getenv("CS_LIVE_MODEL")
	c.LLM.APIKeyEnv = os.Getenv("CS_LIVE_API_KEY_ENV")
	if v := os.Getenv("CS_LIVE_NUM_CTX"); v != "" {
		fmt.Sscan(v, &c.LLM.NumCtx)
	}
	if v := os.Getenv("CS_LIVE_IMAGE_MAX"); v != "" {
		fmt.Sscan(v, &c.LLM.ImageMax)
	}
	if v := os.Getenv("CS_LIVE_MARKS"); v != "" {
		c.LLM.Marks = v
	}
	if v := os.Getenv("CS_LIVE_OBS_FORMAT"); v != "" {
		c.LLM.ObsFormat = v
	}
	if c.LLM.Backend == "" {
		t.Skip("CS_LIVE_BACKEND is not set: these tests need a real model server")
	}
	return c
}

// harness is a real model, set up the way a session sets it up, and a conversation with it.
type harness struct {
	t      *testing.T
	cfg    config.Config
	target detect.Target
	caps   detect.Capabilities
	setup  vision.Setup
	b      backend.Backend
	set    *actions.Set
	conv   *prompt.Conversation
	guest  prompt.Size
	task   string
	turns  int
}

// world is a small simulated desktop that answers a model's actions.
type world interface {
	// Observe returns what the screen shows now, in the pixels the model works in.
	Observe(h *harness) (obs prompt.Observation, img *backend.Image)
	// Do carries out one valid action.
	Do(a actions.Action) prompt.Result
	// Finished reports whether the model has said it is done.
	Finished() bool
	// Mapper converts between the screen and the picture the last observation showed.
	Mapper() vision.Mapper
}

func newHarness(t *testing.T, guest prompt.Size, task string) *harness {
	t.Helper()
	cfg := settings(t)
	d := detect.New(detect.Options{CacheDir: t.TempDir()})
	ctx := context.Background()
	target, _, err := d.Resolve(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := d.Probe(ctx, target, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range caps.Checks {
		t.Logf("probe %-13s ok=%-5v skipped=%-5v %6s  %s", c.Name, c.OK, c.Skipped, c.Latency.Round(100*time.Millisecond), c.Detail)
	}
	setup, err := vision.Plan(caps, target.Local, cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	if m := os.Getenv("CS_LIVE_MODE"); m != "" { // try a way of naming actions other than the best one the probe found
		setup.Mode = backend.Mode(m)
	}
	b, err := d.Backend(target)
	if err != nil {
		t.Fatal(err)
	}
	set, err := actions.NewSet(setup.Profile)
	if err != nil {
		t.Fatal(err)
	}
	fencer, err := untrusted.New()
	if err != nil {
		t.Fatal(err)
	}
	imgW, imgH := guest.W, guest.H
	if setup.Vision {
		imgW, imgH = scaled(guest, cfg.LLM.ImageMax)
	}
	sys, err := prompt.System(prompt.Params{Nonce: fencer.Nonce(), Width: imgW, Height: imgH, MaxActions: 3, Vision: setup.Vision, Marks: setup.Marks, Mode: setup.Mode, Set: set})
	if err != nil {
		t.Fatal(err)
	}
	window := caps.ContextLength
	if cfg.LLM.NumCtx > 0 && (window == 0 || cfg.LLM.NumCtx < window) {
		window = cfg.LLM.NumCtx
	}
	conv, err := prompt.NewConversation(sys.Text, prompt.Options{Backend: string(target.Protocol), Mode: setup.Mode, Set: set, Renderer: prompt.Renderer{Fencer: fencer, Format: cfg.LLM.ObsFormat}, ContextTokens: window})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s: vision=%v mode=%s tools=%s context=%d; the system prompt is %d tokens", target.Backend, target.Model, setup.Vision, setup.Mode, setup.Profile, window, prompt.EstimateTokens(sys.Text))
	for _, n := range setup.Notices {
		t.Logf("NOTICE: %s", n)
	}
	return &harness{t: t, cfg: cfg, target: target, caps: caps, setup: setup, b: b, set: set, conv: conv, guest: guest, task: task}
}

func scaled(g prompt.Size, maxEdge int) (int, int) {
	if maxEdge <= 0 || max(g.W, g.H) <= maxEdge {
		return g.W, g.H
	}
	f := float64(maxEdge) / float64(max(g.W, g.H))
	return int(float64(g.W)*f + 0.5), int(float64(g.H)*f + 0.5)
}

// run drives the model for up to maxTurns turns.
func (h *harness) run(w world, maxTurns int) {
	h.t.Helper()
	ctx := context.Background()
	native := h.setup.Mode == backend.ModeNative
	for turn := 1; turn <= maxTurns && !w.Finished(); turn++ {
		obs, img := w.Observe(h)
		obs.Turn, obs.Network, obs.Task = turn, "disabled", h.task
		obs.Budget = prompt.Budget{ActionsLeft: 40, TurnsLeft: maxTurns - turn + 1, TimeLeft: 20 * time.Minute}
		if h.setup.Vision {
			obs.Mode = "vision"
		} else {
			obs.Mode = "text-only"
		}
		obs.Tools = h.setup.Profile
		h.conv.Begin(obs, img)
		h.turns = turn

		var res parse.Result
		var resp backend.Response
		for repair := 0; ; repair++ {
			req, rep, err := h.conv.Request(3)
			if err != nil {
				h.t.Fatal(err)
			}
			begin := time.Now()
			resp, err = h.b.Complete(ctx, req)
			if err != nil {
				h.t.Fatalf("turn %d: %v", turn, err)
			}
			res = parse.Parse(h.set, resp.Reply, 3)
			h.t.Logf("turn %d (%d tokens in the request, %s, %d out): %s", turn, rep.Estimated, time.Since(begin).Round(100*time.Millisecond), resp.Usage.CompletionTokens, oneLine(resp.Reply.Text, 300))
			for _, c := range resp.Reply.ToolCalls {
				h.t.Logf("    call %s %s", c.Name, oneLine(string(c.Args), 200))
			}
			if err := h.conv.Assistant(resp.Reply.Text, resp.Reply.ToolCalls); err != nil {
				h.t.Fatal(err)
			}
			if res.Usable() {
				break
			}
			for _, f := range res.Failures {
				h.t.Logf("    NOT USABLE: %s", f.Err)
			}
			if repair >= parse.MaxRepairs {
				h.t.Fatalf("turn %d: no usable answer after %d repairs", turn, repair)
			}
			var failed []prompt.Result
			for _, o := range parse.Outcomes(resp.Reply.ToolCalls, res) {
				failed = append(failed, h.resultFor(w, o))
			}
			if len(resp.Reply.ToolCalls) > 0 {
				if err := h.conv.Results(failed, prompt.Limits{}); err != nil {
					h.t.Fatal(err)
				}
			}
			if err := h.conv.Notice(parse.RepairMessage(res, h.set, 3, native)); err != nil {
				h.t.Fatal(err)
			}
		}

		var results []prompt.Result
		if len(resp.Reply.ToolCalls) > 0 {
			for _, o := range parse.Outcomes(resp.Reply.ToolCalls, res) {
				results = append(results, h.resultFor(w, o))
			}
		} else {
			for _, it := range res.Actions {
				results = append(results, h.resultFor(w, parse.Outcome{Disposition: parse.DispValid, Item: &it}))
			}
		}
		for _, r := range results {
			h.t.Logf("    -> %s %s %s", r.Tool, status(r), r.Detail)
		}
		if note := parse.Feedback(res, 3); note != "" {
			h.conv.Note(note)
		}
		if err := h.conv.Results(results, prompt.Limits{}); err != nil {
			h.t.Fatal(err)
		}
	}
}

func status(r prompt.Result) string {
	if r.OK {
		return "ok"
	}
	return "error " + r.Code
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

// resultFor makes the result of one call: the world's for a valid one, the problems for an invalid one.
func (h *harness) resultFor(w world, o parse.Outcome) prompt.Result {
	r := prompt.Result{CallID: o.Call.ID, Tool: o.Call.Name}
	switch o.Disposition {
	case parse.DispValid:
		a := o.Item.Action
		g, err := w.Mapper().Guest(a)
		if err != nil {
			r.Code, r.Detail = actions.CodeInvalidArgs, err.Error()
			break
		}
		r = w.Do(g)
		r.CallID, r.Tool, r.Action = o.Call.ID, a.Tool, prompt.Describe(a)
	case parse.DispInvalid:
		r.Code, r.Detail = actions.CodeInvalidArgs, o.Failure.Err.Error()
	case parse.DispNotRun:
		r.Code, r.Detail = prompt.CodeNotRun, "an earlier action of the answer was not valid"
	case parse.DispDropped:
		r.Code, r.Detail = prompt.CodeDropped, "more actions than the limit in one answer"
	}
	return r
}

// --- drawing -----------------------------------------------------------------------------------------------------

type box struct {
	r     image.Rectangle
	label string
	fill  color.RGBA
}

func drawText(dst *image.RGBA, x, y int, s string, fg color.RGBA, scale int) {
	small := image.NewRGBA(image.Rect(0, 0, 7*len(s)+2, 15))
	d := font.Drawer{Dst: small, Src: image.NewUniform(fg), Face: basicfont.Face7x13, Dot: fixed.P(1, 11)}
	d.DrawString(s)
	big := imaging.Enlarge(small, scale)
	for yy := 0; yy < big.Bounds().Dy(); yy++ {
		for xx := 0; xx < big.Bounds().Dx(); xx++ {
			if c := big.RGBAAt(xx, yy); c.A > 0 && image.Pt(x+xx, y+yy).In(dst.Bounds()) {
				dst.SetRGBA(x+xx, y+yy, c)
			}
		}
	}
}

func fillRect(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if image.Pt(x, y).In(dst.Bounds()) {
				dst.SetRGBA(x, y, c)
			}
		}
	}
}

func (h *harness) encode(img *image.RGBA) *backend.Image {
	h.t.Helper()
	data, err := imaging.Encode(img, h.setup.ImageFormat, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return &backend.Image{MIME: h.setup.ImageFormat.MIME(), Data: data}
}

// --- scenario 1: a terminal ---------------------------------------------------------------------------------------

// terminal is a desktop with one terminal window. The model can run a command with exec, or type it into the terminal.
type terminal struct {
	mapper  vision.Mapper
	lines   []string
	typed   string
	done    bool
	success bool
	summary string
	ran     []string
}

const kernel = "6.12.14-amd64"
const ps1 = "cs@guest:~$ "

func newTerminal() *terminal { return &terminal{lines: []string{ps1}} }

func (w *terminal) Finished() bool { return w.done }

func (w *terminal) Mapper() vision.Mapper { return w.mapper }

func (w *terminal) Observe(h *harness) (prompt.Observation, *backend.Image) {
	size := h.guest
	mapper, _ := vision.Identity(h.guest)
	var img *backend.Image
	obs := prompt.Observation{Screen: size}
	if h.setup.Vision {
		iw, ih := scaled(h.guest, h.cfg.LLM.ImageMax)
		mapper, _ = vision.NewMapper(h.guest, prompt.Size{W: iw, H: ih})
		obs.Screen = prompt.Size{W: iw, H: ih}
		canvas := image.NewRGBA(image.Rect(0, 0, iw, ih))
		fillRect(canvas, canvas.Bounds(), color.RGBA{60, 90, 140, 255})
		win := mapper.Rect([4]int{100, 80, 1000, 600})
		fillRect(canvas, image.Rect(win[0], win[1], win[0]+win[2], win[1]+win[3]), color.RGBA{20, 20, 24, 255})
		fillRect(canvas, image.Rect(win[0], win[1], win[0]+win[2], win[1]+22), color.RGBA{200, 200, 205, 255})
		drawText(canvas, win[0]+8, win[1]+4, "Terminal", color.RGBA{20, 20, 20, 255}, 1)
		for i, l := range w.lines {
			drawText(canvas, win[0]+10, win[1]+34+i*30, l, color.RGBA{230, 230, 230, 255}, 2)
		}
		img = h.encode(canvas)
		obs.Image = &prompt.ImageNote{W: iw, H: ih}
	}
	cur := mapper.Point(500, 400)
	obs.Cursor = &cur
	w.mapper = mapper
	obs.Active = mapper.Window(&protocol.Window{Title: "Terminal", Class: "xfce4-terminal", Bounds: protocol.Rect{100, 80, 1000, 600}, Active: true})
	tree := []prompt.Node{
		{ID: "a1", Role: "frame", Name: "Terminal", Bounds: mapper.Rect([4]int{100, 80, 1000, 600}), States: []string{"active"}},
		{ID: "a2", Role: "menu", Name: "File", Bounds: mapper.Rect([4]int{105, 108, 40, 24}), Actions: []string{"Click"}},
		{ID: "a3", Role: "menu", Name: "Edit", Bounds: mapper.Rect([4]int{146, 108, 40, 24}), Actions: []string{"Click"}},
		{ID: "a4", Role: "terminal", Name: "Terminal", Value: strings.Join(w.lines, "\n"), Bounds: mapper.Rect([4]int{105, 136, 990, 540}), States: []string{"focused", "editable"}},
	}
	obs.UI = &prompt.UITree{Generation: "g1", Scope: "active_window", Nodes: tree}
	if !h.setup.Vision {
		ocr := &prompt.OCRBlock{Generation: "g1"}
		for i, l := range w.lines {
			ocr.Lines = append(ocr.Lines, prompt.OCRLine{ID: fmt.Sprintf("o%d", i+1), Text: l, Bounds: mapper.Rect([4]int{110, 140 + i*20, 7 * len(l), 14})})
		}
		obs.OCR = ocr
	}
	return obs, img
}

func (w *terminal) run(cmd string) string {
	w.ran = append(w.ran, cmd)
	if strings.Contains(cmd, "uname") {
		return kernel + "\n"
	}
	return ""
}

func (w *terminal) Do(a actions.Action) prompt.Result {
	switch args := a.Args.(type) {
	case *actions.ExecArgs:
		out := w.run(args.Command)
		return prompt.Result{OK: true, Detail: "exit=0 12ms", Parts: []prompt.Part{{Source: "exec.stdout", Text: out}}}
	case *actions.TypeTextArgs:
		w.typed += args.Text
		w.lines[len(w.lines)-1] += args.Text
		if strings.Contains(args.Text, "\n") {
			cmd := strings.TrimSpace(strings.TrimPrefix(w.lines[len(w.lines)-1], ps1))
			w.lines[len(w.lines)-1] = ps1 + cmd
			if out := w.run(cmd); out != "" {
				w.lines = append(w.lines, strings.TrimRight(out, "\n"))
			}
			w.lines = append(w.lines, ps1)
		}
		return prompt.Result{OK: true, Detail: "typed"}
	case *actions.KeyArgs:
		if strings.EqualFold(args.Keys, "Return") && strings.Contains(w.lines[len(w.lines)-1], "uname") {
			cmd := strings.TrimSpace(strings.TrimPrefix(w.lines[len(w.lines)-1], ps1))
			w.lines[len(w.lines)-1] = ps1 + cmd
			if out := w.run(cmd); out != "" {
				w.lines = append(w.lines, strings.TrimSpace(out))
			}
			w.lines = append(w.lines, ps1)
		}
		return prompt.Result{OK: true, Detail: "pressed " + args.Keys}
	case *actions.DoneArgs:
		w.done, w.success, w.summary = true, args.Success, args.Summary
		return prompt.Result{OK: true, Detail: "the task is over"}
	case *actions.AskUserArgs:
		return prompt.Result{OK: true, Detail: "the user says: go on without me"}
	}
	return prompt.Result{OK: true, Detail: "done"}
}

func TestLiveProbe(t *testing.T) {
	cfg := settings(t)
	d := detect.New(detect.Options{})
	target, _, err := d.Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := d.Probe(context.Background(), target, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range caps.Checks {
		t.Logf("%-13s ok=%-5v skipped=%-5v %6s  %s", c.Name, c.OK, c.Skipped, c.Latency.Round(100*time.Millisecond), c.Detail)
	}
	mode, ok := caps.Mode()
	t.Logf("vision=%v webp=%v mode=%v usable=%v context=%d", caps.Vision, caps.WebP, mode, ok, caps.ContextLength)
	if !ok {
		t.Fatal("the model cannot name its actions any way")
	}
}

// The model is asked for the kernel version in a terminal: exec is the way, typing into the terminal another.
func TestLiveKernelVersion(t *testing.T) {
	h := newHarness(t, prompt.Size{W: 1280, H: 800}, "Find out the version of the Linux kernel this computer runs, and report it with done.")
	w := newTerminal()
	h.run(w, 6)
	t.Logf("commands run: %q; done=%v success=%v summary=%q", w.ran, w.done, w.success, w.summary)
	if !w.done || !w.success || !strings.Contains(w.summary, "6.12.14") {
		t.Errorf("the model did not report the kernel version")
	}
}

// --- scenario 2: three buttons on a picture ------------------------------------------------------------------------

// buttons is a desktop with a dialog of three buttons. The model is asked to click one of them: by id, from the tree, or by
// where it sees it in the picture (with CS_LIVE_NOTREE=1 the tree is not offered, so only the picture tells).
type buttons struct {
	rects   map[string]image.Rectangle // name -> rectangle in guest pixels
	ids     map[string]string          // id -> name
	clicked []string
	via     []string
	done    bool
	summary string
	noTree  bool
	opened  bool
	mapper  vision.Mapper
}

func newButtons() *buttons {
	return &buttons{
		rects:  map[string]image.Rectangle{"Save": image.Rect(200, 360, 420, 440), "Open": image.Rect(520, 360, 740, 440), "Cancel": image.Rect(840, 360, 1060, 440)},
		ids:    map[string]string{"a2": "Save", "a3": "Open", "a4": "Cancel"},
		noTree: os.Getenv("CS_LIVE_NOTREE") != "",
	}
}

func (w *buttons) Finished() bool        { return w.done }
func (w *buttons) Mapper() vision.Mapper { return w.mapper }

func (w *buttons) Observe(h *harness) (prompt.Observation, *backend.Image) {
	mapper, _ := vision.Identity(h.guest)
	obs := prompt.Observation{Screen: h.guest}
	var img *backend.Image
	if h.setup.Vision {
		iw, ih := scaled(h.guest, h.cfg.LLM.ImageMax)
		mapper, _ = vision.NewMapper(h.guest, prompt.Size{W: iw, H: ih})
		obs.Screen = prompt.Size{W: iw, H: ih}
		canvas := image.NewRGBA(image.Rect(0, 0, iw, ih))
		fillRect(canvas, canvas.Bounds(), color.RGBA{70, 100, 150, 255})
		dlg := mapper.Rect([4]int{150, 250, 960, 300})
		fillRect(canvas, image.Rect(dlg[0], dlg[1], dlg[0]+dlg[2], dlg[1]+dlg[3]), color.RGBA{235, 235, 235, 255})
		drawText(canvas, dlg[0]+16, dlg[1]+14, "Choose what to do with the file", color.RGBA{20, 20, 20, 255}, 2)
		var marks []imaging.Mark
		names := []string{"Save", "Open", "Cancel"}
		if w.opened {
			names = nil
			drawText(canvas, dlg[0]+16, dlg[1]+120, "report.txt was opened", color.RGBA{20, 20, 20, 255}, 3)
		}
		for _, name := range names {
			g := w.rects[name]
			r := mapper.Rect([4]int{g.Min.X, g.Min.Y, g.Dx(), g.Dy()})
			fillRect(canvas, image.Rect(r[0], r[1], r[0]+r[2], r[1]+r[3]), color.RGBA{250, 250, 250, 255})
			fillRect(canvas, image.Rect(r[0], r[1], r[0]+r[2], r[1]+2), color.RGBA{90, 90, 90, 255})
			fillRect(canvas, image.Rect(r[0], r[1]+r[3]-2, r[0]+r[2], r[1]+r[3]), color.RGBA{90, 90, 90, 255})
			drawText(canvas, r[0]+r[2]/2-len(name)*10, r[1]+r[3]/2-12, name, color.RGBA{10, 10, 10, 255}, 3)
			for id, n := range w.ids {
				if n == name {
					marks = append(marks, imaging.Mark{Label: id[1:], X: r[0], Y: r[1], W: r[2], H: r[3]})
				}
			}
		}
		var shown image.Image = canvas
		if h.setup.Marks && !w.noTree {
			shown = imaging.DrawMarks(canvas, marks)
			obs.Image = &prompt.ImageNote{W: iw, H: ih, Marks: "a2..a4"}
		} else {
			obs.Image = &prompt.ImageNote{W: iw, H: ih}
		}
		data, err := imaging.Encode(shown, h.setup.ImageFormat, 0)
		if err != nil {
			h.t.Fatal(err)
		}
		img = &backend.Image{MIME: h.setup.ImageFormat.MIME(), Data: data}
	}
	w.mapper = mapper
	cur := mapper.Point(100, 100)
	obs.Cursor = &cur
	obs.Active = mapper.Window(&protocol.Window{Title: "Open or save", Class: "zenity", Bounds: protocol.Rect{150, 250, 960, 300}, Active: true})
	if !w.noTree && w.opened {
		obs.UI = &prompt.UITree{Generation: "g2", Scope: "active_window", Nodes: []prompt.Node{{ID: "a1", Role: "frame", Name: "report.txt", Bounds: mapper.Rect([4]int{150, 250, 960, 300}), States: []string{"active"}}}}
	} else if !w.noTree {
		nodes := []prompt.Node{{ID: "a1", Role: "dialog", Name: "Open or save", Bounds: mapper.Rect([4]int{150, 250, 960, 300}), States: []string{"active", "modal"}}}
		for _, id := range []string{"a2", "a3", "a4"} {
			g := w.rects[w.ids[id]]
			nodes = append(nodes, prompt.Node{ID: id, Role: "push button", Name: w.ids[id], Bounds: mapper.Rect([4]int{g.Min.X, g.Min.Y, g.Dx(), g.Dy()}), Actions: []string{"Click"}})
		}
		obs.UI = &prompt.UITree{Generation: "g1", Scope: "active_window", Nodes: nodes}
	}
	if !h.setup.Vision && w.opened {
		obs.OCR = &prompt.OCRBlock{Generation: "g2", Lines: []prompt.OCRLine{{ID: "o1", Text: "report.txt was opened", Bounds: mapper.Rect([4]int{166, 264, 600, 30})}}}
	} else if !h.setup.Vision {
		obs.OCR = &prompt.OCRBlock{Generation: "g1", Lines: []prompt.OCRLine{
			{ID: "o1", Text: "Choose what to do with the file", Bounds: mapper.Rect([4]int{166, 264, 600, 30})},
			{ID: "o2", Text: "Save", Bounds: mapper.Rect([4]int{280, 385, 80, 30})},
			{ID: "o3", Text: "Open", Bounds: mapper.Rect([4]int{600, 385, 80, 30})},
			{ID: "o4", Text: "Cancel", Bounds: mapper.Rect([4]int{920, 385, 100, 30})},
		}}
	}
	return obs, img
}

func (w *buttons) Do(a actions.Action) prompt.Result {
	switch args := a.Args.(type) {
	case *actions.ClickElementArgs:
		switch {
		case w.ids[args.ID] != "" && !w.noTree:
			w.click(w.ids[args.ID], "click_element "+args.ID)
			return prompt.Result{OK: true, Detail: "clicked " + args.ID}
		case strings.HasPrefix(args.ID, "o"):
			names := map[string]string{"o2": "Save", "o3": "Open", "o4": "Cancel"}
			if n := names[args.ID]; n != "" {
				w.click(n, "click_element "+args.ID)
				return prompt.Result{OK: true, Detail: "clicked " + args.ID}
			}
		}
		return prompt.Result{Code: actions.CodeNotFound, Detail: "no element " + args.ID + " in the last observation" + prompt.Hint(actions.CodeNotFound)}
	case *actions.MouseArgs:
		for name, r := range w.rects {
			if args.Op != "move" && image.Pt(args.X, args.Y).In(r) {
				w.click(name, fmt.Sprintf("mouse %s at %d,%d (screen)", args.Op, args.X, args.Y))
				return prompt.Result{OK: true, Detail: fmt.Sprintf("%s at %d,%d", args.Op, args.X, args.Y)}
			}
		}
		w.via = append(w.via, fmt.Sprintf("mouse %s at %d,%d (screen) hit nothing", args.Op, args.X, args.Y))
		return prompt.Result{OK: true, Detail: fmt.Sprintf("%s at %d,%d: nothing there", args.Op, args.X, args.Y)}
	case *actions.DoneArgs:
		w.done, w.summary = true, args.Summary
		return prompt.Result{OK: true, Detail: "the task is over"}
	}
	return prompt.Result{OK: true, Detail: "done"}
}

// The model is asked to click one of three buttons, which it can find in the tree, in the recognized text or in the picture.
func TestLiveClickTheOpenButton(t *testing.T) {
	h := newHarness(t, prompt.Size{W: 1280, H: 800}, `The dialog on the screen asks what to do with a file. Click its "Open" button, then report with done.`)
	w := newButtons()
	h.run(w, 6)
	t.Logf("clicked %v via %v; done=%v summary=%q", w.clicked, w.via, w.done, w.summary)
	if len(w.clicked) == 0 || w.clicked[len(w.clicked)-1] != "Open" || !w.done {
		t.Errorf("the model did not click Open and finish")
	}
	for _, c := range w.clicked {
		if c != "Open" {
			t.Errorf("the model clicked %s", c)
		}
	}
}

// click records a click on a button; opening the file closes the dialog.
func (w *buttons) click(name, via string) {
	w.clicked, w.via = append(w.clicked, name), append(w.via, via)
	if name == "Open" {
		w.opened = true
	}
}
