// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
)

// Defaults of the conversation.
const (
	// DefaultKeepTurns is how many turns before the current one stay in the conversation with their answers and results
	// (PLAN.md section 9.4); older turns shrink to a line each.
	DefaultKeepTurns = 6
	// DefaultContextTokens is the window assumed for a backend that does not say.
	DefaultContextTokens = 16384
	// budgetPercent is how much of the window the conversation may fill; the rest is for the answer.
	budgetPercent = 85

	messageOverhead = 4
	maxAssistantRns = 3000
	maxSummaryLines = 30
	maxSummaryRunes = 240
	minNodes        = 8
	minOCRLines     = 5
	minPartBytes    = 256
)

// Options are what a conversation needs to know about the session.
type Options struct {
	// Backend is the kind of backend ("ollama", "openai" or "anthropic"); it decides what a picture costs.
	Backend string
	// Mode is how the model names its actions; Set the tools of the session.
	Mode backend.Mode
	Set  *actions.Set
	// Renderer makes the text of observations (the fencer of the session and the observation format).
	Renderer Renderer
	// ContextTokens is the model's context window (DefaultContextTokens when 0).
	ContextTokens int
	// KeepTurns is how many past turns are kept in full (DefaultKeepTurns when 0).
	KeepTurns int
}

// Report says how a request was made to fit.
type Report struct {
	// Window is the model's context; Limit what the conversation may fill (85 % of it); Estimated what it fills.
	Window, Limit, Estimated int
	// Fixed is the part that does not shrink: the system prompt and the tool definitions the backend is sent.
	Fixed int
	// ImageTokens is what the current picture is counted as.
	ImageTokens int
	// KeptTurns past turns are in the request in full; DroppedTurns were left out (their one-line summaries remain).
	KeptTurns, DroppedTurns int
	// Nodes and OCRLines are how many of the current tree and screen text are in the request, of NodesTotal and OCRTotal.
	Nodes, NodesTotal, OCRLines, OCRTotal int
	// ResultsCut is set when the text of results was cut to fit.
	ResultsCut bool
}

// ContextError says the conversation does not fit the model's context even with everything optional taken out.
type ContextError struct{ Estimated, Limit, Window int }

func (e *ContextError) Error() string {
	return fmt.Sprintf("the conversation needs about %d tokens at the least and the model's context of %d tokens allows %d (%d%% of it, the rest is for the answer): give the model a larger context (--num-ctx for Ollama), use a model with a larger window, or shorten the task", e.Estimated, e.Window, e.Limit, budgetPercent)
}

// turn is one observation and everything said after it.
type turn struct {
	n    int
	obs  Observation
	img  *backend.Image
	msgs []backend.Message
	// results are the results of the actions of the answer, for the summary and (without native calls) the next observation.
	results []Result
	// summary is the turn in one line, made when the next turn begins.
	summary string
}

// Conversation is the history a model is shown: the system prompt, the last turns in full, a line for each older turn
// and the current observation with its picture, trimmed to fit the context (PLAN.md section 9.4).
type Conversation struct {
	system   string
	opts     Options
	fixed    int
	turns    []*turn
	pending  []Result // results to show in the next observation (a model that does not make native calls)
	notes    []string
	userMsgs []string
}

// NewConversation starts a conversation with a system prompt.
func NewConversation(system string, opts Options) (*Conversation, error) {
	if opts.Set == nil {
		return nil, errors.New("a conversation needs the tools of the session")
	}
	if opts.Renderer.Fencer == nil {
		return nil, errors.New("a conversation needs the session's fencer")
	}
	switch opts.Mode {
	case backend.ModeNative, backend.ModeStructured, backend.ModeJSON:
	default:
		return nil, fmt.Errorf("mode %q is not native, structured or json", opts.Mode)
	}
	if opts.ContextTokens <= 0 {
		opts.ContextTokens = DefaultContextTokens
	}
	if opts.KeepTurns <= 0 {
		opts.KeepTurns = DefaultKeepTurns
	}
	c := &Conversation{system: system, opts: opts}
	c.fixed = EstimateTokens(system) + messageOverhead
	tools, err := c.toolTokens()
	if err != nil {
		return nil, err
	}
	c.fixed += tools
	return c, nil
}

// toolTokens is what the backend is sent besides the messages: the tool definitions of native mode, or the schema of the
// answer of structured mode. JSON mode sends nothing more (the prompt describes the tools).
func (c *Conversation) toolTokens() (int, error) {
	var doc []byte
	var err error
	switch c.opts.Mode {
	case backend.ModeNative:
		doc, err = c.opts.Set.Render(actions.FormatOpenAI)
	case backend.ModeStructured:
		doc, err = c.opts.Set.Envelope(actions.DefaultMaxActions, true)
	default:
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("measuring the tool definitions: %w", err)
	}
	return EstimateTokens(string(doc)), nil
}

// Turn is the number of the current turn (0 before the first).
func (c *Conversation) Turn() int {
	if len(c.turns) == 0 {
		return 0
	}
	return c.turns[len(c.turns)-1].n
}

// Begin starts a turn with its observation and, in vision mode, the picture. The turn before it shrinks: its picture, tree
// and screen text go, and it becomes one line when it falls out of the turns kept. The results given with Results since
// the last turn are shown in this observation when the model does not make native calls, and the notes given with Note
// and the messages given with UserMessage are shown in it.
func (c *Conversation) Begin(obs Observation, img *backend.Image) {
	if last := c.last(); last != nil {
		last.img = nil
		last.summary = summarize(last.n, last.results)
	}
	if c.opts.Mode != backend.ModeNative && len(c.pending) > 0 {
		obs.Results = c.pending
	}
	obs.Notes = append(append([]string(nil), c.notes...), obs.Notes...)
	obs.UserMessages = append(append([]string(nil), c.userMsgs...), obs.UserMessages...)
	c.pending, c.notes, c.userMsgs = nil, nil, nil
	c.turns = append(c.turns, &turn{n: obs.Turn, obs: obs, img: img})
	c.release()
}

// release frees what turns that can never be shown in full again hold.
func (c *Conversation) release() {
	cut := len(c.turns) - 1 - c.opts.KeepTurns
	for i := 0; i < cut; i++ {
		t := c.turns[i]
		t.obs, t.msgs, t.results, t.img = Observation{}, nil, nil, nil
	}
}

func (c *Conversation) last() *turn {
	if len(c.turns) == 0 {
		return nil
	}
	return c.turns[len(c.turns)-1]
}

// Assistant records the model's answer: its words (without reasoning blocks, cut to a length that cannot swamp the
// context) and the native calls it made.
func (c *Conversation) Assistant(text string, calls []parse.ToolCall) error {
	t := c.last()
	if t == nil {
		return errors.New("there is no turn to answer yet")
	}
	text = textclean.Block(parse.StripReasoning(text), maxAssistantRns)
	t.msgs = append(t.msgs, backend.Message{Role: backend.RoleAssistant, Text: text, Calls: calls})
	return nil
}

// Results records what came of the actions of the last answer. A model that makes native calls gets a tool message for
// each call, in the order of the calls, so there must be one result for each (also for the actions that were invalid,
// not run or dropped: the APIs refuse a conversation where a call is left unanswered). For any other model they are shown
// in the next observation.
func (c *Conversation) Results(results []Result, lim Limits) error {
	t := c.last()
	if t == nil {
		return errors.New("there is no turn to record results for")
	}
	t.results = append(t.results, results...)
	if c.opts.Mode != backend.ModeNative {
		c.pending = append(c.pending, results...)
		return nil
	}
	var calls []parse.ToolCall
	for i := len(t.msgs) - 1; i >= 0; i-- {
		if t.msgs[i].Role == backend.RoleAssistant {
			calls = t.msgs[i].Calls
			break
		}
	}
	if len(calls) != len(results) {
		return fmt.Errorf("the last answer made %d tool calls and %d results were given: every call is answered", len(calls), len(results))
	}
	for i, res := range results {
		call := calls[i]
		if res.Tool != "" && call.Name != "" && res.Tool != call.Name {
			return fmt.Errorf("result %d is for %s but the call it answers is %s", i+1, res.Tool, call.Name)
		}
		t.msgs = append(t.msgs, backend.Message{Role: backend.RoleTool, CallID: call.ID, Tool: call.Name, Text: c.opts.Renderer.ResultText(res, i+1, lim)})
	}
	return nil
}

// Notice says something to the model now, in the middle of a turn: the message that asks it to answer again.
func (c *Conversation) Notice(text string) error {
	t := c.last()
	if t == nil {
		return errors.New("there is no turn to speak in")
	}
	t.msgs = append(t.msgs, backend.Message{Role: backend.RoleUser, Text: textclean.Block(text, 4000)})
	return nil
}

// Note queues a sentence for the next observation (Feedback: what was wrong with an action that was not run).
func (c *Conversation) Note(text string) {
	if text = strings.TrimSpace(text); text != "" {
		c.notes = append(c.notes, text)
	}
}

// UserMessage queues what the person watching wrote, for the next observation.
func (c *Conversation) UserMessage(text string) {
	if text = strings.TrimSpace(text); text != "" {
		c.userMsgs = append(c.userMsgs, text)
	}
}

// Describe writes an action in one line for the summary of a turn: the tool and its arguments as they were validated.
func Describe(a actions.Action) string {
	if len(a.Raw) == 0 || string(a.Raw) == "{}" || string(a.Raw) == "null" {
		return a.Tool
	}
	return a.Tool + " " + textclean.Line(string(a.Raw), 120)
}

// summarize writes a turn in one line: what was done and how it went.
func summarize(n int, results []Result) string {
	if len(results) == 0 {
		return fmt.Sprintf("Turn %d: no action was run.", n)
	}
	parts := make([]string, len(results))
	for i, res := range results {
		what := res.Action
		if what == "" {
			what = res.Tool
		}
		parts[i] = textclean.Line(what, 100) + " -> " + res.status()
	}
	return textclean.Line(fmt.Sprintf("Turn %d: %s", n, strings.Join(parts, "; ")), maxSummaryRunes)
}

// summaryText lists the turns that are not shown in full, oldest first.
func summaryText(turns []*turn) string {
	if len(turns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("EARLIER TURNS (one line each; their screens are not shown any more)\n")
	skip := 0
	if len(turns) > maxSummaryLines {
		skip = len(turns) - maxSummaryLines
		fmt.Fprintf(&b, "(%d earlier turns not listed)\n", skip)
	}
	for _, t := range turns[skip:] {
		s := t.summary
		if s == "" {
			s = summarize(t.n, t.results)
		}
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return b.String()
}

// Request makes the request for the model's next answer: the conversation trimmed to fit the context. The order of what
// is given up is the plan's: the oldest turns first, then nodes of the accessibility tree (those with actions, a state that
// matters or inside the active window stay), then lines of screen text, then the text of results.
func (c *Conversation) Request(maxActions int) (backend.Request, Report, error) {
	cur := c.last()
	if cur == nil {
		return backend.Request{}, Report{}, errors.New("there is no observation to answer yet")
	}
	window := c.opts.ContextTokens
	limit := window * budgetPercent / 100
	rep := Report{Window: window, Limit: limit, Fixed: c.fixed}
	if cur.img != nil {
		w, h := cur.obs.Screen.W, cur.obs.Screen.H
		if cur.obs.Image != nil {
			w, h = cur.obs.Image.W, cur.obs.Image.H
		}
		rep.ImageTokens = ImageTokens(c.opts.Backend, w, h)
	}
	past := c.turns[:len(c.turns)-1]
	keep := min(len(past), c.opts.KeepTurns)
	lim := Limits{}
	steps := trimSteps(cur.obs)
	for {
		msgs := c.build(keep, lim)
		est := c.fixed + rep.ImageTokens
		for _, m := range msgs[1:] { // the system message is in fixed
			est += messageTokens(m)
		}
		rep.Estimated = est
		if est <= limit {
			rep.KeptTurns, rep.DroppedTurns = keep, len(past)-keep
			rep.Nodes, rep.NodesTotal, rep.OCRLines, rep.OCRTotal = c.shown(cur.obs, lim)
			rep.ResultsCut = lim.MaxPartBytes > 0
			req := backend.Request{Messages: msgs, Set: c.opts.Set, Mode: c.opts.Mode, MaxActions: maxActions}
			if err := req.Validate(); err != nil {
				return backend.Request{}, rep, fmt.Errorf("the conversation is not one a backend takes: %w", err)
			}
			return req, rep, nil
		}
		if keep > 0 {
			keep--
			continue
		}
		next, ok := steps(lim)
		if !ok {
			return backend.Request{}, rep, &ContextError{Estimated: est, Limit: limit, Window: window}
		}
		lim = next
	}
}

// trimSteps returns the function that, given the limits so far, gives the next tighter ones, or false when nothing more
// can be taken out: nodes first (halved down to a few), then lines of screen text, then the text of results.
func trimSteps(o Observation) func(Limits) (Limits, bool) {
	nodes, lines := 0, 0
	if o.UI != nil {
		nodes = len(o.UI.Nodes)
	}
	if o.OCR != nil {
		lines = len(o.OCR.Lines)
	}
	return func(l Limits) (Limits, bool) {
		switch {
		case nodes > minNodes && (l.MaxNodes == 0 || l.MaxNodes > minNodes):
			cur := l.MaxNodes
			if cur == 0 {
				cur = nodes
			}
			l.MaxNodes = max(cur/2, minNodes)
		case lines > minOCRLines && (l.MaxOCR == 0 || l.MaxOCR > minOCRLines):
			cur := l.MaxOCR
			if cur == 0 {
				cur = lines
			}
			l.MaxOCR = max(cur/2, minOCRLines)
		case l.MaxPartBytes == 0 || l.MaxPartBytes > minPartBytes:
			cur := l.MaxPartBytes
			if cur == 0 {
				cur = untrusted.DefaultMaxBytes
			}
			l.MaxPartBytes = max(cur/2, minPartBytes)
		default:
			return l, false
		}
		return l, true
	}
}

func (c *Conversation) shown(o Observation, lim Limits) (nodes, nodesTotal, ocr, ocrTotal int) {
	if o.UI != nil {
		nodes = len(chooseNodes(o.UI.Nodes, o.Active, lim.MaxNodes))
		nodesTotal = max(o.UI.Total, len(o.UI.Nodes))
	}
	if o.OCR != nil {
		ocr = len(o.OCR.Lines)
		if lim.MaxOCR > 0 {
			ocr = min(ocr, lim.MaxOCR)
		}
		ocrTotal = max(o.OCR.Total, len(o.OCR.Lines))
	}
	return nodes, nodesTotal, ocr, ocrTotal
}

// build makes the messages with the last keep past turns in full and the current observation cut to lim.
func (c *Conversation) build(keep int, lim Limits) []backend.Message {
	r := c.opts.Renderer
	past := c.turns[:len(c.turns)-1]
	cur := c.turns[len(c.turns)-1]
	dropped := past[:len(past)-keep]
	kept := past[len(past)-keep:]
	summary := summaryText(dropped)

	msgs := []backend.Message{{Role: backend.RoleSystem, Text: c.system}}
	for i, t := range kept {
		text := r.Observation(t.obs.Compact(), Limits{})
		if i == 0 && summary != "" {
			text, summary = summary+text, ""
		}
		msgs = append(msgs, backend.Message{Role: backend.RoleUser, Text: text})
		msgs = append(msgs, t.msgs...)
	}
	text := r.Observation(cur.obs, lim)
	if summary != "" {
		text = summary + text
	}
	user := backend.Message{Role: backend.RoleUser, Text: text}
	if cur.img != nil {
		user.Images = []backend.Image{*cur.img}
	}
	msgs = append(msgs, user)
	msgs = append(msgs, cur.msgs...)
	return msgs
}

func messageTokens(m backend.Message) int {
	n := EstimateTokens(m.Text) + messageOverhead
	for _, call := range m.Calls {
		n += EstimateTokens(call.Name) + EstimateTokens(string(call.Args)) + messageOverhead
	}
	return n
}
