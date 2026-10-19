// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"context"
	"regexp"

	"github.com/PacifAIst/controlsnack/internal/safety"
)

// RuleHit is one rule that matched, with the invocation it matched on.
type RuleHit struct {
	ID          string       `json:"id"`
	Class       safety.Class `json:"class"`
	Invocation  string       `json:"invocation"`
	Explanation string       `json:"explanation"`
}

// Decision is the outcome of evaluating one action against the rules, before the safety profile is applied
// (safety.Profile.Apply turns it into the verdict that is enforced).
type Decision struct {
	Verdict safety.Verdict `json:"verdict"`
	Rules   []RuleHit      `json:"rules,omitempty"`
}

// RuleIDs lists the distinct IDs of the matched rules in order of first appearance.
func (d Decision) RuleIDs() []string {
	var ids []string
	seen := map[string]bool{}
	for _, h := range d.Rules {
		if !seen[h.ID] {
			seen[h.ID] = true
			ids = append(ids, h.ID)
		}
	}
	return ids
}

// ScriptReader lets the engine look at a script before it runs. In a session it is backed by the guest agent's
// file_peek request (PLAN.md section 5.1); the engine never touches the host file system.
type ScriptReader interface {
	// Peek returns up to max bytes from the start of the file, the file's full size and whether it exists. An error
	// means the file could not be inspected (for example the agent is down), which the engine treats as "cannot
	// review" and denies.
	Peek(ctx context.Context, path string, max int) (head []byte, size int64, exists bool, err error)
}

// Options configure an Engine.
type Options struct {
	// NetEgress is true under --allow-net; it switches on the rules marked net_egress_only.
	NetEgress bool
	// Home, User and Cwd describe the guest account that commands run as. Defaults: /home/cs, cs, /home/cs/work.
	Home, User, Cwd string
	// Scripts is consulted when a command runs a script file. Nil disables script analysis.
	Scripts ScriptReader
	// MaxNesting bounds `bash -c` recursion and script-in-script analysis (default 3, PLAN.md section 6.3).
	MaxNesting int
	// MaxCommandBytes bounds the size of a command (default 64 KiB).
	MaxCommandBytes int
}

func (o Options) withDefaults() Options {
	if o.Home == "" {
		o.Home = "/home/cs"
	}
	if o.User == "" {
		o.User = "cs"
	}
	if o.Cwd == "" {
		o.Cwd = o.Home + "/work"
	}
	if o.MaxNesting <= 0 {
		o.MaxNesting = 3
	}
	if o.MaxCommandBytes <= 0 {
		o.MaxCommandBytes = 64 << 10
	}
	return o
}

// Rule is one compiled rule.
type Rule struct {
	ID      string
	Class   safety.Class
	Explain string
	// Disabled rules never match (a user override of class "allow").
	Disabled bool
	// Invariant rules cannot be changed or removed by a user policy.
	Invariant bool
	// NetEgressOnly rules only apply under --allow-net.
	NetEgressOnly bool
	// Structural rules are emitted by the engine itself (parse failures, dynamic command names, fork bombs, strict
	// mode) rather than matched; they exist as rules so their class, text and invariant status live in one place.
	Structural bool

	Matches []Match
	// Regex, for the CONFIRM-REGEX-* / DENY-REGEX-* rules of a user policy, is matched against the raw command.
	Regex *regexp.Regexp
}

// Match is one alternative of a rule. All the conditions that are set must hold (AND); a rule matches when any of
// its Match entries does (OR).
type Match struct {
	Command      []string // command names; * ? and [..] wildcards allowed
	Flags        []string // any of these flags present (short letters or long names without dashes)
	WithoutFlags []string // none of these flags present
	Args         []string // any positional argument equal to one of these
	ArgRegex     []string // any argument matches any of these regular expressions
	WritesTo     []string // any file the command writes is in one of these named path sets
	Detect       string   // a named detector from detectors.go

	argRegex []*regexp.Regexp
}

// RuleSet is the effective policy: the built-in rules with the user's policy merged over them.
type RuleSet struct {
	// Mode is "normal" or "strict". In strict mode only commands on Allowlist run; the rules still apply on top.
	Mode      string
	Allowlist []string
	Rules     []Rule
	// Hash identifies the effective policy in the audit log.
	Hash string

	byID map[string]*Rule
}

// Strict reports whether only allowlisted commands may run.
func (rs *RuleSet) Strict() bool { return rs.Mode == "strict" }

// Rule returns the rule with the given ID.
func (rs *RuleSet) Rule(id string) (*Rule, bool) {
	r, ok := rs.byID[id]
	return r, ok
}
