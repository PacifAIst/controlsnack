// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package safety

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
)

func TestVerdictOrderingAndNames(t *testing.T) {
	if Allow >= Confirm || Confirm >= Deny {
		t.Fatal("verdicts must be ordered by severity")
	}
	if Worse(Allow, Deny) != Deny || Worse(Deny, Allow) != Deny || Worse(Confirm, Allow) != Confirm || Worse(Allow, Allow) != Allow {
		t.Fatal("Worse must return the more severe verdict")
	}
	for v, s := range map[Verdict]string{Allow: "allow", Confirm: "confirm", Deny: "deny"} {
		if v.String() != s {
			t.Errorf("%d.String() = %q, want %q", v, v.String(), s)
		}
	}
	if ClassDeny.Verdict() != Deny || ClassConfirm.Verdict() != Confirm {
		t.Fatal("rule classes map to their verdicts")
	}
}

func TestInvariantsAreTheNineOfThePlan(t *testing.T) {
	inv := Invariants()
	if len(inv) != 9 {
		t.Fatalf("PLAN.md section 6.1 lists 9 invariants, have %d", len(inv))
	}
	for i, in := range inv {
		if in.ID != i+1 || in.Name == "" || in.Statement == "" || in.Enforced == "" {
			t.Errorf("invariant %d is incomplete: %+v", i+1, in)
		}
	}
	// A caller must not be able to weaken the list by editing the returned slice.
	inv[0].Statement = "tampered"
	if Invariants()[0].Statement == "tampered" {
		t.Fatal("Invariants must return a copy")
	}
}

// configKeys lists every dotted key of the configuration.
func configKeys(t *testing.T) []string {
	t.Helper()
	var keys []string
	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := f.Tag.Get("yaml")
			if tag == "" {
				continue
			}
			path := tag
			if prefix != "" {
				path = prefix + "." + tag
			}
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(config.Duration(0)) {
				walk(f.Type, path)
				continue
			}
			keys = append(keys, path)
		}
	}
	walk(reflect.TypeOf(config.Config{}), "")
	return keys
}

func TestNoConfigKeyCanSwitchAnInvariantOff(t *testing.T) {
	// PLAN.md P2-T01. The invariants have no switch. This guard fails the moment someone adds a key that smells
	// like one (disable_audit, no_fence, allow_remote_listen, ...), so the question gets asked in review.
	suspicious := []string{"kill", "audit_off", "no_audit", "disable", "fence", "untrusted", "csrf", "cookie", "origin",
		"loopback", "listen", "bind", "nic", "virtfs", "share", "schema", "validate", "insecure", "skip"}
	allowed := map[string]bool{
		"safety.policy": true, // a rule file *adds to* the built-in rules; it cannot remove invariant-tagged rules (P2-T03)
	}
	for _, key := range configKeys(t) {
		if allowed[key] {
			continue
		}
		for _, bad := range suspicious {
			if strings.Contains(key, bad) {
				t.Errorf("config key %q contains %q: configuration must not be able to switch off a safety invariant (PLAN.md section 6.1)", key, bad)
			}
		}
	}
	// And the single risky switch there is, --unsafe, is not a config key at all.
	for _, key := range configKeys(t) {
		if key == "safety.unsafe" {
			// yaml tag exists for display only; the mapstructure tag is "-" (see config.Safety).
			f, _ := reflect.TypeOf(config.Safety{}).FieldByName("Unsafe")
			if f.Tag.Get("mapstructure") != "-" {
				t.Fatal("safety.unsafe must not be loadable from a file or the environment")
			}
		}
	}
}

func TestProfilesForEveryCombinationKeepInvariants(t *testing.T) {
	// Whatever the configuration says, the profile is built from budgets only; there is nothing in it that could
	// turn an invariant off. This documents that by construction: Profile has no field named after an invariant.
	for _, f := range reflect.VisibleFields(reflect.TypeOf(Profile{})) {
		name := strings.ToLower(f.Name)
		for _, bad := range []string{"kill", "audit", "fence", "loopback", "nic", "schema", "csrf"} {
			if strings.Contains(name, bad) {
				t.Errorf("Profile.%s looks like an invariant switch", f.Name)
			}
		}
	}
}

func TestSafeProfileMatchesTheDefaults(t *testing.T) {
	p := ProfileFor(config.Defaults())
	want := Profile{
		ActionsPerSec: 2, ActionsPerResponse: 3, ActionsPerTask: 60, MaxTurns: 100,
		SessionTimeout: time.Hour, IdleTimeout: 15 * time.Minute, ConfirmTimeout: 120 * time.Second,
	}
	if p != want {
		t.Fatalf("safe profile\n got %+v\nwant %+v", p, want)
	}
	if p.Unsafe || p.AllowHeadless || p.AllowPlainHTTPRemoteLLM || p.Step {
		t.Fatal("nothing is relaxed by default")
	}
}

func TestUnsafeProfileRelaxesExactlyTheListInSection62(t *testing.T) {
	c := config.Defaults()
	c.Safety.Unsafe = true
	c.Safety.ActionsPerSec = 10
	c.Safety.ActionsPerResponse = 10
	c.Safety.ActionsPerTask = 500
	c.Safety.MaxTurns = 500
	c.Safety.Timeout = config.Duration(8 * time.Hour)
	p := ProfileFor(c)
	if !p.Unsafe || !p.AllowHeadless || !p.AllowPlainHTTPRemoteLLM {
		t.Errorf("unsafe should allow headless runs and plain-HTTP remote endpoints: %+v", p)
	}
	if p.ActionsPerSec != 10 || p.ActionsPerResponse != 10 || p.ActionsPerTask != 500 || p.MaxTurns != 500 || p.SessionTimeout != 8*time.Hour {
		t.Errorf("unsafe budgets: %+v", p)
	}
	if p.ConfirmTimeout != 120*time.Second || p.IdleTimeout != 15*time.Minute {
		t.Errorf("confirm and idle timeouts do not change under --unsafe: %+v", p)
	}
}

func TestProfileClampsWhatValidationShouldHaveRejected(t *testing.T) {
	// A Config built in code can carry values validation never saw; the profile must not exceed the mode's bounds.
	c := config.Defaults()
	c.Safety.ActionsPerSec = 1000
	c.Safety.ActionsPerResponse = 99
	c.Safety.ActionsPerTask = 99999
	c.Safety.MaxTurns = 99999
	c.Safety.Timeout = config.Duration(240 * time.Hour)
	c.Safety.IdleTimeout = config.Duration(240 * time.Hour)
	p := ProfileFor(c)
	b := config.SafeBounds
	if p.ActionsPerSec != b.ActionsPerSec || p.ActionsPerResponse != b.ActionsPerResponse || p.ActionsPerTask != b.ActionsPerTask ||
		p.MaxTurns != b.MaxTurns || p.SessionTimeout != b.SessionTimeout || p.IdleTimeout != b.SessionTimeout {
		t.Fatalf("safe profile exceeded SafeBounds: %+v", p)
	}
	c.Safety.Unsafe = true
	if p := ProfileFor(c); p.ActionsPerSec != config.UnsafeBounds.ActionsPerSec || p.ActionsPerTask != config.UnsafeBounds.ActionsPerTask {
		t.Fatalf("unsafe profile exceeded UnsafeBounds: %+v", p)
	}
}

func TestProfileFallsBackToSafeDefaultsForNonsense(t *testing.T) {
	c := config.Defaults()
	c.Safety.Unsafe = true // even here a zero must not mean "no limit"
	c.Safety.ActionsPerSec = 0
	c.Safety.ActionsPerResponse = -4
	c.Safety.ActionsPerTask = 0
	c.Safety.MaxTurns = 0
	c.Safety.Timeout = 0
	c.Safety.ConfirmTimeout = 0
	p := ProfileFor(c)
	if p.ActionsPerSec != 2 || p.ActionsPerResponse != 3 || p.ActionsPerTask != 60 || p.MaxTurns != 100 ||
		p.SessionTimeout != time.Hour || p.ConfirmTimeout != 120*time.Second {
		t.Fatalf("non-positive values must fall back to the safe defaults, not to the loosest setting: %+v", p)
	}
}

func TestProfileApply(t *testing.T) {
	tests := []struct {
		name     string
		unsafe   bool
		step     bool
		in, want Verdict
	}{
		{"safe allow", false, false, Allow, Allow},
		{"safe confirm", false, false, Confirm, Confirm},
		{"safe deny", false, false, Deny, Deny},
		{"unsafe allow", true, false, Allow, Allow},
		{"unsafe confirm becomes allow", true, false, Confirm, Allow},
		{"unsafe deny becomes confirm", true, false, Deny, Confirm},
		{"step makes allow a confirm", false, true, Allow, Confirm},
		{"step leaves deny alone", false, true, Deny, Deny},
		{"step leaves confirm alone", false, true, Confirm, Confirm},
		{"step beats unsafe for confirm-class", true, true, Confirm, Confirm},
		{"step beats unsafe for allow", true, true, Allow, Confirm},
		{"unsafe+step deny is still a confirm", true, true, Deny, Confirm},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Profile{Unsafe: tt.unsafe, Step: tt.step}
			if got := p.Apply(tt.in); got != tt.want {
				t.Fatalf("Apply(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
