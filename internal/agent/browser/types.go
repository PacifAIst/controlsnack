// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"context"
	"errors"
	"fmt"
)

// Kind classifies an Error; the agent maps each to a protocol error code.
type Kind int

// Kinds of Error.
const (
	KindInvalid     Kind = iota // the request is wrong (bad operation, address, selector, text)
	KindNotFound                // no browser, no tab, no element matches
	KindStale                   // an id of a page or an element that has gone
	KindUnsupported             // the browser cannot be started or driven here
	KindTimeout                 // the page or the element did not come in time
	KindFailed                  // the browser refused
)

// Error is a failure the model can be told about.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func fail(k Kind, format string, args ...any) error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, args...)}
}

// Errors a Backend reports.
var (
	// ErrNoBrowser means the browser is not running (it was never opened, or it was closed).
	ErrNoBrowser = errors.New("the browser is not open")
	// ErrNodeGone means the element is not on the page any more: the page changed or was left.
	ErrNodeGone = errors.New("the element is no longer on the page")
	// ErrNoHistory means there is no page to go back or forward to.
	ErrNoHistory = errors.New("there is no page to go to")
	// ErrNoMatch means no element matches the selector.
	ErrNoMatch = errors.New("no element matches")
	// ErrBadSelector means the selector is not valid CSS.
	ErrBadSelector = errors.New("the selector is not valid")
	// ErrUnavailable means the browser program is not installed or would not start.
	ErrUnavailable = errors.New("the browser cannot be started")
)

// Tab is one page of the browser.
type Tab struct {
	ID     string
	Title  string
	URL    string
	Active bool
}

// AXNode is an element of a page's accessibility tree, as the backend reports it. Node is the browser's own number for
// the element (a backend DOM node id), valid while the page is.
type AXNode struct {
	Node        int64
	Role        string
	Name        string
	Description string
	Value       string
	States      []string
}

// Snapshot is the accessibility tree of the active tab, flattened in document order.
type Snapshot struct {
	Tab   string
	Nodes []AXNode
}

// Box is where an element is on the screen, in screen pixels (approximately: see the backend).
type Box struct{ X, Y, W, H int }

// Backend is the browser as the service uses it. The chromedp one drives Chromium; tests provide one in memory.
type Backend interface {
	// Tabs lists the open tabs in the browser's order. With no browser running it lists none.
	Tabs(ctx context.Context) ([]Tab, error)
	// Open opens url in a new tab, which becomes the active one, starting the browser if it is not running. An empty
	// url opens a blank page.
	Open(ctx context.Context, url string) error
	// Activate makes a tab the active one and brings it to the front.
	Activate(ctx context.Context, tab string) error

	// The rest act on the active tab (ErrNoBrowser when there is none).
	Navigate(ctx context.Context, url string) error
	// History goes back (delta -1) or forward (+1), or says ErrNoHistory.
	History(ctx context.Context, delta int) error
	Reload(ctx context.Context) error
	// Summary is the title and address of the page.
	Summary(ctx context.Context) (title, url string, err error)
	// VisibleText is the text a person can read on the page, at most max characters.
	VisibleText(ctx context.Context, max int) (string, error)
	// AXNodes is the page's accessibility tree, flattened, with the nodes worth reporting.
	AXNodes(ctx context.Context) (Snapshot, error)
	// Boxes says where elements are on the screen; elements that are not rendered are left out.
	Boxes(ctx context.Context, nodes []int64) (map[int64]Box, error)
	// Describe is an element's role and name now, or ErrNodeGone.
	Describe(ctx context.Context, node int64) (role, name string, err error)

	ClickNode(ctx context.Context, node int64) error
	ClickSelector(ctx context.Context, selector string) error
	// TypeNode puts text in an element that takes text, replacing what is there; submit then presses Enter.
	TypeNode(ctx context.Context, node int64, text string, submit bool) error
	TypeSelector(ctx context.Context, selector, text string, submit bool) error

	// Close ends the browser.
	Close() error
}
