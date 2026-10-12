// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/PacifAIst/controlsnack-local/internal/safety"
)

//go:embed default.yaml
var defaultYAML []byte

// stringList accepts either a YAML scalar or a sequence of scalars.
type stringList []string

func (s *stringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = stringList{n.Value}
		return nil
	case yaml.SequenceNode:
		out := make(stringList, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: expected a text value", c.Line)
			}
			out = append(out, c.Value)
		}
		*s = out
		return nil
	default:
		return fmt.Errorf("line %d: expected text or a list of text", n.Line)
	}
}

type matchYAML struct {
	Command      stringList `yaml:"command"`
	Flags        stringList `yaml:"flags"`
	WithoutFlags stringList `yaml:"without_flags"`
	Args         stringList `yaml:"args"`
	ArgRegex     stringList `yaml:"arg_regex"`
	WritesTo     stringList `yaml:"writes_to"`
	Detect       string     `yaml:"detect"`
}

type ruleYAML struct {
	ID            string      `yaml:"id"`
	Class         string      `yaml:"class"`
	Explain       string      `yaml:"explain"`
	Invariant     bool        `yaml:"invariant"`
	Structural    bool        `yaml:"structural"`
	NetEgressOnly bool        `yaml:"net_egress_only"`
	Match         []matchYAML `yaml:"match"`
	Regex         string      `yaml:"regex"`
}

type overrideYAML struct {
	Class string `yaml:"class"`
}

type fileYAML struct {
	Version         int                     `yaml:"version"`
	Mode            string                  `yaml:"mode"`
	Allowlist       stringList              `yaml:"allowlist"`
	StrictAllowlist stringList              `yaml:"strict_allowlist"`
	Override        map[string]overrideYAML `yaml:"override"`
	Rules           []ruleYAML              `yaml:"rules"`
}

const (
	maxUserRules = 200
	maxRegexLen  = 500
)

var (
	reRuleID = regexp.MustCompile(`^[A-Z][A-Z0-9-]{2,63}$`)
	// A user rule must announce itself: either a USER- rule built from the match DSL, or a regex rule on the raw
	// command whose prefix says what it does.
	reUserID = regexp.MustCompile(`^(USER|CONFIRM-REGEX|DENY-REGEX)-[A-Za-z0-9-]{1,48}$`)
)

func decodeStrict(data []byte, out *fileYAML) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

var (
	defaultOnce sync.Once
	defaultSet  *RuleSet
	defaultErr  error
)

// DefaultRuleSet returns the built-in rules. The result is shared; callers must not modify it.
func DefaultRuleSet() (*RuleSet, error) {
	defaultOnce.Do(func() { defaultSet, defaultErr = buildRuleSet(defaultYAML, nil) })
	return defaultSet, defaultErr
}

// LoadRuleSet merges a user policy file over the built-in rules. A nil or empty user policy yields the defaults.
//
// A user policy may add rules (USER-*, CONFIRM-REGEX-*, DENY-REGEX-*), change the class of a built-in rule or
// switch it off with `override`, and select `mode: strict` with an `allowlist`. It cannot change or remove an
// invariant rule, and unknown keys are errors.
func LoadRuleSet(user []byte) (*RuleSet, error) {
	if len(bytes.TrimSpace(user)) == 0 {
		return DefaultRuleSet()
	}
	return buildRuleSet(defaultYAML, user)
}

func buildRuleSet(defaults, user []byte) (*RuleSet, error) {
	var base fileYAML
	if err := decodeStrict(defaults, &base); err != nil {
		return nil, fmt.Errorf("built-in policy: %w", err)
	}
	rs := &RuleSet{Mode: "normal"}
	seen := map[string]bool{}
	for _, ry := range base.Rules {
		r, err := compileRule(ry)
		if err != nil {
			return nil, fmt.Errorf("built-in rule %s: %w", ry.ID, err)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("built-in rule %s is defined twice", r.ID)
		}
		seen[r.ID] = true
		rs.Rules = append(rs.Rules, r)
	}
	rs.reindex() // the index points into rs.Rules, so build it only once the slice has stopped growing
	allowlist := []string(base.StrictAllowlist)

	if len(user) > 0 {
		var uf fileYAML
		if err := decodeStrict(user, &uf); err != nil {
			return nil, fmt.Errorf("policy file: %w", err)
		}
		var err error
		if allowlist, err = rs.mergeUser(uf, allowlist); err != nil {
			return nil, fmt.Errorf("policy file: %w", err)
		}
	}
	rs.Allowlist = allowlist
	rs.reindex()
	rs.Hash = rs.hash()
	return rs, nil
}

// reindex rebuilds the ID index after Rules has been appended to.
func (rs *RuleSet) reindex() {
	rs.byID = make(map[string]*Rule, len(rs.Rules))
	for i := range rs.Rules {
		rs.byID[rs.Rules[i].ID] = &rs.Rules[i]
	}
}

func (rs *RuleSet) mergeUser(uf fileYAML, allowlist []string) ([]string, error) {
	if uf.Version != 0 && uf.Version != 1 {
		return nil, fmt.Errorf("version %d is not supported (this build reads version 1)", uf.Version)
	}
	if len(uf.StrictAllowlist) > 0 {
		return nil, errors.New("strict_allowlist is the built-in default; name your own list with allowlist")
	}
	switch uf.Mode {
	case "", "normal":
	case "strict":
		rs.Mode = "strict"
	default:
		return nil, fmt.Errorf("mode %q is not valid; use normal or strict", uf.Mode)
	}
	if len(uf.Allowlist) > 0 {
		allowlist = []string(uf.Allowlist)
	}

	ids := make([]string, 0, len(uf.Override))
	for id := range uf.Override {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r, ok := rs.byID[id]
		if !ok {
			return nil, fmt.Errorf("override of unknown rule %s", id)
		}
		if r.Invariant {
			return nil, fmt.Errorf("rule %s is an invariant rule and cannot be changed or switched off", id)
		}
		switch uf.Override[id].Class {
		case "allow", "off":
			r.Disabled = true
		case "confirm":
			r.Class = safety.ClassConfirm
		case "deny":
			r.Class = safety.ClassDeny
		default:
			return nil, fmt.Errorf("override of %s: class %q is not valid; use deny, confirm or allow", id, uf.Override[id].Class)
		}
	}

	if len(uf.Rules) > maxUserRules {
		return nil, fmt.Errorf("%d rules; the limit is %d", len(uf.Rules), maxUserRules)
	}
	for _, ry := range uf.Rules {
		if ry.Invariant || ry.Structural {
			return nil, fmt.Errorf("rule %s: invariant and structural are reserved for built-in rules", ry.ID)
		}
		if !reUserID.MatchString(ry.ID) {
			return nil, fmt.Errorf("rule %q: user rule IDs look like USER-name, CONFIRM-REGEX-name or DENY-REGEX-name", ry.ID)
		}
		if _, dup := rs.byID[ry.ID]; dup {
			return nil, fmt.Errorf("rule %s is defined twice", ry.ID)
		}
		r, err := compileRule(ry)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", ry.ID, err)
		}
		rs.Rules = append(rs.Rules, r)
		rs.reindex() // also makes a duplicate ID later in the same file visible to the check above
	}
	return allowlist, nil
}

func compileRule(ry ruleYAML) (Rule, error) {
	if !reRuleID.MatchString(strings.ToUpper(ry.ID)) && !reUserID.MatchString(ry.ID) {
		return Rule{}, fmt.Errorf("invalid rule ID %q", ry.ID)
	}
	var class safety.Class
	switch ry.Class {
	case "deny":
		class = safety.ClassDeny
	case "confirm":
		class = safety.ClassConfirm
	default:
		return Rule{}, fmt.Errorf("class %q is not valid; use deny or confirm", ry.Class)
	}
	if strings.TrimSpace(ry.Explain) == "" {
		return Rule{}, errors.New("every rule needs an explain text: it is what the user sees")
	}
	r := Rule{
		ID: ry.ID, Class: class, Explain: strings.Join(strings.Fields(ry.Explain), " "),
		Invariant: ry.Invariant, Structural: ry.Structural, NetEgressOnly: ry.NetEgressOnly,
	}

	hasRegexID := strings.HasPrefix(ry.ID, "CONFIRM-REGEX-") || strings.HasPrefix(ry.ID, "DENY-REGEX-")
	switch {
	case ry.Regex != "":
		if !hasRegexID {
			return Rule{}, errors.New("only CONFIRM-REGEX-* and DENY-REGEX-* rules may use regex")
		}
		if len(ry.Match) > 0 {
			return Rule{}, errors.New("a rule has either regex or match, not both")
		}
		if len(ry.Regex) > maxRegexLen {
			return Rule{}, fmt.Errorf("regex is %d characters; the limit is %d", len(ry.Regex), maxRegexLen)
		}
		re, err := regexp.Compile(ry.Regex)
		if err != nil {
			return Rule{}, fmt.Errorf("regex: %w", err)
		}
		r.Regex = re
		if want := regexClass(ry.ID); want != class {
			return Rule{}, fmt.Errorf("%s must have class %s", ry.ID, want)
		}
	case hasRegexID:
		return Rule{}, errors.New("a regex rule needs a regex")
	case ry.Structural:
		if len(ry.Match) > 0 {
			return Rule{}, errors.New("a structural rule is raised by the engine and has no match")
		}
	case len(ry.Match) == 0:
		return Rule{}, errors.New("a rule needs match entries")
	}

	for i, my := range ry.Match {
		m, err := compileMatch(my)
		if err != nil {
			return Rule{}, fmt.Errorf("match %d: %w", i+1, err)
		}
		r.Matches = append(r.Matches, m)
	}
	return r, nil
}

func regexClass(id string) safety.Class {
	if strings.HasPrefix(id, "DENY-") {
		return safety.ClassDeny
	}
	return safety.ClassConfirm
}

func compileMatch(my matchYAML) (Match, error) {
	m := Match{
		Command: my.Command, Flags: my.Flags, WithoutFlags: my.WithoutFlags, Args: my.Args,
		ArgRegex: my.ArgRegex, WritesTo: my.WritesTo, Detect: my.Detect,
	}
	if len(m.Command) == 0 && len(m.Flags) == 0 && len(m.Args) == 0 && len(m.ArgRegex) == 0 && len(m.WritesTo) == 0 && m.Detect == "" {
		return Match{}, errors.New("an empty match would match every command")
	}
	if len(m.Command) == 0 && (len(m.Flags) > 0 || len(m.WithoutFlags) > 0 || len(m.Args) > 0) {
		return Match{}, errors.New("flags, without_flags and args need a command to apply to")
	}
	for _, pat := range m.Command {
		if pat == "" {
			return Match{}, errors.New("empty command name")
		}
	}
	for _, name := range m.WritesTo {
		if _, ok := pathSetNames[name]; !ok {
			return Match{}, fmt.Errorf("unknown path set %q (known: %s)", name, strings.Join(sortedKeys(pathSetNames), ", "))
		}
	}
	if m.Detect != "" {
		if _, ok := detectors[m.Detect]; !ok {
			return Match{}, fmt.Errorf("unknown detector %q (known: %s)", m.Detect, strings.Join(sortedKeys(detectors), ", "))
		}
	}
	for _, src := range m.ArgRegex {
		if len(src) > maxRegexLen {
			return Match{}, fmt.Errorf("arg_regex is %d characters; the limit is %d", len(src), maxRegexLen)
		}
		re, err := regexp.Compile(src)
		if err != nil {
			return Match{}, fmt.Errorf("arg_regex %q: %w", src, err)
		}
		m.argRegex = append(m.argRegex, re)
	}
	return m, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// hash fingerprints the effective policy for the audit log's config record.
func (rs *RuleSet) hash() string {
	type m struct {
		Command, Flags, WithoutFlags, Args, ArgRegex, WritesTo []string
		Detect                                                 string
	}
	type r struct {
		ID, Class, Explain                   string
		Disabled, Invariant, Structural, Net bool
		Regex                                string
		Matches                              []m
	}
	out := struct {
		Mode      string
		Allowlist []string
		Rules     []r
	}{Mode: rs.Mode}
	if rs.Strict() {
		out.Allowlist = append([]string(nil), rs.Allowlist...)
		sort.Strings(out.Allowlist)
	}
	for _, rule := range rs.Rules {
		rr := r{ID: rule.ID, Class: string(rule.Class), Explain: rule.Explain, Disabled: rule.Disabled,
			Invariant: rule.Invariant, Structural: rule.Structural, Net: rule.NetEgressOnly}
		if rule.Regex != nil {
			rr.Regex = rule.Regex.String()
		}
		for _, mt := range rule.Matches {
			rr.Matches = append(rr.Matches, m{mt.Command, mt.Flags, mt.WithoutFlags, mt.Args, mt.ArgRegex, mt.WritesTo, mt.Detect})
		}
		out.Rules = append(out.Rules, rr)
	}
	data, _ := json.Marshal(out) // plain strings and bools: cannot fail
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
