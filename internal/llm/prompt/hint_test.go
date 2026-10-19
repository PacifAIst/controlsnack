// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/llm/actions"
)

func TestEveryFailureCodeHasAdviceOrIsKnownToNeedNone(t *testing.T) {
	needNone := map[string]bool{actions.CodeInvalidArgs: true, actions.CodeUnknownTool: true, actions.CodeInternal: true}
	for _, code := range []string{
		actions.CodeInvalidArgs, actions.CodeUnknownTool, actions.CodePolicyDenied, actions.CodeUserRejected, actions.CodeConfirmTimeout,
		actions.CodeRateLimited, actions.CodeBudgetExhausted, actions.CodeStaleElement, actions.CodeNotFound, actions.CodeTimeout,
		actions.CodeAgentUnavail, actions.CodeUnsupported, actions.CodeInternal, CodeNotRun, CodeDropped,
	} {
		h := Hint(code)
		switch {
		case needNone[code] || code == CodeDropped:
			if h != "" {
				t.Errorf("%s: %q (the problems already say what to do)", code, h)
			}
		case !strings.HasPrefix(h, ". ") || !strings.HasSuffix(h, ".") || strings.Contains(h, "\n") || len(h) > 260:
			t.Errorf("%s: advice %q is not one short sentence that continues the host's words", code, h)
		}
	}
	if Hint("") != "" || Hint("something_new") != "" {
		t.Error("an unknown code has no advice")
	}
	if !strings.Contains(Hint(actions.CodeNotFound), "do not guess") || !strings.Contains(Hint(actions.CodeNotFound), "mouse") {
		t.Errorf("a missing id: %q", Hint(actions.CodeNotFound))
	}
}

func TestAdviceReadsAfterAFailedResult(t *testing.T) {
	r := Result{Tool: "click_element", Code: actions.CodeNotFound, Detail: "no element w1" + Hint(actions.CodeNotFound)}
	got := textRenderer().ResultText(r, 1, Limits{})
	if !strings.HasPrefix(got, "1. click_element error not_found no element w1. Ids come only from the observation") {
		t.Errorf("result = %q", got)
	}
}
