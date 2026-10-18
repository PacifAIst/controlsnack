// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
)

// ErrBlind means a text-only session has nothing to read the screen from: neither the accessibility tree nor recognized
// text could be had.
var ErrBlind = errors.New("neither the accessibility tree nor the recognized text of the screen could be read, and a model that cannot see pictures has nothing else to go by")

// ObserverConfig is how the observer looks at the screen each turn.
type ObserverConfig struct {
	Setup Setup
	// Guest is the size of the guest screen, from the handshake.
	Guest prompt.Size
	// Scope is the part of the desktop whose tree is read: active_window (the default), desktop or browser.
	Scope string
	// MaxNodes bounds the tree (the agent's own default when 0).
	MaxNodes int
	// TreeBudget and OCRBudget are how long the agent may spend reading the tree and the text. The session scales them by
	// the speed of the virtual machine: without hardware acceleration the same read takes several times as long.
	TreeBudget, OCRBudget time.Duration
	// OCRScale is how many times the screen is enlarged before it is read, 1 to 3 (the agent's default when 0).
	OCRScale int
}

// Observer reads what the model is shown of the screen each turn. With vision that is the picture (with the numbered boxes
// of the tree), the active window and the tree; in text-only mode it is the same without the picture, and the text
// recognized on the screen in its place (PLAN.md section 9.6).
type Observer struct {
	c    Caller
	cfg  ObserverConfig
	pipe *Pipeline
	text Mapper
}

// NewObserver makes an Observer. In text-only mode no pictures are made and the mapper is the identity.
func NewObserver(c Caller, cfg ObserverConfig) (*Observer, error) {
	if c == nil {
		return nil, errors.New("the observer needs a connection to the agent")
	}
	switch cfg.Scope {
	case "", "active_window", "desktop", "browser":
	default:
		return nil, fmt.Errorf("the scope %q is not active_window, desktop or browser", cfg.Scope)
	}
	if cfg.Scope == "" {
		cfg.Scope = "active_window"
	}
	o := &Observer{c: c, cfg: cfg}
	var err error
	if cfg.Setup.Vision {
		o.pipe, err = NewPipeline(c, Config{Guest: cfg.Guest, Format: cfg.Setup.ImageFormat, MaxLongEdge: cfg.Setup.ImageMax, Marks: cfg.Setup.Marks, CoordSpace: cfg.Setup.CoordSpace(prompt.Size{})})
	} else {
		o.text, err = Identity(cfg.Guest)
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// Seen is what one look at the screen found, in the coordinates the model works in.
type Seen struct {
	Mapper Mapper
	Cursor prompt.Point
	Active *prompt.Window
	// UI is nil when the tree could not be read; OCR is nil in vision mode and when the text could not be read.
	UI  *prompt.UITree
	OCR *prompt.OCRBlock
	// Shot is the picture, in vision mode.
	Shot *Shot
	// Notes say, for the model, what could not be read and why.
	Notes []string
	// Raw is what the agent said, in guest pixels and before the model's coordinate space was applied: for the parts of the
	// session that act on it, which judge and aim in the guest's own terms (the policy's hit test of a click, the place of
	// an element or a line of text that the model named by its id).
	Raw RawSeen

	mode, tools string
}

// codeOf is the protocol error code of an error the agent reported, and "" for any other error.
func codeOf(err error) string {
	var c interface{ ErrorCode() string }
	if errors.As(err, &c) {
		return c.ErrorCode()
	}
	return ""
}

// remoteNote makes a sentence for the model from an error the agent reported: the agent runs in the guest, so its words are
// cleaned like any text from it.
func remoteNote(what string, err error) string {
	return untrusted.Sanitize(textclean.Line(fmt.Sprintf("%s could not be read (%v)", what, err), 300))
}

// Look reads the screen. An error the agent reports for the tree, the text or a mark that went stale is not the end of the
// turn: what was read is returned with a note on what was not. An error of the connection itself, and a picture that cannot
// be had in vision mode, are.
func (o *Observer) Look(ctx context.Context) (Seen, error) {
	var info protocol.ScreenInfoRes
	if _, err := o.c.Call(ctx, protocol.TypeScreenInfo, struct{}{}, nil, &info); err != nil {
		return Seen{}, fmt.Errorf("reading the screen: %w", err)
	}
	var seen Seen
	seen.Raw.Info = info
	seen.mode, seen.tools = "text-only", o.cfg.Setup.Profile
	if o.cfg.Setup.Vision {
		seen.mode = "vision"
	}

	var tree protocol.A11yTreeRes
	treeOK := true
	req := protocol.A11yTreeReq{Scope: o.cfg.Scope, MaxNodes: o.cfg.MaxNodes, BudgetMS: int(o.cfg.TreeBudget.Milliseconds())}
	if _, err := o.c.Call(ctx, protocol.TypeA11yTree, req, nil, &tree); err != nil {
		if codeOf(err) == "" {
			return Seen{}, fmt.Errorf("reading the accessibility tree: %w", err)
		}
		treeOK = false
		seen.Notes = append(seen.Notes, remoteNote("The accessibility tree", err))
	}

	if o.cfg.Setup.Vision {
		generation := ""
		if treeOK {
			generation = tree.Generation
		}
		shot, err := o.pipe.Capture(ctx, generation)
		if err != nil && generation != "" && codeOf(err) == protocol.CodeStaleElement {
			// The tree was replaced between reading it and drawing on the picture: the picture without boxes still serves.
			seen.Notes = append(seen.Notes, "The numbered boxes could not be drawn because the accessibility tree changed; use the ids of the tree, or read it again.")
			shot, err = o.pipe.Capture(ctx, "")
		}
		if err != nil {
			return Seen{}, err
		}
		seen.Shot, seen.Mapper, seen.Cursor = &shot, shot.Mapper, shot.Cursor
	} else {
		seen.Mapper = o.text
		seen.Cursor = o.text.Cursor(info.Cursor)
		var res protocol.OCRRes
		oreq := protocol.OCRReq{BudgetMS: int(o.cfg.OCRBudget.Milliseconds()), Scale: o.cfg.OCRScale}
		if _, err := o.c.Call(ctx, protocol.TypeOCR, oreq, nil, &res); err != nil {
			if codeOf(err) == "" {
				return Seen{}, fmt.Errorf("reading the text of the screen: %w", err)
			}
			seen.Notes = append(seen.Notes, remoteNote("The text of the screen", err))
			if !treeOK {
				return Seen{}, ErrBlind
			}
		} else {
			seen.OCR = seen.Mapper.OCR(res)
			seen.Raw.OCR = &res
		}
	}

	seen.Active = seen.Mapper.Window(info.ActiveWindow)
	if treeOK {
		seen.UI = seen.Mapper.UITree(tree, o.cfg.Scope)
		seen.Raw.Tree = &tree
	}
	return seen, nil
}

// Fill puts what was seen into an observation: the size of the screen as the model knows it, the cursor, the active
// window, the tree, the screen text, the picture's description, the notes, and the mode and tool words of the header. The
// rest of the observation (turn, task, results, budget) is the session's.
func (s Seen) Fill(o *prompt.Observation) {
	o.Screen = s.Mapper.ImageSize()
	cur := s.Cursor
	o.Cursor = &cur
	o.Active, o.UI, o.OCR = s.Active, s.UI, s.OCR
	o.Mode, o.Tools = s.mode, s.tools
	o.Image = nil
	if s.Shot != nil {
		note := s.Shot.Note
		o.Image = &note
	}
	o.Notes = append(o.Notes, s.Notes...)
}

// RawSeen is the agent's own answers to one look. Tree and OCR are nil when they could not be read.
type RawSeen struct {
	Info protocol.ScreenInfoRes
	Tree *protocol.A11yTreeRes
	OCR  *protocol.OCRRes
}
