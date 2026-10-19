// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/a11y"
	"github.com/PacifAIst/controlsnack/internal/agent/server"
	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// maxTreeBytes bounds the encoded size of the nodes in one answer, well inside the protocol's header limit: names,
// descriptions and values are up to 1000 characters each and JSON may write a character in six bytes.
const maxTreeBytes = 700 << 10

// Bounds of the time a tree request may ask to spend reading.
const (
	minTreeBudgetMS = 100
	maxTreeBudgetMS = 30000
)

// A11y serves `a11y_tree` and `a11y_action` (PLAN.md section 5.1).
type A11y struct{ Service *a11y.Service }

// NewA11y returns the handlers over a bus. desktop finds the window that has the focus; in does the clicking and
// typing the accessibility interfaces cannot (it may be nil, and those actions then fail as unsupported).
func NewA11y(bus a11y.Bus, desktop Desktop, in *Input) A11y {
	var drv a11y.Driver
	if in != nil {
		drv = a11yDriver{in: in}
	}
	return A11y{Service: a11y.NewService(bus, a11yDesktop{desktop}, drv)}
}

// Tree answers `a11y_tree`: the elements of a scope, with ids for a new generation.
func (h A11y) Tree(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.A11yTreeReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	if r.BudgetMS != 0 && (r.BudgetMS < minTreeBudgetMS || r.BudgetMS > maxTreeBudgetMS) {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "budget_ms %d is outside %d..%d (0 means the default)", r.BudgetMS, minTreeBudgetMS, maxTreeBudgetMS)
	}
	res, err := h.Service.Tree(ctx, r.Scope, a11y.Options{MaxNodes: r.MaxNodes, MaxDepth: r.MaxDepth, Query: r.Query,
		Budget: time.Duration(r.BudgetMS) * time.Millisecond})
	if err != nil {
		return nil, nil, a11yError(ctx, err, "reading the accessibility tree")
	}
	nodes := make([]protocol.A11yNode, 0, len(res.Entries))
	for _, e := range res.Entries {
		nodes = append(nodes, protocol.A11yNode{ID: e.ID, Role: e.Role, Name: e.Name, Description: e.Description, Value: e.Value,
			States: e.States, Bounds: protocol.Rect{e.X, e.Y, e.W, e.H}, Actions: e.Actions})
	}
	out := protocol.A11yTreeRes{Generation: res.Generation, Truncated: res.Truncated, Note: res.Note}
	var cut bool
	if out.Nodes, cut = fitNodes(nodes, maxTreeBytes); cut {
		out.Truncated = true
	}
	return out, nil, nil
}

// Action answers `a11y_action`. The text of set_text is never put in a message or a log line.
func (h A11y) Action(ctx context.Context, req protocol.Frame) (any, []byte, error) {
	var r protocol.A11yActionReq
	if err := protocol.DecodeBody(req, &r); err != nil {
		return nil, nil, server.Fail(protocol.CodeInvalidArgs, "%v", err)
	}
	out, err := h.Service.Act(ctx, r.ID, r.Generation, r.Op, r.Text)
	if err != nil {
		return nil, nil, a11yError(ctx, err, "the accessibility action failed")
	}
	return protocol.A11yActionRes{Method: out.Method}, nil, nil
}

// fitNodes keeps the nodes, in order, that fit in the byte budget once encoded; cut says some were left out.
func fitNodes(nodes []protocol.A11yNode, budget int) (kept []protocol.A11yNode, cut bool) {
	used := 0
	for i, n := range nodes {
		b, err := json.Marshal(n)
		if err != nil {
			return nodes[:i], true
		}
		if used += len(b) + 1; used > budget {
			return nodes[:i], true
		}
	}
	return nodes, false
}

// a11yError turns what the service said into the protocol's error codes.
func a11yError(ctx context.Context, err error, what string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *a11y.Error
	if errors.As(err, &ae) {
		switch ae.Kind {
		case a11y.KindInvalid:
			return server.Fail(protocol.CodeInvalidArgs, "%s", ae.Msg)
		case a11y.KindNotFound:
			return server.Fail(protocol.CodeNotFound, "%s", ae.Msg)
		case a11y.KindStale:
			return server.Fail(protocol.CodeStaleElement, "%s", ae.Msg)
		case a11y.KindUnsupported:
			return server.Fail(protocol.CodeUnsupported, "%s", ae.Msg)
		}
		return server.Fail(protocol.CodeInternal, "%s", ae.Msg)
	}
	if errors.Is(err, a11y.ErrNoBus) || errors.Is(err, a11y.ErrUnsupported) {
		return server.Fail(protocol.CodeUnsupported, "accessibility is not available in this session: %v", err)
	}
	return server.Fail(protocol.CodeInternal, "%s: %v", what, err)
}

// a11yDesktop tells the service which window has the focus.
type a11yDesktop struct{ d Desktop }

func (a a11yDesktop) ActiveWindow(ctx context.Context) (*a11y.Window, error) {
	if a.d == nil {
		return nil, nil
	}
	w, err := a.d.ActiveWindow(ctx)
	if err != nil || w == nil {
		return nil, err
	}
	return &a11y.Window{PID: w.PID, Title: w.Title}, nil
}

// a11yDriver clicks and types through the input handler, so it has the same checks and the same way of releasing what it
// pressed.
type a11yDriver struct{ in *Input }

func (d a11yDriver) do(ctx context.Context, ev protocol.InputEvent) error {
	w, h, err := d.in.Device.Size()
	if err != nil {
		return err
	}
	if err := validateInput(protocol.InputReq{Events: []protocol.InputEvent{ev}}, w, h); err != nil {
		return err
	}
	if err := d.in.run(ctx, ev); err != nil {
		return err
	}
	return d.in.Device.Sync()
}

func (d a11yDriver) Click(ctx context.Context, x, y int) error {
	return d.do(ctx, protocol.InputEvent{Type: evClick, X: x, Y: y})
}

func (d a11yDriver) Keys(ctx context.Context, combo string) error {
	return d.do(ctx, protocol.InputEvent{Type: evKey, Keys: combo})
}

func (d a11yDriver) Type(ctx context.Context, text string) error {
	return d.do(ctx, protocol.InputEvent{Type: evText, Text: text})
}
