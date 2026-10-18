// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"strings"
	"testing"
)

func TestCommandsStartInTheGuestAccountsWorkDirectory(t *testing.T) {
	// Removing "." in the work directory is a question; in the home directory it would be the end of the account.
	a := newAssembly(t)
	a.model.replies = []reply{calls(tc("c1", "exec", `{"command":"rm -rf .","timeout_s":10}`)), calls(tc("c2", "done", `{"success":false,"summary":"x"}`))}
	a.run("t")
	tools := toolTexts(a.model.requests()[1])
	if len(tools) != 1 || !strings.Contains(tools[0], "user_rejected") {
		t.Errorf("results %q", tools)
	}
}
