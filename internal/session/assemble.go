// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
	"github.com/PacifAIst/controlsnack-local/internal/safety/confirm"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/safety/policy"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
)

// Budgets for reading the screen's structure: the agent's own default is 1.5 s for the tree, and what is slow under software
// emulation (D-61) is slow for every read.
const (
	treeBudgetBase = 1500 * time.Millisecond
	ocrBudgetBase  = 3 * time.Second
	peekTimeout    = 10 * time.Second
	peekBytes      = 64 << 10
)

// Parts are what a session is built from: the model, the guest, and the places its words go.
type Parts struct {
	Cfg     config.Config
	Profile safety.Profile
	Model   Model
	// Agent is the connection to the agent; Monitor is QEMU's, for the fallback (nil: there is none).
	Agent   AgentLink
	Monitor Monitor
	// Guest is the size of the guest's screen; User the account the agent runs as; Capabilities what it announced.
	Guest        prompt.Size
	User         string
	Capabilities []string
	// SlowBy is how many times slower the guest is than a real machine (the accelerator's timeout factor; 4 under software
	// emulation).
	SlowBy int
	// Dir is the session's directory, where the fallback's screendumps are written.
	Dir string
	// Machine is the session's state machine, already made (the viewer hears of it from the start).
	Machine *Machine
	Viewer  Sink
	Audit   Recorder
	// Killed says whether the kill switch has fired; Fatal is called when the audit log can no longer be written.
	Killed func() bool
	Fatal  func(error)
	// Rules is the text of the user's policy file, if there is one.
	Rules []byte
	// Fencer fences the guest's words in what the model reads (a fresh one when nil).
	Fencer *untrusted.Fencer
}

// Session is everything a session is made of, joined.
type Session struct {
	Machine    *Machine
	Limits     *limits.Limits
	Broker     *confirm.Broker
	Console    *Console
	Dispatcher *Dispatcher
	Loop       *Loop
	Guards     *Guards
	Link       *Link
	Watchdog   *Watchdog
	Redactor   *secret.Redactor
	Conv       *prompt.Conversation
	// System is the system prompt the model is given, with the hashes the audit log records.
	System prompt.Rendered
}

// agentScripts lets the policy read a script in the guest before it runs (the agent's file_peek).
type agentScripts struct {
	link AgentCaller
	// timeout is how long a read may take (peekTimeout when zero).
	timeout time.Duration
}

// AgentCaller is the agent's protocol as the session calls it (the Link).
type AgentCaller interface {
	Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error)
}

// Peek implements policy.ScriptReader.
func (s agentScripts) Peek(ctx context.Context, path string, max int) ([]byte, int64, bool, error) {
	limit := s.timeout
	if limit <= 0 {
		limit = peekTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var res protocol.FilePeekRes
	head, err := s.link.Call(ctx, protocol.TypeFilePeek, protocol.FilePeekReq{Path: path, MaxBytes: min(max, peekBytes)}, nil, &res)
	if err != nil {
		return nil, 0, false, err
	}
	return head, res.Size, res.Exists, nil
}

var _ policy.ScriptReader = agentScripts{}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// pictureSize is the size of the picture the model is given of a screen: the screen's, cut to the longest side allowed.
func pictureSize(guest prompt.Size, maxLongEdge int) prompt.Size {
	long := max(guest.W, guest.H)
	if maxLongEdge <= 0 || long <= maxLongEdge {
		return guest
	}
	f := float64(maxLongEdge) / float64(long)
	return prompt.Size{W: max(1, int(float64(guest.W)*f+0.5)), H: max(1, int(float64(guest.H)*f+0.5))}
}

// Assemble joins the parts of a session: the rules and the budgets, the way to the guest (the agent, and QEMU's monitor behind
// it), what the model is told and how it is asked, the person's side, and the loop that goes round them.
func Assemble(p Parts) (*Session, error) {
	switch {
	case p.Machine == nil:
		return nil, errors.New("a session needs its state machine")
	case p.Agent == nil:
		return nil, errors.New("a session needs a way to the agent")
	case p.Model.Backend == nil:
		return nil, errors.New("a session needs a model")
	case p.Guest.W < 2 || p.Guest.H < 2:
		return nil, fmt.Errorf("a screen of %dx%d", p.Guest.W, p.Guest.H)
	}
	slow := max(1, p.SlowBy)
	s := &Session{Machine: p.Machine, Redactor: secret.NewRedactor(), Guards: NewGuards(0, 0)}
	if k := p.Model.Target.APIKey; k != "" {
		s.Redactor.AddSecret(k)
	}
	fencer := p.Fencer
	if fencer == nil {
		var err error
		if fencer, err = untrusted.New(); err != nil {
			return nil, err
		}
	}

	// The way to the guest.
	s.Link = &Link{Agent: p.Agent, Viewer: p.Viewer, Audit: p.Audit, Fatal: p.Fatal}
	if p.Monitor != nil {
		s.Link.Fallback = NewFallback(p.Monitor, p.Dir, p.Guest)
	}

	// The rules.
	var rs *policy.RuleSet
	var err error
	if len(p.Rules) > 0 {
		rs, err = policy.LoadRuleSet(p.Rules)
	} else {
		rs, err = policy.DefaultRuleSet()
	}
	if err != nil {
		return nil, fmt.Errorf("the policy rules: %w", err)
	}
	home := ""
	if p.User != "" {
		home = "/home/" + p.User
	}
	engine, err := policy.NewEngine(rs, policy.Options{NetEgress: p.Cfg.Net.Allow, User: p.User, Home: home, Cwd: cwdOf(home), Scripts: agentScripts{link: s.Link}})
	if err != nil {
		return nil, fmt.Errorf("the policy engine: %w", err)
	}
	s.Broker = confirm.New(confirm.Options{Timeout: p.Profile.ConfirmTimeout})
	s.Limits = limits.New(p.Profile, nil)

	// What the model is told, and how it is asked.
	set, err := actions.NewSet(p.Model.Setup.Profile)
	if err != nil {
		return nil, err
	}
	hasTree := has(p.Capabilities, protocol.CapA11y)
	if !hasTree {
		set = set.WithoutTree()
	}
	space := p.Guest
	if p.Model.Setup.Vision {
		space = p.Model.Setup.CoordSpace(pictureSize(p.Guest, p.Model.Setup.ImageMax))
	}
	maxActions := p.Profile.ActionsPerResponse
	var domains []string
	if p.Cfg.Net.Allow {
		domains = p.Cfg.Net.AllowDomains
	}
	s.System, err = prompt.System(prompt.Params{Nonce: fencer.Nonce(), Width: space.W, Height: space.H, MaxActions: maxActions, NetAllowed: p.Cfg.Net.Allow,
		NetDomains: domains, Vision: p.Model.Setup.Vision, Relative: p.Model.Setup.Relative(), NoTree: !hasTree, Marks: p.Model.Setup.Marks && hasTree,
		Mode: p.Model.Setup.Mode, Set: set})
	if err != nil {
		return nil, err
	}
	format := prompt.FormatText
	if p.Cfg.LLM.ObsFormat == prompt.FormatJSON {
		format = prompt.FormatJSON
	}
	window := p.Model.Caps.ContextLength
	if window <= 0 {
		window = p.Cfg.LLM.NumCtx
	}
	s.Conv, err = prompt.NewConversation(s.System.Text, prompt.Options{Backend: string(p.Model.Target.Protocol), Mode: p.Model.Setup.Mode, Set: set,
		Renderer: prompt.Renderer{Fencer: fencer, Format: format}, ContextTokens: window})
	if err != nil {
		return nil, err
	}
	observer, err := vision.NewObserver(s.Link, vision.ObserverConfig{Setup: p.Model.Setup, Guest: p.Guest, Scope: "active_window",
		TreeBudget: treeBudgetBase * time.Duration(slow), OCRBudget: ocrBudgetBase * time.Duration(slow)})
	if err != nil {
		return nil, err
	}

	// The doing.
	exec := NewExecutor(s.Link)
	s.Dispatcher = &Dispatcher{Exec: exec, Policy: engine, Profile: p.Profile, Broker: s.Broker, Limits: s.Limits, Audit: p.Audit, Viewer: p.Viewer,
		Machine: p.Machine, Killed: p.Killed, Redactor: s.Redactor}

	// The person's side.
	s.Console = &Console{Machine: p.Machine, Broker: s.Broker, Limits: s.Limits, Viewer: p.Viewer, Audit: p.Audit, Redactor: s.Redactor, Step: s.Dispatcher, Fatal: p.Fatal}

	network := "disabled"
	if p.Cfg.Net.Allow {
		network = "enabled"
	}
	s.Loop = &Loop{Machine: p.Machine, Limits: s.Limits, Look: observer, Conv: s.Conv, Model: p.Model.Backend, Set: set, Act: s.Dispatcher, View: exec.SetView,
		Viewer: p.Viewer, Audit: p.Audit, Person: s.Console, Guard: s.Guards, Killed: p.Killed, Redactor: s.Redactor, Network: network, MaxActions: maxActions}
	s.Watchdog = &Watchdog{Machine: p.Machine, Limits: s.Limits}
	return s, nil
}

// cwdOf is the directory commands start in: the guest account's work directory.
func cwdOf(home string) string {
	if home == "" {
		return ""
	}
	return home + "/work"
}
