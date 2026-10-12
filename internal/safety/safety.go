// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package safety

import "fmt"

// Verdict is the outcome of evaluating an action against the rules.
type Verdict int

// The verdicts are ordered by severity: the worst one across all matched rules wins.
const (
	Allow   Verdict = iota // run it
	Confirm                // ask the user first
	Deny                   // refuse
)

func (v Verdict) String() string {
	switch v {
	case Allow:
		return "allow"
	case Confirm:
		return "confirm"
	case Deny:
		return "deny"
	default:
		return fmt.Sprintf("verdict(%d)", int(v))
	}
}

// Worse returns the more severe of two verdicts.
func Worse(a, b Verdict) Verdict {
	if b > a {
		return b
	}
	return a
}

// Class is the class of a rule: what a match means before the profile is applied.
type Class string

// Rule classes (PLAN.md section 6.4).
const (
	ClassDeny    Class = "deny"
	ClassConfirm Class = "confirm"
)

// Verdict is the verdict a match of a rule of this class asks for.
func (c Class) Verdict() Verdict {
	if c == ClassDeny {
		return Deny
	}
	return Confirm
}
