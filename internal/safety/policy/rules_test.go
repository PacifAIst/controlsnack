// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/safety"
)

func TestDefaultRulesMatchThePlan(t *testing.T) {
	rs, err := DefaultRuleSet()
	if err != nil {
		t.Fatal(err)
	}
	// PLAN.md section 6.4 plus the structural rules of section 6.3.
	want := map[string]safety.Class{
		"DENY-ROOT-WIPE": safety.ClassDeny, "DENY-DISK": safety.ClassDeny, "DENY-RAW-DEV-WRITE": safety.ClassDeny,
		"DENY-POWER": safety.ClassDeny, "DENY-ACCOUNTS": safety.ClassDeny, "DENY-PRIV-ESC": safety.ClassDeny,
		"DENY-KERNEL": safety.ClassDeny, "DENY-FORK-BOMB": safety.ClassDeny, "DENY-PIPE-TO-SHELL": safety.ClassDeny,
		"DENY-DYNAMIC-CMD": safety.ClassDeny, "DENY-FIREWALL": safety.ClassDeny, "DENY-MASS-KILL": safety.ClassDeny,
		"DENY-UNPARSEABLE": safety.ClassDeny, "DENY-NOT-ALLOWLISTED": safety.ClassDeny,
		"CONFIRM-DELETE": safety.ClassConfirm, "CONFIRM-OVERWRITE": safety.ClassConfirm, "CONFIRM-NET-EXPOSE": safety.ClassConfirm,
		"CONFIRM-NET-EGRESS": safety.ClassConfirm, "CONFIRM-PERMS": safety.ClassConfirm, "CONFIRM-PERSIST": safety.ClassConfirm,
		// section 6.5
		"CONFIRM-GUI-DESTRUCTIVE": safety.ClassConfirm, "CONFIRM-GUI-NET-ACTION": safety.ClassConfirm,
		"CONFIRM-PASSWORD-FIELD": safety.ClassConfirm, "DENY-BROWSER-URL": safety.ClassDeny,
	}
	if len(rs.Rules) != len(want) {
		var have []string
		for _, r := range rs.Rules {
			have = append(have, r.ID)
		}
		t.Errorf("have %d rules %v, plan lists %d", len(rs.Rules), have, len(want))
	}
	for id, class := range want {
		r, ok := rs.Rule(id)
		if !ok {
			t.Errorf("missing rule %s", id)
			continue
		}
		if r.Class != class {
			t.Errorf("%s has class %s, want %s", id, r.Class, class)
		}
		if len(strings.Fields(r.Explain)) < 4 {
			t.Errorf("%s needs a real explanation, has %q", id, r.Explain)
		}
		if r.Disabled {
			t.Errorf("%s is disabled by default", id)
		}
	}
	if r, _ := rs.Rule("CONFIRM-NET-EGRESS"); !r.NetEgressOnly {
		t.Error("CONFIRM-NET-EGRESS applies only with --allow-net")
	}
	if rs.Strict() || rs.Mode != "normal" {
		t.Error("the default mode is normal")
	}
	if len(rs.Allowlist) == 0 {
		t.Error("the default strict allowlist is empty")
	}
}

func TestInvariantRulesAreTheSoundnessAndIsolationOnes(t *testing.T) {
	rs, _ := DefaultRuleSet()
	var got []string
	for _, r := range rs.Rules {
		if r.Invariant {
			got = append(got, r.ID)
		}
	}
	sort.Strings(got)
	want := []string{"DENY-DYNAMIC-CMD", "DENY-FIREWALL", "DENY-KERNEL", "DENY-NOT-ALLOWLISTED", "DENY-PIPE-TO-SHELL",
		"DENY-PRIV-ESC", "DENY-RAW-DEV-WRITE", "DENY-UNPARSEABLE"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("invariant rules\n got %v\nwant %v", got, want)
	}
	// None of them can be touched by a user policy.
	for _, id := range want {
		for _, class := range []string{"allow", "confirm", "deny", "off"} {
			_, err := LoadRuleSet([]byte("override:\n  " + id + ": {class: " + class + "}\n"))
			if err == nil || !strings.Contains(err.Error(), "invariant") {
				t.Errorf("override of %s to %s must be refused as invariant, got %v", id, class, err)
			}
		}
	}
}

func TestUserPolicyAddsRules(t *testing.T) {
	const policy = `
version: 1
rules:
  - id: CONFIRM-REGEX-pip
    class: confirm
    explain: Installing Python packages changes the environment.
    regex: '^\s*pip3? install'
  - id: DENY-REGEX-wget-exec
    class: deny
    explain: Never fetch and run.
    regex: 'wget .*&&.*chmod'
  - id: USER-no-npm
    class: deny
    explain: This project does not use npm.
    match:
      - command: [npm, npx, yarn]
  - id: USER-big-find
    class: confirm
    explain: A find over the whole file system is slow.
    match:
      - command: find
        arg_regex: ['^/$']
`
	e := mustEngine(t, policy, Options{})
	runCases(t, e, []execCase{
		{`pip install requests`, "CONFIRM-REGEX-pip"},
		{`pip3 install requests`, "CONFIRM-REGEX-pip"},
		{`pip list`, ""},
		{`wget http://x/a.sh && chmod +x a.sh`, "DENY-REGEX-wget-exec"},
		{`npm install`, "USER-no-npm"},
		{`env npx foo`, "USER-no-npm"},
		{`bash -c 'yarn add x'`, "USER-no-npm"},
		{`find / -name x`, "USER-big-find"},
		{`find . -name x`, ""},
		// built-in rules keep working next to the user's
		{`rm -rf /`, "CONFIRM-DELETE DENY-ROOT-WIPE"},
	})
	// A regex rule sees the raw command line, quotes and all.
	runCases(t, e, []execCase{{`echo "pip install x"`, ""}}) // not at the start of the line
	runCases(t, e, []execCase{{`  pip install x`, "CONFIRM-REGEX-pip"}})
}

func TestUserPolicyOverrides(t *testing.T) {
	e := mustEngine(t, `
override:
  CONFIRM-DELETE: {class: allow}
  DENY-POWER: {class: confirm}
  CONFIRM-OVERWRITE: {class: deny}
  DENY-DISK: {class: off}
`, Options{})
	runCases(t, e, []execCase{
		{`rm foo`, ""},
		{`shutdown -h now`, "DENY-POWER"},
		{`cp a b`, "CONFIRM-OVERWRITE"},
		{`mkfs.ext4 /dev/vda1`, ""},
		{`rm -rf /`, "DENY-ROOT-WIPE"}, // CONFIRM-DELETE is off, ROOT-WIPE still denies
	})
	// The effective class is what the decision reports.
	if d := e.EvaluateExec(context.Background(), `shutdown now`, ""); d.Verdict != safety.Confirm {
		t.Errorf("DENY-POWER overridden to confirm gave verdict %v", d.Verdict)
	}
	if d := e.EvaluateExec(context.Background(), `cp a b`, ""); d.Verdict != safety.Deny {
		t.Errorf("CONFIRM-OVERWRITE overridden to deny gave verdict %v", d.Verdict)
	}
}

func TestUserPolicyErrors(t *testing.T) {
	tests := []struct{ name, policy, want string }{
		{"unknown top-level key", "vrsion: 1\n", "vrsion"},
		{"unknown rule key", "rules:\n  - id: USER-x\n    class: deny\n    explain: e e e e\n    matchh: []\n", "matchh"},
		{"unsupported version", "version: 2\n", "version 2"},
		{"bad mode", "mode: paranoid\n", "mode"},
		{"override of unknown rule", "override:\n  NOPE-RULE: {class: deny}\n", "unknown rule"},
		{"override with a bad class", "override:\n  DENY-POWER: {class: maybe}\n", "class"},
		{"id not announcing itself", "rules:\n  - id: MYRULE\n    class: deny\n    explain: e e e e\n    match: [{command: x}]\n", "user rule IDs"},
		{"id shadowing a built-in", "rules:\n  - id: DENY-POWER\n    class: deny\n    explain: e e e e\n    match: [{command: x}]\n", "user rule IDs"},
		{"duplicate user ids", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    match: [{command: x}]\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    match: [{command: y}]\n", "twice"},
		{"missing explain", "rules:\n  - id: USER-a\n    class: deny\n    match: [{command: x}]\n", "explain"},
		{"bad class", "rules:\n  - id: USER-a\n    class: block\n    explain: e e e e\n    match: [{command: x}]\n", "class"},
		{"no match", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n", "match"},
		{"empty match entry", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    match: [{}]\n", "empty match"},
		{"flags without command", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    match: [{flags: [r]}]\n", "need a command"},
		{"unknown detector", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    match: [{detect: telepathy}]\n", "unknown detector"},
		{"unknown path set", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    match: [{writes_to: [everywhere]}]\n", "unknown path set"},
		{"bad arg regex", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    match: [{command: x, arg_regex: ['(']}]\n", "arg_regex"},
		{"bad regex", "rules:\n  - id: CONFIRM-REGEX-a\n    class: confirm\n    explain: e e e e\n    regex: '('\n", "regex"},
		{"regex too long", "rules:\n  - id: CONFIRM-REGEX-a\n    class: confirm\n    explain: e e e e\n    regex: '" + strings.Repeat("a", 600) + "'\n", "limit"},
		{"regex on a USER rule", "rules:\n  - id: USER-a\n    class: confirm\n    explain: e e e e\n    regex: 'x'\n", "only CONFIRM-REGEX"},
		{"regex rule with the wrong class", "rules:\n  - id: CONFIRM-REGEX-a\n    class: deny\n    explain: e e e e\n    regex: 'x'\n", "must have class"},
		{"regex id without a regex", "rules:\n  - id: CONFIRM-REGEX-a\n    class: confirm\n    explain: e e e e\n", "needs a regex"},
		{"regex and match together", "rules:\n  - id: CONFIRM-REGEX-a\n    class: confirm\n    explain: e e e e\n    regex: 'x'\n    match: [{command: y}]\n", "not both"},
		{"claiming invariant", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    invariant: true\n    match: [{command: x}]\n", "reserved"},
		{"claiming structural", "rules:\n  - id: USER-a\n    class: deny\n    explain: e e e e\n    structural: true\n", "reserved"},
		{"not yaml", "rules: [", "policy file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadRuleSet([]byte(tt.policy))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "policy file") {
				t.Errorf("errors should say they come from the policy file: %q", err)
			}
		})
	}

	var many strings.Builder
	many.WriteString("rules:\n")
	for i := 0; i < maxUserRules+1; i++ {
		many.WriteString("  - id: USER-r" + strings.Repeat("a", i%10) + string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + "\n    class: deny\n    explain: e e e e\n    match: [{command: x}]\n")
	}
	if _, err := LoadRuleSet([]byte(many.String())); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("too many rules must be refused: %v", err)
	}
}

func TestEmptyUserPolicyIsTheDefault(t *testing.T) {
	def, _ := DefaultRuleSet()
	for _, in := range []string{"", "   \n\t", "\n"} {
		rs, err := LoadRuleSet([]byte(in))
		if err != nil || rs != def {
			t.Errorf("%q should give the shared default rule set: %v", in, err)
		}
	}
	if rs, err := LoadRuleSet([]byte("# only a comment\n")); err != nil || rs.Hash != def.Hash {
		t.Errorf("a comment-only policy changes nothing: %v", err)
	}
}

func TestPolicyHash(t *testing.T) {
	def, _ := DefaultRuleSet()
	if len(def.Hash) != 64 {
		t.Fatalf("hash %q", def.Hash)
	}
	a1, _ := LoadRuleSet([]byte("override:\n  CONFIRM-DELETE: {class: allow}\n"))
	a2, _ := LoadRuleSet([]byte("override: {CONFIRM-DELETE: {class: allow}}\n"))
	b, _ := LoadRuleSet([]byte("override:\n  CONFIRM-DELETE: {class: deny}\n"))
	if a1.Hash != a2.Hash {
		t.Error("the same policy written differently must hash the same")
	}
	if a1.Hash == def.Hash || a1.Hash == b.Hash {
		t.Error("a different policy must hash differently")
	}
	s1, _ := LoadRuleSet([]byte("mode: strict\nallowlist: [ls, cat]\n"))
	s2, _ := LoadRuleSet([]byte("mode: strict\nallowlist: [cat, ls]\n"))
	if s1.Hash != s2.Hash {
		t.Error("allowlist order must not matter")
	}
}

func TestDefaultRuleSetIsNotMutatedByUserPolicies(t *testing.T) {
	// The defaults are shared; merging a user policy must work on a copy.
	before, _ := DefaultRuleSet()
	hash := before.Hash
	if _, err := LoadRuleSet([]byte("override:\n  CONFIRM-DELETE: {class: allow}\n")); err != nil {
		t.Fatal(err)
	}
	after, _ := DefaultRuleSet()
	if after.Hash != hash {
		t.Fatal("the shared default rule set changed")
	}
	if r, _ := after.Rule("CONFIRM-DELETE"); r.Disabled {
		t.Fatal("a user override leaked into the shared defaults")
	}
	e := mustEngine(t, "", Options{})
	if got := sortedIDs(e.EvaluateExec(context.Background(), `rm x`, "")); got != "CONFIRM-DELETE" {
		t.Fatalf("defaults broken after a merge: %q", got)
	}
}

func TestStrictMode(t *testing.T) {
	custom := mustEngine(t, "mode: strict\nallowlist: [ls, cat, echo]\n", Options{})
	runCases(t, custom, []execCase{
		{`ls`, ""},
		{`cat f`, ""},
		{`echo hi`, ""},
		{`rm f`, "CONFIRM-DELETE DENY-NOT-ALLOWLISTED"},
		{`echo $(date)`, "DENY-NOT-ALLOWLISTED"},
		{`ls | grep x`, "DENY-NOT-ALLOWLISTED"},
		{`env ls`, "DENY-NOT-ALLOWLISTED"}, // the wrapper itself is a command too
		{`bash -c 'ls'`, "DENY-NOT-ALLOWLISTED"},
		{`rm -rf /`, "CONFIRM-DELETE DENY-NOT-ALLOWLISTED DENY-ROOT-WIPE"}, // the rules still apply on top
		{`/bin/ls`, ""}, // the allowlist is by command name
		{`ls; cat x && echo ok`, ""},
	})
	def := mustEngine(t, "mode: strict\n", Options{})
	runCases(t, def, []execCase{
		{`ls -la`, ""},
		{`grep -r x .`, ""},
		{`git status`, ""},
		{`curl http://x`, "DENY-NOT-ALLOWLISTED"},
		{`rm f`, "CONFIRM-DELETE DENY-NOT-ALLOWLISTED"},
	})
	normal := mustEngine(t, "mode: normal\nallowlist: [ls]\n", Options{})
	runCases(t, normal, []execCase{{`curl http://x`, ""}}) // the allowlist only matters in strict mode
}

func TestNewEngineRefusesIncompleteRuleSets(t *testing.T) {
	if _, err := NewEngine(nil, Options{}); err == nil {
		t.Error("nil rule set")
	}
	rs := &RuleSet{Mode: "normal", byID: map[string]*Rule{}}
	if _, err := NewEngine(rs, Options{}); err == nil || !strings.Contains(err.Error(), "lacks") {
		t.Errorf("a rule set without the structural rules must be refused: %v", err)
	}
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.Home != "/home/cs" || o.User != "cs" || o.Cwd != "/home/cs/work" || o.MaxNesting != 3 || o.MaxCommandBytes != 64<<10 {
		t.Errorf("defaults: %+v", o)
	}
	o = Options{Home: "/home/x"}.withDefaults()
	if o.Cwd != "/home/x/work" {
		t.Errorf("cwd follows home: %q", o.Cwd)
	}
}

func TestStringListAcceptsScalarOrList(t *testing.T) {
	e := mustEngine(t, "rules:\n  - id: USER-a\n    class: deny\n    explain: scalar and list forms work.\n    match:\n      - command: foo\n        flags: x\n      - command: [bar, baz]\n", Options{})
	runCases(t, e, []execCase{
		{`foo -x`, "USER-a"}, {`foo -y`, ""}, {`bar`, "USER-a"}, {`baz`, "USER-a"}, {`qux`, ""},
	})
}
