// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"context"
	"fmt"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/safety"
)

// structuralRules must exist in every rule set: the engine raises them itself.
var structuralRules = []string{"DENY-UNPARSEABLE", "DENY-DYNAMIC-CMD", "DENY-NOT-ALLOWLISTED", "DENY-FORK-BOMB", "DENY-PIPE-TO-SHELL"}

// Engine evaluates actions against a rule set. It is safe for concurrent use.
//
// The exec policy is a guardrail, not the security boundary: the VM is (PLAN.md D-16). It reads the command the
// way bash would and judges every command it finds, but it cannot see what a python or node script will do, or
// what a dynamic argument will turn out to be. Anything it cannot analyze is denied rather than guessed at.
type Engine struct {
	rs    *RuleSet
	o     Options
	ps    *pathSets
	allow map[string]bool
}

// NewEngine builds an engine for a rule set.
func NewEngine(rs *RuleSet, o Options) (*Engine, error) {
	if rs == nil {
		return nil, fmt.Errorf("policy: no rule set")
	}
	for _, id := range structuralRules {
		if _, ok := rs.Rule(id); !ok {
			return nil, fmt.Errorf("policy: rule set lacks the built-in rule %s", id)
		}
	}
	o = o.withDefaults()
	e := &Engine{rs: rs, o: o, ps: newPathSets(o.Home), allow: map[string]bool{}}
	for _, name := range rs.Allowlist {
		e.allow[name] = true
	}
	return e, nil
}

// RuleSet returns the rule set the engine uses.
func (e *Engine) RuleSet() *RuleSet { return e.rs }

// EvaluateExec judges a shell command line that the model wants to run in the guest. cwd is the working directory
// it will start in ("" for the default).
func (e *Engine) EvaluateExec(ctx context.Context, command, cwd string) (d Decision) {
	a := &analysis{e: e, ctx: ctx, seen: map[string]bool{}, written: map[string]*written{}}
	// A failure inside the analysis must never turn into "allowed".
	defer func() {
		if r := recover(); r != nil {
			a.unparseable(fmt.Sprintf("internal error while reviewing the command: %v", r))
			d = a.decision()
		}
	}()

	if cwd == "" {
		cwd = e.o.Cwd
	}
	a.cwd = normalizePath(cwd, e.o.Cwd, e.o.Home)

	switch {
	case len(command) > e.o.MaxCommandBytes:
		a.unparseable(fmt.Sprintf("the command is %d bytes; the limit is %d", len(command), e.o.MaxCommandBytes))
	case strings.ContainsRune(command, 0):
		a.unparseable("the command contains a NUL byte")
	default:
		a.userRegexRules(command)
		a.analyzeText(command, 0, baseCtx{})
	}
	return a.decision()
}

// userRegexRules applies the CONFIRM-REGEX-* and DENY-REGEX-* rules of a user policy to the raw command.
func (a *analysis) userRegexRules(command string) {
	for i := range a.e.rs.Rules {
		r := &a.e.rs.Rules[i]
		if r.Regex != nil && !r.Disabled && r.Regex.MatchString(command) {
			text := command
			if len(text) > 160 {
				text = text[:157] + "..."
			}
			a.emit(r.ID, text)
		}
	}
}

// decision folds the hits into one verdict: the most severe class wins.
func (a *analysis) decision() Decision {
	d := Decision{Verdict: safety.Allow, Rules: a.hits}
	for _, h := range a.hits {
		d.Verdict = safety.Worse(d.Verdict, h.Class.Verdict())
	}
	return d
}
