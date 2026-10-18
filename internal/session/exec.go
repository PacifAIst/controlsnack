// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/policy"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
)

// Agent is the host's end of the agent protocol (agentclient.Client).
type Agent interface {
	Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error)
}

// callMargin is how much longer than the time an action is allowed the host waits for the agent's answer: the agent enforces
// the time itself and says so, and the host's wait is only the net under it.
const callMargin = 15 * time.Second

// Done is what an action came to.
type Done struct {
	// Result is what the model is told. Its CallID and Action are filled in by the turn loop.
	Result prompt.Result
	// ScreenHash is the hash of the screen after an input, when the agent said; the loop uses it to see that the screen changed.
	ScreenHash string
}

// Executor does what the model's actions ask, through the agent. It decides nothing: whether an action may run is the
// dispatcher's business, before the executor is asked. It knows what the model was last shown, in the guest's own pixels,
// to turn an id or a position the model named into what the agent needs.
type Executor struct {
	agent Agent

	mu   sync.Mutex
	view vision.RawSeen
	// browserTree is the generation of the last browser tree: the w ids of click_element and set_text belong to it.
	browserNodes []protocol.A11yNode
}

// NewExecutor makes an Executor over the agent.
func NewExecutor(agent Agent) *Executor { return &Executor{agent: agent} }

// SetView tells the executor what the model is now looking at: the tree and the text as the agent said them, in guest pixels.
func (x *Executor) SetView(v vision.RawSeen) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.view = v
}

// ActiveWindow is the id and the class of the window that had the focus when the model last looked ("" when none had it).
func (x *Executor) ActiveWindow() (id, class string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if w := x.view.Info.ActiveWindow; w != nil {
		return w.ID, w.Class
	}
	return "", ""
}

// Snapshot is the accessibility tree the model last saw, in the form the policy judges clicks by (guest pixels). It is nil when
// there is none.
func (x *Executor) Snapshot() *policy.A11ySnapshot {
	x.mu.Lock()
	defer x.mu.Unlock()
	nodes := x.view.Tree
	if nodes == nil && len(x.browserNodes) == 0 {
		return nil
	}
	snap := &policy.A11ySnapshot{}
	add := func(list []protocol.A11yNode) {
		for _, n := range list {
			snap.Nodes = append(snap.Nodes, policy.A11yNode{ID: n.ID, Role: n.Role, Name: n.Name, Description: n.Description, Bounds: [4]int(n.Bounds), States: n.States, Actions: n.Actions})
		}
	}
	if nodes != nil {
		add(nodes.Nodes)
	}
	add(x.browserNodes)
	return snap
}

func (x *Executor) viewTree() *protocol.A11yTreeRes {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.view.Tree
}

func (x *Executor) viewOCR() *protocol.OCRRes {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.view.OCR
}

func (x *Executor) setView(f func(v *vision.RawSeen)) {
	x.mu.Lock()
	defer x.mu.Unlock()
	f(&x.view)
}

// ok and fail make results.
func ok(tool, detail string, parts ...prompt.Part) Done {
	return Done{Result: prompt.Result{Tool: tool, OK: true, Detail: detail, Parts: parts}}
}

func fail(tool, code, detail string) Done {
	return Done{Result: prompt.Result{Tool: tool, Code: code, Detail: detail}}
}

// guestWords makes a sentence out of text that came from the guest (an agent's error message): it is cleaned and made harmless
// like all guest text, because it will be read by the model.
func guestWords(s string) string { return untrusted.Sanitize(textclean.Line(s, 300)) }

// failure turns an error of a call into the result the model reads.
func failure(tool string, err error) Done {
	var remote *agentclient.RemoteError
	switch {
	case errors.Is(err, agentclient.ErrAgentDown):
		return fail(tool, actions.CodeAgentUnavail, "The guest agent is not reachable right now. Wait a moment and try again, or use another tool.")
	case errors.Is(err, context.DeadlineExceeded):
		return fail(tool, actions.CodeTimeout, "The action took too long and was stopped.")
	case errors.Is(err, context.Canceled):
		return fail(tool, actions.CodeInternal, "The action was stopped.")
	case errors.As(err, &remote):
		code := actions.CodeInternal
		switch remote.Code {
		case protocol.CodeNotFound:
			code = actions.CodeNotFound
		case protocol.CodeTimeout:
			code = actions.CodeTimeout
		case protocol.CodeStaleElement:
			code = actions.CodeStaleElement
		case protocol.CodeUnsupported:
			code = actions.CodeUnsupported
		case protocol.CodeInvalidArgs:
			code = actions.CodeInvalidArgs
		}
		return fail(tool, code, guestWords(remote.Message))
	}
	return fail(tool, actions.CodeInternal, guestWords(err.Error()))
}

// Do carries out one action. The action's coordinates must already be the guest's (vision.Mapper.Guest).
func (x *Executor) Do(ctx context.Context, a actions.Action) Done {
	tool := a.Tool
	switch args := a.Args.(type) {
	case *actions.ScreenshotArgs:
		return ok(tool, "The picture comes with the next observation.")
	case *actions.ExecArgs:
		return x.exec(ctx, tool, args)
	case *actions.ReadTreeArgs:
		return x.readTree(ctx, tool, args)
	case *actions.ClickElementArgs:
		return x.click(ctx, tool, args)
	case *actions.SetTextArgs:
		return x.setText(ctx, tool, args)
	case *actions.ReadScreenTextArgs:
		return x.readText(ctx, tool, args)
	case *actions.BrowserArgs:
		return x.browser(ctx, tool, args)
	case *actions.MouseArgs:
		return x.mouse(ctx, tool, args)
	case *actions.ScrollArgs:
		return x.input(ctx, tool, protocol.InputEvent{Type: "scroll", X: args.X, Y: args.Y, DX: args.DX, DY: args.DY},
			fmt.Sprintf("scrolled by %d at (%d, %d)", args.DY, args.X, args.Y))
	case *actions.KeyArgs:
		return x.input(ctx, tool, protocol.InputEvent{Type: "key", Keys: args.Keys, Repeat: args.Repeat}, "pressed "+guestWords(args.Keys))
	case *actions.TypeTextArgs:
		return x.input(ctx, tool, protocol.InputEvent{Type: "text", Text: args.Text}, fmt.Sprintf("typed %d characters", len([]rune(args.Text))))
	case *actions.WindowArgs:
		return x.window(ctx, tool, args)
	case *actions.WaitForArgs:
		return x.waitFor(ctx, tool, args)
	}
	return fail(tool, actions.CodeUnknownTool, "This tool is not one the session carries out itself.")
}

func (x *Executor) exec(ctx context.Context, tool string, a *actions.ExecArgs) Done {
	timeout := time.Duration(a.TimeoutS) * time.Second
	cctx, cancel := context.WithTimeout(ctx, timeout+callMargin)
	defer cancel()
	var res protocol.ExecRes
	if _, err := x.agent.Call(cctx, protocol.TypeExec, protocol.ExecReq{Command: a.Command, Cwd: a.Cwd, TimeoutMS: int(timeout.Milliseconds()), Stdin: a.Stdin}, nil, &res); err != nil {
		return failure(tool, err)
	}
	parts := []prompt.Part{}
	if res.Stdout != "" {
		parts = append(parts, prompt.Part{Source: "exec.stdout", Text: res.Stdout})
	}
	if res.Stderr != "" {
		parts = append(parts, prompt.Part{Source: "exec.stderr", Text: res.Stderr})
	}
	detail := fmt.Sprintf("exit=%d %dms", res.ExitCode, res.DurationMS)
	if res.Truncated {
		detail += " (output cut)"
	}
	d := Done{Result: prompt.Result{Tool: tool, Parts: parts, Detail: detail}}
	switch {
	case res.TimedOut:
		d.Result.Code, d.Result.Detail = actions.CodeTimeout, fmt.Sprintf("The command did not finish in %d s and was stopped (%dms).", a.TimeoutS, res.DurationMS)
	case res.ExitCode != 0:
		d.Result.Code = "exec_failed" // the command ran and failed: what it printed says why
	default:
		d.Result.OK = true
	}
	return d
}

// input sends one input event.
func (x *Executor) input(ctx context.Context, tool string, ev protocol.InputEvent, what string) Done {
	cctx, cancel := context.WithTimeout(ctx, callMargin)
	defer cancel()
	var res protocol.InputRes
	if _, err := x.agent.Call(cctx, protocol.TypeInput, protocol.InputReq{Events: []protocol.InputEvent{ev}}, nil, &res); err != nil {
		return failure(tool, err)
	}
	d := ok(tool, what)
	d.ScreenHash = res.ScreenHashAfter
	return d
}

func (x *Executor) mouse(ctx context.Context, tool string, a *actions.MouseArgs) Done {
	ev := protocol.InputEvent{X: a.X, Y: a.Y}
	switch a.Op {
	case "move":
		ev.Type = "move"
	case "click":
		ev.Type = "click"
	case "double_click":
		ev.Type, ev.Double = "click", true
	case "right_click":
		ev.Type, ev.Button = "click", "right"
	case "middle_click":
		ev.Type, ev.Button = "click", "middle"
	case "down":
		ev.Type = "down"
	case "up":
		ev.Type = "up"
	case "drag":
		if a.ToX == nil || a.ToY == nil {
			return fail(tool, actions.CodeInvalidArgs, "A drag needs to_x and to_y.")
		}
		ev.Type, ev.DragTo = "drag", &protocol.Point{X: *a.ToX, Y: *a.ToY}
	default:
		return fail(tool, actions.CodeInvalidArgs, "Unknown mouse op.")
	}
	what := fmt.Sprintf("%s at (%d, %d)", strings.ReplaceAll(a.Op, "_", " "), a.X, a.Y)
	if ev.DragTo != nil {
		what = fmt.Sprintf("dragged from (%d, %d) to (%d, %d)", a.X, a.Y, ev.DragTo.X, ev.DragTo.Y)
	}
	return x.input(ctx, tool, ev, what)
}

// center is the middle of a rectangle.
func center(r protocol.Rect) (int, int) { return r[0] + r[2]/2, r[1] + r[3]/2 }

func (x *Executor) click(ctx context.Context, tool string, a *actions.ClickElementArgs) Done {
	switch a.ID[0] {
	case 'w':
		return x.browser(ctx, tool, &actions.BrowserArgs{Op: "click", Target: a.ID})
	case 'o':
		res := x.viewOCR()
		if res != nil {
			for _, l := range res.Lines {
				if l.ID == a.ID {
					cx, cy := center(l.Bounds)
					return x.pointerClick(ctx, tool, a, cx, cy)
				}
			}
		}
		return fail(tool, actions.CodeNotFound, "There is no recognized text with that id in what you were shown.")
	}
	tree := x.viewTree()
	if tree == nil {
		return fail(tool, actions.CodeNotFound, "There is no accessibility tree to take that id from.")
	}
	var node *protocol.A11yNode
	for i := range tree.Nodes {
		if tree.Nodes[i].ID == a.ID {
			node = &tree.Nodes[i]
			break
		}
	}
	if node == nil {
		return fail(tool, actions.CodeNotFound, "There is no element with that id in the tree you were shown.")
	}
	if (a.Button == "" || a.Button == "left") && !a.Double {
		cctx, cancel := context.WithTimeout(ctx, callMargin)
		defer cancel()
		var res protocol.A11yActionRes
		if _, err := x.agent.Call(cctx, protocol.TypeA11yAction, protocol.A11yActionReq{ID: a.ID, Generation: tree.Generation, Op: "click"}, nil, &res); err != nil {
			return failure(tool, err)
		}
		return ok(tool, fmt.Sprintf("clicked %s (%s)", a.ID, guestWords(res.Method)))
	}
	cx, cy := center(node.Bounds) // another button, or a double click: only the pointer does those
	return x.pointerClick(ctx, tool, a, cx, cy)
}

func (x *Executor) pointerClick(ctx context.Context, tool string, a *actions.ClickElementArgs, cx, cy int) Done {
	ev := protocol.InputEvent{Type: "click", X: cx, Y: cy, Double: a.Double}
	if a.Button != "" && a.Button != "left" {
		ev.Button = a.Button
	}
	return x.input(ctx, tool, ev, fmt.Sprintf("clicked %s at (%d, %d)", a.ID, cx, cy))
}

func (x *Executor) setText(ctx context.Context, tool string, a *actions.SetTextArgs) Done {
	if a.ID[0] == 'w' {
		return x.browser(ctx, tool, &actions.BrowserArgs{Op: "type", Target: a.ID, Text: a.Text, Submit: a.Submit})
	}
	tree := x.viewTree()
	if tree == nil {
		return fail(tool, actions.CodeNotFound, "There is no accessibility tree to take that id from.")
	}
	cctx, cancel := context.WithTimeout(ctx, callMargin)
	defer cancel()
	var res protocol.A11yActionRes
	if _, err := x.agent.Call(cctx, protocol.TypeA11yAction, protocol.A11yActionReq{ID: a.ID, Generation: tree.Generation, Op: "set_text", Text: a.Text}, nil, &res); err != nil {
		return failure(tool, err)
	}
	detail := fmt.Sprintf("set the text of %s (%d characters, %s)", a.ID, len([]rune(a.Text)), guestWords(res.Method))
	if a.Submit {
		sub := x.input(ctx, tool, protocol.InputEvent{Type: "key", Keys: "Return"}, "")
		if !sub.Result.OK {
			sub.Result.Detail = detail + "; pressing Enter failed: " + sub.Result.Detail
			return sub
		}
		detail += " and pressed Enter"
		sub.Result.Detail = detail
		return sub
	}
	return ok(tool, detail)
}

func (x *Executor) readTree(ctx context.Context, tool string, a *actions.ReadTreeArgs) Done {
	cctx, cancel := context.WithTimeout(ctx, callMargin+treeWait)
	defer cancel()
	var res protocol.A11yTreeRes
	req := protocol.A11yTreeReq{Scope: a.Scope, Query: a.Query, MaxNodes: a.MaxNodes, BudgetMS: int(treeWait.Milliseconds())}
	if _, err := x.agent.Call(cctx, protocol.TypeA11yTree, req, nil, &res); err != nil {
		return failure(tool, err)
	}
	// What was just read is what the model's next ids are taken from.
	x.setView(func(v *vision.RawSeen) { v.Tree = &res })
	var lines []string
	for _, n := range res.Nodes {
		lines = append(lines, nodeLine(n))
	}
	detail := fmt.Sprintf("%d elements (generation %s)", len(res.Nodes), guestWords(res.Generation))
	if res.Truncated {
		detail += ", cut"
	}
	if res.Note != "" {
		detail += ". " + guestWords(res.Note)
	}
	if len(lines) == 0 {
		return ok(tool, detail)
	}
	return ok(tool, detail, prompt.Part{Source: "a11y.tree", Text: strings.Join(lines, "\n")})
}

// treeWait is how long the agent may read a tree when the model asks for one (the observer's budget is the session's).
const treeWait = 15 * time.Second

func nodeLine(n protocol.A11yNode) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s]", n.ID, n.Role)
	if n.Name != "" {
		fmt.Fprintf(&b, " %q", n.Name)
	}
	if n.Value != "" {
		fmt.Fprintf(&b, " value=%q", n.Value)
	}
	fmt.Fprintf(&b, " [%d,%d,%d,%d]", n.Bounds[0], n.Bounds[1], n.Bounds[2], n.Bounds[3])
	if len(n.States) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(n.States, ","))
	}
	return b.String()
}

func (x *Executor) readText(ctx context.Context, tool string, a *actions.ReadScreenTextArgs) Done {
	cctx, cancel := context.WithTimeout(ctx, callMargin+ocrWait)
	defer cancel()
	req := protocol.OCRReq{BudgetMS: int(ocrWait.Milliseconds())}
	if a.Region != nil {
		r := protocol.Rect(*a.Region)
		req.Region = &r
	}
	var res protocol.OCRRes
	if _, err := x.agent.Call(cctx, protocol.TypeOCR, req, nil, &res); err != nil {
		return failure(tool, err)
	}
	x.setView(func(v *vision.RawSeen) { v.OCR = &res })
	var lines []string
	for _, l := range res.Lines {
		lines = append(lines, fmt.Sprintf("%s %q [%d,%d,%d,%d]", l.ID, l.Text, l.Bounds[0], l.Bounds[1], l.Bounds[2], l.Bounds[3]))
	}
	detail := fmt.Sprintf("%d lines of text", len(res.Lines))
	if res.Truncated {
		detail += ", cut"
	}
	if len(lines) == 0 {
		return ok(tool, detail)
	}
	return ok(tool, detail, prompt.Part{Source: "screen.text", Text: strings.Join(lines, "\n")})
}

// ocrWait is how long the agent may spend recognizing text when the model asks for it.
const ocrWait = 20 * time.Second

func (x *Executor) browser(ctx context.Context, tool string, a *actions.BrowserArgs) Done {
	cctx, cancel := context.WithTimeout(ctx, callMargin+browserWait)
	defer cancel()
	var res protocol.BrowserRes
	req := protocol.BrowserReq{Op: a.Op, URL: a.URL, Target: a.Target, Text: a.Text, Submit: a.Submit, Tab: a.Tab}
	if _, err := x.agent.Call(cctx, protocol.TypeBrowser, req, nil, &res); err != nil {
		return failure(tool, err)
	}
	var parts []prompt.Part
	detail := "browser " + a.Op + " done"
	if res.Title != "" || res.URL != "" {
		parts = append(parts, prompt.Part{Source: "browser.page", Text: fmt.Sprintf("title: %s\nurl: %s", res.Title, res.URL)})
	}
	if res.Text != "" {
		parts = append(parts, prompt.Part{Source: "browser.text", Text: res.Text})
		detail = fmt.Sprintf("read %d characters", len([]rune(res.Text)))
	}
	if len(res.Tabs) > 0 {
		var lines []string
		for _, t := range res.Tabs {
			mark := ""
			if t.Active {
				mark = " (active)"
			}
			lines = append(lines, fmt.Sprintf("%d%s: %s — %s", t.Index, mark, t.Title, t.URL))
		}
		parts = append(parts, prompt.Part{Source: "browser.tabs", Text: strings.Join(lines, "\n")})
		detail = fmt.Sprintf("%d tabs", len(res.Tabs))
	}
	if len(res.Nodes) > 0 {
		var lines []string
		for _, n := range res.Nodes {
			lines = append(lines, nodeLine(n))
		}
		parts = append(parts, prompt.Part{Source: "browser.tree", Text: strings.Join(lines, "\n")})
		detail = fmt.Sprintf("%d elements of the page (generation %s)", len(res.Nodes), guestWords(res.Generation))
		x.mu.Lock()
		x.browserNodes = res.Nodes
		x.mu.Unlock()
	}
	if res.Truncated {
		detail += ", cut"
	}
	if res.Note != "" {
		detail += ". " + guestWords(res.Note)
	}
	return ok(tool, detail, parts...)
}

// browserWait is how long the host waits for a browser operation (loading a page can be slow, most of all without hardware
// acceleration).
const browserWait = 90 * time.Second

func (x *Executor) window(ctx context.Context, tool string, a *actions.WindowArgs) Done {
	cctx, cancel := context.WithTimeout(ctx, callMargin)
	defer cancel()
	var res protocol.WindowsRes
	if _, err := x.agent.Call(cctx, protocol.TypeWindows, protocol.WindowsReq{Op: a.Op, WindowID: a.WindowID}, nil, &res); err != nil {
		return failure(tool, err)
	}
	if a.Op != "list" {
		return ok(tool, a.Op+" "+guestWords(a.WindowID))
	}
	var lines []string
	for _, w := range res.Windows {
		lines = append(lines, fmt.Sprintf("%s %q class=%s [%d,%d,%d,%d]", w.ID, w.Title, w.Class, w.Bounds[0], w.Bounds[1], w.Bounds[2], w.Bounds[3]))
	}
	if len(lines) == 0 {
		return ok(tool, "no windows")
	}
	return ok(tool, fmt.Sprintf("%d windows", len(lines)), prompt.Part{Source: "windows", Text: strings.Join(lines, "\n")})
}

func (x *Executor) waitFor(ctx context.Context, tool string, a *actions.WaitForArgs) Done {
	timeout := time.Duration(a.TimeoutS * float64(time.Second))
	cctx, cancel := context.WithTimeout(ctx, timeout+callMargin)
	defer cancel()
	var res protocol.WaitForRes
	if _, err := x.agent.Call(cctx, protocol.TypeWaitFor, protocol.WaitForReq{Condition: a.Condition, Value: a.Value, TimeoutMS: int(timeout.Milliseconds())}, nil, &res); err != nil {
		return failure(tool, err)
	}
	if !res.Met {
		return fail(tool, actions.CodeTimeout, fmt.Sprintf("The condition (%s) was not met in %.0f s.", a.Condition, a.TimeoutS))
	}
	return ok(tool, fmt.Sprintf("%s met after %d ms", a.Condition, res.WaitedMS))
}
