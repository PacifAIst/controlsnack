// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

// The tools (PLAN.md section 5.3), in the order the schema lists them.
const (
	ToolScreenshot     = "screenshot"
	ToolReadTree       = "read_accessibility_tree"
	ToolClickElement   = "click_element"
	ToolSetText        = "set_text"
	ToolReadScreenText = "read_screen_text"
	ToolExec           = "exec"
	ToolBrowser        = "browser"
	ToolMouse          = "mouse"
	ToolScroll         = "scroll"
	ToolKey            = "key"
	ToolTypeText       = "type_text"
	ToolWindow         = "window"
	ToolWaitFor        = "wait_for"
	ToolAskUser        = "ask_user"
	ToolDone           = "done"
)

// Values the arguments take. The schema files list the same values; a test compares the two.
var (
	Scopes         = []string{"active_window", "desktop", "browser"}
	Buttons        = []string{"left", "right", "middle"}
	BrowserOps     = []string{"open", "navigate", "back", "forward", "reload", "click", "type", "read", "ax_tree", "tabs", "switch_tab"}
	MouseOps       = []string{"move", "click", "double_click", "right_click", "middle_click", "drag", "down", "up"}
	WindowOps      = []string{"list", "focus", "maximize"}
	WaitConditions = []string{"seconds", "screen_change", "screen_idle", "text_visible", "window_title", "element"}
)

// Defaults of the arguments that have one, and limits that are checked in bytes (the schema counts characters).
const (
	DefaultScope    = "active_window"
	DefaultMaxNodes = 150
	DefaultButton   = "left"
	DefaultTimeoutS = 60
	DefaultRepeat   = 1

	MaxStdinBytes = 64 << 10
	MaxURLBytes   = 2048

	// MaxReasonRunes is the longest reason (the model's note on why it does an action).
	MaxReasonRunes = 300
)

// Args is the validated arguments of one tool: a pointer to one of the structs below. The field names of the JSON are
// the ones the schema files (schema/tools) use; a field without omitempty is a required argument.
type Args interface {
	// Tool is the name of the tool the arguments belong to.
	Tool() string
}

// ScreenshotArgs: none.
type ScreenshotArgs struct{}

// ReadTreeArgs are the arguments of read_accessibility_tree.
type ReadTreeArgs struct {
	Scope    string `json:"scope,omitempty"`
	Query    string `json:"query,omitempty"`
	MaxNodes int    `json:"max_nodes,omitempty"`
}

// ClickElementArgs are the arguments of click_element.
type ClickElementArgs struct {
	ID     string `json:"id"`
	Button string `json:"button,omitempty"`
	Double bool   `json:"double,omitempty"`
}

// SetTextArgs are the arguments of set_text.
type SetTextArgs struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Submit bool   `json:"submit,omitempty"`
}

// ReadScreenTextArgs are the arguments of read_screen_text. Region is [x, y, width, height] in the pixels of the
// image the model saw.
type ReadScreenTextArgs struct {
	Region *[4]int `json:"region,omitempty"`
}

// ExecArgs are the arguments of exec.
type ExecArgs struct {
	Command  string `json:"command"`
	Cwd      string `json:"cwd,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
	Stdin    string `json:"stdin,omitempty"`
}

// BrowserArgs are the arguments of browser.
type BrowserArgs struct {
	Op     string `json:"op"`
	URL    string `json:"url,omitempty"`
	Target string `json:"target,omitempty"`
	Text   string `json:"text,omitempty"`
	Submit bool   `json:"submit,omitempty"`
	Tab    int    `json:"tab,omitempty"`
}

// MouseArgs are the arguments of mouse; the positions are in the pixels of the image the model saw.
type MouseArgs struct {
	Op  string `json:"op"`
	X   int    `json:"x"`
	Y   int    `json:"y"`
	ToX *int   `json:"to_x,omitempty"`
	ToY *int   `json:"to_y,omitempty"`
}

// ScrollArgs are the arguments of scroll; the position is in the pixels of the image the model saw.
type ScrollArgs struct {
	X  int `json:"x"`
	Y  int `json:"y"`
	DY int `json:"dy"`
	DX int `json:"dx,omitempty"`
}

// KeyArgs are the arguments of key.
type KeyArgs struct {
	Keys   string `json:"keys"`
	Repeat int    `json:"repeat,omitempty"`
}

// TypeTextArgs are the arguments of type_text.
type TypeTextArgs struct {
	Text string `json:"text"`
}

// WindowArgs are the arguments of window.
type WindowArgs struct {
	Op       string `json:"op"`
	WindowID string `json:"window_id,omitempty"`
}

// WaitForArgs are the arguments of wait_for.
type WaitForArgs struct {
	Condition string  `json:"condition"`
	Value     string  `json:"value,omitempty"`
	TimeoutS  float64 `json:"timeout_s"`
}

// AskUserArgs are the arguments of ask_user.
type AskUserArgs struct {
	Question string `json:"question"`
}

// DoneArgs are the arguments of done.
type DoneArgs struct {
	Success bool   `json:"success"`
	Summary string `json:"summary"`
}

// Tool names of the argument structs.
func (*ScreenshotArgs) Tool() string     { return ToolScreenshot }
func (*ReadTreeArgs) Tool() string       { return ToolReadTree }
func (*ClickElementArgs) Tool() string   { return ToolClickElement }
func (*SetTextArgs) Tool() string        { return ToolSetText }
func (*ReadScreenTextArgs) Tool() string { return ToolReadScreenText }
func (*ExecArgs) Tool() string           { return ToolExec }
func (*BrowserArgs) Tool() string        { return ToolBrowser }
func (*MouseArgs) Tool() string          { return ToolMouse }
func (*ScrollArgs) Tool() string         { return ToolScroll }
func (*KeyArgs) Tool() string            { return ToolKey }
func (*TypeTextArgs) Tool() string       { return ToolTypeText }
func (*WindowArgs) Tool() string         { return ToolWindow }
func (*WaitForArgs) Tool() string        { return ToolWaitFor }
func (*AskUserArgs) Tool() string        { return ToolAskUser }
func (*DoneArgs) Tool() string           { return ToolDone }
func (a *ReadTreeArgs) applyDefaults() {
	a.Scope, a.MaxNodes = orStr(a.Scope, DefaultScope), orInt(a.MaxNodes, DefaultMaxNodes)
}
func (a *ClickElementArgs) applyDefaults() { a.Button = orStr(a.Button, DefaultButton) }
func (a *ExecArgs) applyDefaults()         { a.TimeoutS = orInt(a.TimeoutS, DefaultTimeoutS) }
func (a *KeyArgs) applyDefaults()          { a.Repeat = orInt(a.Repeat, DefaultRepeat) }

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orInt(n, def int) int {
	if n == 0 {
		return def
	}
	return n
}
