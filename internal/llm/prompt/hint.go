// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import "github.com/PacifAIst/controlsnack-local/internal/llm/actions"

// Hint is the sentence that follows the host's words about a failed action, telling the model what to do instead. A small
// model that is told only "not found" tries the same id again and again; one that is told where ids come from, and what
// else it can do, changes what it does. It is empty for a code that needs no advice.
func Hint(code string) string {
	switch code {
	case actions.CodeNotFound:
		return ". Ids come only from the observation (the tree or the recognized text); do not guess one. Point with the mouse at the target's place in the picture instead, or read the screen again."
	case actions.CodeStaleElement:
		return ". The screen changed since that id was given: read the accessibility tree again for new ids."
	case actions.CodePolicyDenied:
		return ". The safety rules refuse this; do not try to get around them. Do it another way, or ask the user."
	case actions.CodeUserRejected:
		return ". The user said no. Do not repeat it: do something else, or ask the user what they want."
	case actions.CodeConfirmTimeout:
		return ". The user did not answer in time. Ask them with ask_user if you still need it."
	case actions.CodeRateLimited, actions.CodeBudgetExhausted:
		return ". You are doing too much too fast, or have used up a limit: slow down, or finish with done and say what is left."
	case actions.CodeTimeout:
		return ". It took too long. Check the screen to see whether it worked before you repeat it."
	case actions.CodeUnsupported, actions.CodeAgentUnavail:
		return ". This is not available here. Use another way."
	case CodeNotRun:
		return ". Answer again if you still want it."
	}
	return ""
}
