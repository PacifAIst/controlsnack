// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package protocol

// Message types. A request has the name below; its response is the name plus ".res". Anything the agent sends on
// its own uses ID 0 and the type "event".
const (
	TypeHello      = "hello"     // host -> agent, first frame
	TypeHelloAck   = "hello_ack" // agent -> host
	TypePing       = "ping"
	TypePong       = "pong"
	TypeCancel     = "cancel" // host -> agent: abort the in-flight request with this body.id
	TypeError      = "error"  // response to any request that failed
	TypeEvent      = "event"  // agent -> host, ID 0
	TypeScreenshot = "screenshot"
	TypeScreenInfo = "screen_info"
	TypeWindows    = "windows"
	TypeInput      = "input"
	TypeA11yTree   = "a11y_tree"
	TypeA11yAction = "a11y_action"
	TypeOCR        = "ocr"
	TypeExec       = "exec"
	TypeBrowser    = "browser"
	TypeWaitFor    = "wait_for"
	TypeFilePeek   = "file_peek"
)

// ResponseOf returns the response type of a request type: the name plus ".res", except for the heartbeat, which is
// answered with "pong" (PLAN.md section 5.1).
func ResponseOf(request string) string {
	if request == TypePing {
		return TypePong
	}
	return request + ".res"
}

// requestTypes are the request types the agent serves. hello and cancel are handled by the dispatcher itself.
var requestTypes = map[string]bool{
	TypePing: true, TypeScreenshot: true, TypeScreenInfo: true, TypeWindows: true, TypeInput: true, TypeA11yTree: true,
	TypeA11yAction: true, TypeOCR: true, TypeExec: true, TypeBrowser: true, TypeWaitFor: true, TypeFilePeek: true,
}

// IsRequest reports whether t is a request type the agent can be asked for.
func IsRequest(t string) bool { return requestTypes[t] }

// Known reports whether t is a type of this protocol version in either direction. Unknown types get an error
// response from the agent (PLAN.md section 5.1).
func Known(t string) bool {
	switch t {
	case TypeHello, TypeHelloAck, TypePong, TypeCancel, TypeError, TypeEvent:
		return true
	}
	if requestTypes[t] {
		return true
	}
	for r := range requestTypes {
		if t == ResponseOf(r) {
			return true
		}
	}
	return false
}

// Error codes carried by TypeError. They are the protocol-level subset of the tool error codes of PLAN.md section 5.3.
const (
	CodeUnknownType = "unknown_type"
	CodeInvalidArgs = "invalid_args"
	CodeNotFound    = "not_found"
	CodeTimeout     = "timeout"
	CodeUnsupported = "unsupported"
	CodeCanceled    = "canceled"
	CodeBusy        = "busy"
	CodeInternal    = "internal"
	// CodeStaleElement answers an accessibility action on an element that is no longer what the model was shown: its
	// generation is not one the agent keeps, or the element has gone or changed since.
	CodeStaleElement = "stale_element"
)

// ErrorBody is the body of a TypeError frame.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CancelBody is the body of a TypeCancel frame.
type CancelBody struct {
	ID uint64 `json:"id"`
}

// EventBody is the body of a TypeEvent frame.
type EventBody struct {
	Name string `json:"name"`
	Data string `json:"data,omitempty"`
}

// Point is a position in guest pixels.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Rect is x, y, width, height in guest pixels.
type Rect [4]int

// --- screen ---------------------------------------------------------------------------------------------------

// ScreenshotReq asks for a picture of the screen. The image comes back as the frame's blob.
type ScreenshotReq struct {
	Format          string `json:"format,omitempty"` // webp (default), jpeg, png
	Quality         int    `json:"quality,omitempty"`
	MaxLongEdge     int    `json:"max_long_edge,omitempty"`
	IfChangedSince  string `json:"if_changed_since,omitempty"` // hash from an earlier response
	MarksGeneration string `json:"marks_generation,omitempty"` // draw set-of-marks for this a11y generation (the picture is then always sent)
}

// ScreenshotRes describes the image in the blob, or says nothing changed (Unchanged, no blob).
type ScreenshotRes struct {
	W         int     `json:"w,omitempty"`
	H         int     `json:"h,omitempty"`
	Format    string  `json:"format,omitempty"`
	Hash      string  `json:"hash"`
	Cursor    Point   `json:"cursor"`
	Scale     float64 `json:"scale,omitempty"` // image pixels per guest pixel
	Unchanged bool    `json:"unchanged,omitempty"`
}

// Monitor is one display.
type Monitor struct {
	ID      int  `json:"id"`
	Bounds  Rect `json:"bounds"`
	Primary bool `json:"primary,omitempty"`
}

// Window is a top-level window as the window manager reports it.
type Window struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Class   string `json:"class,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Bounds  Rect   `json:"bounds"`
	Active  bool   `json:"active,omitempty"`
	Minimal bool   `json:"minimized,omitempty"`
}

// ScreenInfoRes is the response of a screen_info request.
type ScreenInfoRes struct {
	Monitors     []Monitor `json:"monitors"`
	Cursor       Point     `json:"cursor"`
	ActiveWindow *Window   `json:"active_window,omitempty"`
}

// WindowsReq lists windows or acts on one (op list, focus, maximize).
type WindowsReq struct {
	Op       string `json:"op"`
	WindowID string `json:"window_id,omitempty"`
}

// WindowsRes lists windows; for focus and maximize it is empty.
type WindowsRes struct {
	Windows []Window `json:"windows,omitempty"`
}

// --- input ----------------------------------------------------------------------------------------------------

// InputEvent is one low-level input action in guest pixels.
type InputEvent struct {
	Type   string `json:"type"`        // move, down, up, click, scroll, drag, key, text
	X      int    `json:"x,omitempty"` // move, down, up, click, scroll, drag (the start); down, up, click and drag move there first
	Y      int    `json:"y,omitempty"`
	Button string `json:"button,omitempty"` // left (default), right, middle
	Double bool   `json:"double,omitempty"` // click
	DX     int    `json:"dx,omitempty"`     // scroll notches
	DY     int    `json:"dy,omitempty"`
	Keys   string `json:"keys,omitempty"` // key: one xdotool-style combination, e.g. ctrl+l
	Repeat int    `json:"repeat,omitempty"`
	Text   string `json:"text,omitempty"`    // text
	DragTo *Point `json:"drag_to,omitempty"` // drag: press at x, y, move here in about ten steps over 200 ms, release
}

// InputReq is a batch of input events executed in order.
type InputReq struct {
	Events []InputEvent `json:"events"`
}

// InputRes acknowledges the batch with the screen hash afterwards.
type InputRes struct {
	ScreenHashAfter string `json:"screen_hash_after,omitempty"`
}

// --- accessibility and OCR -----------------------------------------------------------------------------------

// A11yNode is one element of the accessibility tree. IDs are valid only for the generation they came with.
type A11yNode struct {
	ID          string   `json:"id"` // a<n> (desktop), w<n> (web)
	Role        string   `json:"role"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Value       string   `json:"value,omitempty"`
	States      []string `json:"states,omitempty"`
	Bounds      Rect     `json:"bounds"`
	Actions     []string `json:"actions,omitempty"`
}

// A11yTreeReq asks for the accessibility tree.
type A11yTreeReq struct {
	Scope    string `json:"scope,omitempty"` // active_window (default), desktop, browser
	Query    string `json:"query,omitempty"`
	MaxNodes int    `json:"max_nodes,omitempty"`
	MaxDepth int    `json:"max_depth,omitempty"`
	// BudgetMS is how long the agent may spend reading (0 means its default). The host scales it by the speed of the
	// virtual machine: the same tree takes several times as long without hardware acceleration.
	BudgetMS int `json:"budget_ms,omitempty"`
}

// A11yTreeRes is the tree of one generation.
type A11yTreeRes struct {
	Generation string     `json:"generation"`
	Nodes      []A11yNode `json:"nodes"`
	Truncated  bool       `json:"truncated,omitempty"`
	// Note says why a tree is empty or partial in words a model can act on ("the application does not expose
	// accessibility information; use the screenshot").
	Note string `json:"note,omitempty"`
}

// A11yActionReq acts on an element of a given generation (op click, focus, set_text).
type A11yActionReq struct {
	ID         string `json:"id"`
	Generation string `json:"generation"`
	Op         string `json:"op"`
	Text       string `json:"text,omitempty"`
}

// A11yActionRes says how the action was done: "action:click" (the element's own action), "pointer" (a click at the
// element's center), "editable_text" (the text was set directly) or "keyboard" (select all and type).
type A11yActionRes struct {
	Method string `json:"method"`
}

// OCRReq runs text recognition on the screen or a region of it.
type OCRReq struct {
	Region *Rect  `json:"region,omitempty"`
	Lang   string `json:"lang,omitempty"`
	// BudgetMS is how long recognition may take (0 means the agent's default, 10 s). The host scales it by the speed of
	// the virtual machine.
	BudgetMS int `json:"budget_ms,omitempty"`
	// Scale is how many times the picture is enlarged before it is read, 1 to 3 (0 means the agent's default, 2). Text
	// of 12 pixels reads better at twice the size; a big or sharp text can be read at 1, in a fraction of the time.
	Scale int `json:"scale,omitempty"`
}

// OCRLine is one recognized line of text.
type OCRLine struct {
	ID     string `json:"id"` // o<n>
	Text   string `json:"text"`
	Bounds Rect   `json:"bounds"`
	Conf   int    `json:"conf"`
}

// OCRRes holds the recognized lines, in reading order. Bounds are guest pixels.
type OCRRes struct {
	Generation string    `json:"generation"`
	Lines      []OCRLine `json:"lines"`
	// Truncated is set when there was more text than the answer holds.
	Truncated bool `json:"truncated,omitempty"`
}

// --- exec, browser, waiting, files ----------------------------------------------------------------------------

// ExecReq runs a command line with bash as the guest user.
type ExecReq struct {
	Command   string `json:"command"`
	Cwd       string `json:"cwd,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	Stdin     string `json:"stdin,omitempty"`
	MaxOutput int    `json:"max_output,omitempty"`
}

// ExecRes is the outcome of a command.
type ExecRes struct {
	ExitCode   int    `json:"exit_code"`
	DurationMS int    `json:"duration_ms"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
}

// BrowserReq is a browser operation (open, navigate, back, forward, reload, click, type, read, ax_tree, tabs,
// switch_tab). Tabs are numbered from 1, as `tabs` lists them.
type BrowserReq struct {
	Op     string `json:"op"`
	URL    string `json:"url,omitempty"`
	Target string `json:"target,omitempty"` // click, type: an id (w12) from the last ax_tree, or a CSS selector
	Text   string `json:"text,omitempty"`
	Submit bool   `json:"submit,omitempty"`
	Tab    int    `json:"tab,omitempty"`
	// MaxNodes and Query narrow an ax_tree: at most this many nodes (1 to 400, default 150), only those with the query
	// (up to 100 characters) in their role, name, description or value.
	MaxNodes int    `json:"max_nodes,omitempty"`
	Query    string `json:"query,omitempty"`
}

// BrowserTab describes an open tab.
type BrowserTab struct {
	Index  int    `json:"index"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Active bool   `json:"active,omitempty"`
}

// BrowserRes carries what the operation produced; only the fields that apply are set.
type BrowserRes struct {
	Title      string       `json:"title,omitempty"`
	URL        string       `json:"url,omitempty"`
	Text       string       `json:"text,omitempty"`
	Tabs       []BrowserTab `json:"tabs,omitempty"`
	Generation string       `json:"generation,omitempty"`
	Nodes      []A11yNode   `json:"nodes,omitempty"`
	// Truncated is set when the text, or the list of nodes, was cut.
	Truncated bool `json:"truncated,omitempty"`
	// Note says why an answer is empty or short in words a model can act on.
	Note string `json:"note,omitempty"`
}

// WaitForReq waits inside the guest (condition seconds, screen_change, screen_idle, text_visible, window_title,
// element).
type WaitForReq struct {
	Condition string `json:"condition"`
	Value     string `json:"value,omitempty"`
	TimeoutMS int    `json:"timeout_ms"`
}

// WaitForRes says whether the condition was met.
type WaitForRes struct {
	Met      bool `json:"met"`
	WaitedMS int  `json:"waited_ms"`
}

// FilePeekReq asks for the start of a file, so the exec policy can read a script before it runs.
type FilePeekReq struct {
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes"`
}

// FilePeekRes describes the file; the head of the file is the frame's blob.
type FilePeekRes struct {
	Exists bool  `json:"exists"`
	Size   int64 `json:"size"`
}
