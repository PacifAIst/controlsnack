// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/safety/untrusted"
)

// Observation formats (--obs-format).
const (
	FormatText = "text"
	FormatJSON = "json"
)

// Codes of a result that was not an error of the action but of the answer around it: the action was valid and was left
// out, or was beyond the limit. The codes of the actions package name the others.
const (
	CodeNotRun  = "not_run"
	CodeDropped = "dropped"
)

// Point, Size and Rect are in the coordinate space of the image the model is given, not in guest pixels: the host
// rescales what the guest reports before it builds an observation (PLAN.md section 5.2). Rect is x, y, width, height.
type (
	Point struct{ X, Y int }
	Size  struct{ W, H int }
	Rect  [4]int
)

// Window is the active window.
type Window struct {
	// Class is the window class; Title the title. Both come from the guest.
	Class, Title string
	Bounds       Rect
}

// Node is an element of the accessibility tree.
type Node struct {
	ID, Role, Name, Description, Value string
	States, Actions                    []string
	Bounds                             Rect
}

// UITree is the accessibility tree of one generation.
type UITree struct {
	Generation string
	// Scope is active_window, desktop or browser.
	Scope string
	Nodes []Node
	// Total is how many nodes there were before the host cut some to fit the context; 0 means all of them are in Nodes.
	Total int
	// Truncated is set when the agent itself stopped before the end of the tree.
	Truncated bool
	// Note is the agent's words about an empty or partial tree.
	Note string
}

// OCRLine is a line of text recognized on the screen.
type OCRLine struct {
	ID, Text string
	Bounds   Rect
}

// OCRBlock is the text recognized on the screen, in reading order.
type OCRBlock struct {
	Generation string
	Lines      []OCRLine
	// Total is how many lines there were before the host cut some; 0 means all of them are in Lines.
	Total     int
	Truncated bool
}

// Part is a piece of what an action returned that the guest chose: command output, text of a page. It is fenced when
// rendered.
type Part struct {
	// Source labels it: exec.stdout, exec.stderr, browser.text, file.content.
	Source string
	Text   string
	// MaxBytes caps it (untrusted.DefaultMaxBytes when 0).
	MaxBytes int
}

// Result is what came of one action.
type Result struct {
	// CallID is the id of the native call it answers; empty when the model did not use native calls.
	CallID string
	Tool   string
	// Action is the action in one line (see Describe), for the summary of a turn that is no longer shown in full.
	Action string
	OK     bool
	// Code is why it failed: one of the codes of the actions package, CodeNotRun or CodeDropped.
	Code string
	// Detail is the host's words: "exit=0 31ms", or why the action failed. It is not guest text.
	Detail string
	Parts  []Part
}

// Budget is what is left of the session's limits.
type Budget struct {
	ActionsLeft, TurnsLeft int
	TimeLeft               time.Duration
}

// ImageNote describes the picture that goes with the observation.
type ImageNote struct {
	W, H int
	// Marks names the elements the numbered boxes mark ("a1..a23"); empty when the image has none.
	Marks string
}

// Observation is what the model receives each turn (PLAN.md section 5.2).
type Observation struct {
	Turn   int
	Screen Size
	Cursor *Point
	// Network is "disabled" or "enabled"; Mode "vision" or "text-only"; Tools "full" or "compact".
	Network, Mode, Tools string
	// Task is the user's task, carried in every full observation so that it is the last thing before the answer.
	Task   string
	Active *Window
	UI     *UITree
	OCR    *OCRBlock
	// Results of the actions of the last answer. A model that makes native calls gets them as tool messages instead.
	Results []Result
	// Notes are the host's words about the last answer (parse.Feedback: an action that was not valid, actions not run).
	Notes []string
	// UserMessages are what the person watching wrote since the last turn.
	UserMessages []string
	Budget       Budget
	Image        *ImageNote
}

// Limits cuts an observation to fit a budget; a zero field is no limit.
type Limits struct {
	MaxNodes     int
	MaxOCR       int
	MaxPartBytes int
}

// Renderer turns observations and results into the text the model reads.
type Renderer struct {
	Fencer *untrusted.Fencer
	// Format is FormatText (the default) or FormatJSON.
	Format string
}

const (
	maxTaskRunes    = 4000
	maxUserRunes    = 2000
	maxDetailRunes  = 400
	maxTitleBytes   = 400
	maxNameRunes    = 80
	maxValueRunes   = 120
	maxDescRunes    = 80
	maxOCRRunes     = 200
	maxNoteBytes    = 600
	maxNoteRunes    = 1500
	maxStates       = 8
	maxResultsShown = 40
)

// Observation renders a full observation.
func (r Renderer) Observation(o Observation, lim Limits) string {
	if r.Format == FormatJSON {
		return r.jsonObservation(o, lim)
	}
	return r.textObservation(o, lim)
}

// Compact is the observation of a turn that is no longer the current one: the header, the active window and the
// results, without the tree, the screen text, the task and the picture, which are out of date and cost the most.
func (o Observation) Compact() Observation {
	c := o
	c.UI, c.OCR, c.Image, c.Task = nil, nil, nil, ""
	return c
}

// fence wraps text that came from the guest. Text of several lines goes between lines of its own, as in the plan's
// example, so that the fence markers are never in the middle of a line of it; trailing line breaks go.
func (r Renderer) fence(source, text string, maxBytes int) string {
	text = strings.TrimRight(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text), "\n")
	if strings.Contains(text, "\n") {
		text = "\n" + text + "\n"
	}
	return r.Fencer.Fence(source, text, maxBytes)
}

// detailLine cleans the host's words about a result. They are not guest text, but they are made from what the guest's
// agent says went wrong, so they get the same cleaning: no marker can be made of them.
func detailLine(s string) string {
	return untrusted.Sanitize(textclean.Line(s, maxDetailRunes))
}

func token(s string, max int) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '.', c == ':':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= max {
			break
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func (r Rect) String() string { return fmt.Sprintf("[%d,%d,%d,%d]", r[0], r[1], r[2], r[3]) }

func (r Renderer) textObservation(o Observation, lim Limits) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TURN %d · screen %dx%d", o.Turn, o.Screen.W, o.Screen.H)
	if o.Cursor != nil {
		fmt.Fprintf(&b, " · cursor %d,%d", o.Cursor.X, o.Cursor.Y)
	}
	fmt.Fprintf(&b, " · network: %s · mode: %s · tools: %s\n", token(o.Network, 20), token(o.Mode, 20), token(o.Tools, 20))
	if o.Task != "" {
		fmt.Fprintf(&b, "TASK: %s\n", indent(textclean.Block(o.Task, maxTaskRunes)))
	}
	if o.Active != nil {
		fmt.Fprintf(&b, "ACTIVE WINDOW: %s %s\n%s\n", token(o.Active.Class, 40), o.Active.Bounds, r.fence("window.title", o.Active.Title, maxTitleBytes))
	} else {
		b.WriteString("ACTIVE WINDOW: none\n")
	}
	if o.UI != nil {
		r.textUI(&b, o.UI, o.Active, lim)
	}
	if o.OCR != nil {
		r.textOCR(&b, o.OCR, lim)
	}
	if len(o.Results) > 0 {
		b.WriteString("LAST RESULTS\n")
		b.WriteString(r.resultsText(o.Results, lim))
	}
	for _, n := range o.Notes {
		if n = untrusted.Sanitize(textclean.Block(n, maxNoteRunes)); n != "" {
			fmt.Fprintf(&b, "NOTE: %s\n", indent(n))
		}
	}
	for _, m := range o.UserMessages {
		if m = textclean.Block(m, maxUserRunes); m != "" {
			fmt.Fprintf(&b, "USER: %s\n", indent(m))
		}
	}
	fmt.Fprintf(&b, "BUDGET actions_left=%d turns_left=%d time_left=%s\n", o.Budget.ActionsLeft, o.Budget.TurnsLeft, durationWords(o.Budget.TimeLeft))
	if o.Image != nil {
		fmt.Fprintf(&b, "[image attached: %dx%d", o.Image.W, o.Image.H)
		if o.Image.Marks != "" {
			fmt.Fprintf(&b, ", marks for %s", token(o.Image.Marks, 40))
		}
		b.WriteString("]\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func durationWords(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// nodeLine is one element: id, role, name, bounds, then what is worth knowing of it.
func nodeLine(n Node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", token(n.ID, 16), token(n.Role, 40))
	if n.Name != "" {
		b.WriteByte(' ')
		b.WriteString(strconv.Quote(textclean.Line(n.Name, maxNameRunes)))
	} else if n.Description != "" {
		b.WriteString(" desc=")
		b.WriteString(strconv.Quote(textclean.Line(n.Description, maxDescRunes)))
	}
	b.WriteByte(' ')
	b.WriteString(n.Bounds.String())
	if n.Value != "" {
		b.WriteString(" value=")
		b.WriteString(strconv.Quote(textclean.Line(n.Value, maxValueRunes)))
	}
	if len(n.States) > 0 {
		b.WriteString(" states=")
		b.WriteString(joinTokens(n.States, maxStates))
	}
	if len(n.Actions) > 0 {
		b.WriteString(" actions=")
		b.WriteString(joinTokens(n.Actions, maxStates))
	}
	return b.String()
}

func joinTokens(items []string, max int) string {
	if len(items) > max {
		items = items[:max]
	}
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = token(s, 24)
	}
	return strings.Join(out, ",")
}

func (r Renderer) treeLines(t *UITree, active *Window, lim Limits) (lines []string, shown, total int) {
	total = t.Total
	if total < len(t.Nodes) {
		total = len(t.Nodes)
	}
	nodes := chooseNodes(t.Nodes, active, lim.MaxNodes)
	lines = make([]string, len(nodes))
	for i, n := range nodes {
		lines[i] = nodeLine(n)
	}
	return lines, len(nodes), total
}

func (r Renderer) textUI(b *strings.Builder, t *UITree, active *Window, lim Limits) {
	lines, shown, total := r.treeLines(t, active, lim)
	fmt.Fprintf(b, "UI generation %s · scope %s · %d/%d nodes", token(t.Generation, 24), token(t.Scope, 24), shown, total)
	if t.Truncated {
		b.WriteString(" · the tree was cut where it was read")
	}
	b.WriteByte('\n')
	if t.Note != "" {
		fmt.Fprintf(b, "%s\n", r.fence("ui.note", t.Note, maxNoteBytes))
	}
	if shown > 0 {
		fmt.Fprintf(b, "%s\n", r.fence("ui", strings.Join(lines, "\n"), 0))
	}
}

func ocrLine(l OCRLine) string {
	return fmt.Sprintf("%s %s %s", token(l.ID, 16), strconv.Quote(textclean.Line(l.Text, maxOCRRunes)), l.Bounds)
}

func (r Renderer) ocrLines(o *OCRBlock, lim Limits) (lines []string, total int) {
	total = o.Total
	if total < len(o.Lines) {
		total = len(o.Lines)
	}
	src := o.Lines
	if lim.MaxOCR > 0 && len(src) > lim.MaxOCR {
		src = src[:lim.MaxOCR]
	}
	lines = make([]string, len(src))
	for i, l := range src {
		lines[i] = ocrLine(l)
	}
	return lines, total
}

func (r Renderer) textOCR(b *strings.Builder, o *OCRBlock, lim Limits) {
	lines, total := r.ocrLines(o, lim)
	fmt.Fprintf(b, "OCR generation %s · %d/%d lines", token(o.Generation, 24), len(lines), total)
	if o.Truncated {
		b.WriteString(" · the text was cut where it was read")
	}
	b.WriteByte('\n')
	if len(lines) > 0 {
		fmt.Fprintf(b, "%s\n", r.fence("ocr", strings.Join(lines, "\n"), 0))
	}
}

// status is how a result says whether it worked.
func (res Result) status() string {
	switch {
	case res.OK:
		return "ok"
	case res.Code == CodeNotRun:
		return "not run"
	case res.Code == CodeDropped:
		return "dropped"
	case res.Code == "":
		return "error"
	}
	return "error " + token(res.Code, 40)
}

// ResultText renders one result for a tool message: the tool, how it went, the host's words and the guest's output
// fenced. n is its place among the results of the answer, from 1.
func (r Renderer) ResultText(res Result, n int, lim Limits) string {
	if r.Format == FormatJSON {
		return r.jsonResult(res, n, lim)
	}
	return r.textResult(res, n, lim)
}

func (r Renderer) textResult(res Result, n int, lim Limits) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d. %s %s", n, token(res.Tool, 40), res.status())
	if d := detailLine(res.Detail); d != "" {
		b.WriteByte(' ')
		b.WriteString(d)
	}
	for _, p := range res.Parts {
		if p.Text == "" {
			continue
		}
		max := p.MaxBytes
		if lim.MaxPartBytes > 0 && (max == 0 || max > lim.MaxPartBytes) {
			max = lim.MaxPartBytes
		}
		fmt.Fprintf(&b, "\n%s", r.fence(p.Source, p.Text, max))
	}
	return b.String()
}

// resultsText renders the results of an answer, one after another.
func (r Renderer) resultsText(results []Result, lim Limits) string {
	var b strings.Builder
	for i, res := range results {
		if i == maxResultsShown {
			fmt.Fprintf(&b, "(%d more results not shown)\n", len(results)-maxResultsShown)
			break
		}
		b.WriteString(r.textResult(res, i+1, lim))
		b.WriteByte('\n')
	}
	return b.String()
}

// --- the JSON form ------------------------------------------------------------------------------------------------

type jsonObservation struct {
	Turn         int          `json:"turn"`
	Screen       jsonSize     `json:"screen"`
	Cursor       *jsonPoint   `json:"cursor,omitempty"`
	Network      string       `json:"network"`
	Mode         string       `json:"mode"`
	Tools        string       `json:"tools"`
	Task         string       `json:"task,omitempty"`
	ActiveWindow *jsonWindow  `json:"active_window,omitempty"`
	UI           *jsonUI      `json:"ui,omitempty"`
	OCR          *jsonOCR     `json:"ocr,omitempty"`
	LastResults  []jsonResult `json:"last_results,omitempty"`
	Notes        []string     `json:"notes,omitempty"`
	UserMessages []string     `json:"user_messages,omitempty"`
	Budget       jsonBudget   `json:"budget"`
	Image        *jsonImage   `json:"image,omitempty"`
}

type (
	jsonSize struct {
		W int `json:"w"`
		H int `json:"h"`
	}
	jsonPoint struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	jsonWindow struct {
		Class  string `json:"class"`
		Bounds Rect   `json:"bounds"`
		Title  string `json:"title"`
	}
	jsonUI struct {
		Generation string `json:"generation"`
		Scope      string `json:"scope"`
		Shown      int    `json:"nodes_shown"`
		Total      int    `json:"nodes_total"`
		Cut        bool   `json:"cut,omitempty"`
		Note       string `json:"note,omitempty"`
		Nodes      string `json:"nodes,omitempty"`
	}
	jsonOCR struct {
		Generation string `json:"generation"`
		Shown      int    `json:"lines_shown"`
		Total      int    `json:"lines_total"`
		Cut        bool   `json:"cut,omitempty"`
		Lines      string `json:"lines,omitempty"`
	}
	jsonResult struct {
		N       int      `json:"n"`
		Tool    string   `json:"tool"`
		Status  string   `json:"status"`
		Detail  string   `json:"detail,omitempty"`
		Outputs []string `json:"outputs,omitempty"`
	}
	jsonBudget struct {
		ActionsLeft int `json:"actions_left"`
		TurnsLeft   int `json:"turns_left"`
		TimeLeftS   int `json:"time_left_s"`
	}
	jsonImage struct {
		W     int    `json:"w"`
		H     int    `json:"h"`
		Marks string `json:"marks,omitempty"`
	}
)

func encode(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // "<<untrusted" must stay as it is: the fence is what the model looks for
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r Renderer) jsonResult(res Result, n int, lim Limits) string {
	return encode(r.buildJSONResult(res, n, lim))
}

func (r Renderer) buildJSONResult(res Result, n int, lim Limits) jsonResult {
	jr := jsonResult{N: n, Tool: token(res.Tool, 40), Status: res.status(), Detail: detailLine(res.Detail)}
	for _, p := range res.Parts {
		if p.Text == "" {
			continue
		}
		max := p.MaxBytes
		if lim.MaxPartBytes > 0 && (max == 0 || max > lim.MaxPartBytes) {
			max = lim.MaxPartBytes
		}
		jr.Outputs = append(jr.Outputs, r.fence(p.Source, p.Text, max))
	}
	return jr
}

func (r Renderer) jsonObservation(o Observation, lim Limits) string {
	j := jsonObservation{
		Turn: o.Turn, Screen: jsonSize{o.Screen.W, o.Screen.H}, Network: token(o.Network, 20), Mode: token(o.Mode, 20), Tools: token(o.Tools, 20),
		Budget: jsonBudget{o.Budget.ActionsLeft, o.Budget.TurnsLeft, int(max(o.Budget.TimeLeft, 0).Seconds())},
	}
	if o.Cursor != nil {
		j.Cursor = &jsonPoint{o.Cursor.X, o.Cursor.Y}
	}
	if o.Task != "" {
		j.Task = textclean.Block(o.Task, maxTaskRunes)
	}
	if o.Active != nil {
		j.ActiveWindow = &jsonWindow{Class: token(o.Active.Class, 40), Bounds: o.Active.Bounds, Title: r.fence("window.title", o.Active.Title, maxTitleBytes)}
	}
	if o.UI != nil {
		lines, shown, total := r.treeLines(o.UI, o.Active, lim)
		ui := &jsonUI{Generation: token(o.UI.Generation, 24), Scope: token(o.UI.Scope, 24), Shown: shown, Total: total, Cut: o.UI.Truncated}
		if o.UI.Note != "" {
			ui.Note = r.fence("ui.note", o.UI.Note, maxNoteBytes)
		}
		if shown > 0 {
			ui.Nodes = r.fence("ui", strings.Join(lines, "\n"), 0)
		}
		j.UI = ui
	}
	if o.OCR != nil {
		lines, total := r.ocrLines(o.OCR, lim)
		ocr := &jsonOCR{Generation: token(o.OCR.Generation, 24), Shown: len(lines), Total: total, Cut: o.OCR.Truncated}
		if len(lines) > 0 {
			ocr.Lines = r.fence("ocr", strings.Join(lines, "\n"), 0)
		}
		j.OCR = ocr
	}
	for i, res := range o.Results {
		if i == maxResultsShown {
			break
		}
		j.LastResults = append(j.LastResults, r.buildJSONResult(res, i+1, lim))
	}
	for _, n := range o.Notes {
		if n = untrusted.Sanitize(textclean.Block(n, maxNoteRunes)); n != "" {
			j.Notes = append(j.Notes, n)
		}
	}
	for _, m := range o.UserMessages {
		if m = textclean.Block(m, maxUserRunes); m != "" {
			j.UserMessages = append(j.UserMessages, m)
		}
	}
	if o.Image != nil {
		img := &jsonImage{W: o.Image.W, H: o.Image.H}
		if o.Image.Marks != "" {
			img.Marks = token(o.Image.Marks, 40)
		}
		j.Image = img
	}
	return encode(j)
}

// chooseNodes keeps at most max nodes (all when max is 0), preferring what a model can act on: elements that have
// actions or a state that matters (focused, editable, checked...), those inside the active window, those with a name.
// The nodes kept stay in the order of the tree.
func chooseNodes(nodes []Node, active *Window, max int) []Node {
	if max <= 0 || len(nodes) <= max {
		return nodes
	}
	type scored struct {
		i, score int
	}
	order := make([]scored, len(nodes))
	for i, n := range nodes {
		s := 0
		if len(n.Actions) > 0 {
			s += 4
		}
		for _, st := range n.States {
			if st == "focused" || st == "editable" || st == "active" || st == "modal" {
				s += 3
				break
			}
		}
		if active != nil && inside(n.Bounds, active.Bounds) {
			s += 2
		}
		if n.Name != "" || n.Value != "" {
			s++
		}
		order[i] = scored{i, s}
	}
	// A stable selection of the top max: sort by score descending, then by position.
	sortScored := func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return a.i - b.i
	}
	sorted := append([]scored(nil), order...)
	slices.SortFunc(sorted, sortScored)
	keep := make(map[int]bool, max)
	for _, s := range sorted[:max] {
		keep[s.i] = true
	}
	out := make([]Node, 0, max)
	for i, n := range nodes {
		if keep[i] {
			out = append(out, n)
		}
	}
	return out
}

func inside(r, w Rect) bool {
	return r[0] >= w[0] && r[1] >= w[1] && r[0]+r[2] <= w[0]+w[2] && r[1]+r[3] <= w[1]+w[3]
}

// indent puts the lines after the first of a text that spans lines two spaces in: a line of a task or a message that
// looks like "BUDGET actions_left=999" is then plainly a part of that text, not a line of the observation.
func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }
