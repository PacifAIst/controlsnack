// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"strings"
	"sync"

	"github.com/PacifAIst/controlsnack/internal/safety"
	"github.com/PacifAIst/controlsnack/internal/safety/policy"
)

// In a terminal the keyboard is a shell: what is typed there and ended with Return is a command, and typing it would be a way round
// the rules of the exec tool. So the exec rules judge it too, as it is typed (every line that is ended) and at Return (what was
// typed before it). What the rules cannot parse is let through: the line may be for a program that reads it (an editor, a
// language's prompt) and not for the shell, and the rules refuse text that is not a command only because it cannot tell.
// The rules are a guardrail here as they are for exec (README, section 7): the virtual machine is the boundary.

// typedIgnored are the rules of the exec tool that do not apply to a line typed into a terminal: they speak of text that cannot be
// understood as a command, which is what most lines typed into a program are.
var typedIgnored = map[string]bool{"DENY-UNPARSEABLE": true, "DENY-NOT-ALLOWLISTED": true}

// isTerminal says whether a window class is a terminal emulator.
func isTerminal(class string) bool {
	c := strings.ToLower(class)
	return strings.Contains(c, "terminal") || strings.Contains(c, "xterm") || strings.Contains(c, "konsole") || strings.Contains(c, "terminator") ||
		strings.Contains(c, "alacritty") || strings.Contains(c, "kitty")
}

// typedLine is the line that is being typed into the terminal that has the focus: what was typed after the last Return.
type typedLine struct {
	mu      sync.Mutex
	window  string
	pending string
}

// take returns what is pending for the window (nothing for another window than the one it was typed in) and forgets it.
func (t *typedLine) take(window string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := ""
	if t.window == window {
		p = t.pending
	}
	t.window, t.pending = window, ""
	return p
}

func (t *typedLine) set(window, pending string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.window, t.pending = window, pending
}

func (t *typedLine) forget() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.window, t.pending = "", ""
}

// keysOf is a key combination in a form that can be compared: lower case, the keys in the order written.
func keysOf(combo string) string { return strings.ToLower(strings.TrimSpace(combo)) }

// endsTheLine says whether the key sends the line to the shell.
func endsTheLine(combo string) bool {
	switch keysOf(combo) {
	case "return", "enter", "kp_enter", "ctrl+j", "ctrl+m":
		return true
	}
	return false
}

// clearsTheLine says whether the key throws away what was typed on the line.
func clearsTheLine(combo string) bool {
	switch keysOf(combo) {
	case "ctrl+c", "ctrl+u", "ctrl+d", "escape":
		return true
	}
	return false
}

// splitLines cuts a typed text into the lines that were ended (with a line break) and what follows the last one.
func splitLines(text string) (lines []string, rest string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	parts := strings.Split(text, "\n")
	return parts[:len(parts)-1], parts[len(parts)-1]
}

// judgeCommands asks the exec rules about each command and returns the worst verdict and the rules behind it.
func (d *Dispatcher) judgeCommands(ctx context.Context, commands []string) (safety.Verdict, []policy.RuleHit) {
	verdict := safety.Allow
	var hits []policy.RuleHit
	for _, c := range commands {
		if strings.TrimSpace(c) == "" {
			continue
		}
		dec := d.Policy.EvaluateExec(ctx, c, "")
		var kept []policy.RuleHit
		worst := safety.Allow
		for _, h := range dec.Rules {
			if typedIgnored[h.ID] {
				continue
			}
			kept = append(kept, h)
			worst = safety.Worse(worst, h.Class.Verdict())
		}
		verdict = safety.Worse(verdict, worst)
		hits = append(hits, kept...)
	}
	return verdict, hits
}

// typedVerdict is what the exec rules say about a key or a text that goes to a terminal: the verdict and the rules, or Allow with none.
func (d *Dispatcher) typedVerdict(ctx context.Context, tool, text, keys string) (safety.Verdict, []policy.RuleHit) {
	win, class := d.Exec.ActiveWindow()
	if win == "" || !isTerminal(class) {
		d.typed.forget()
		return safety.Allow, nil
	}
	switch tool {
	case "type_text":
		pending := d.typed.take(win)
		lines, rest := splitLines(pending + text)
		verdict, hits := d.judgeCommands(ctx, lines)
		if verdict == safety.Deny {
			d.typed.set(win, pending) // the text is not typed
			return verdict, hits
		}
		d.typed.set(win, rest)
		return verdict, hits
	case "key":
		switch {
		case endsTheLine(keys):
			pending := d.typed.take(win)
			verdict, hits := d.judgeCommands(ctx, []string{pending})
			if verdict != safety.Allow {
				d.typed.set(win, pending) // the line is still on the screen, and the key is not pressed (yet)
			}
			return verdict, hits
		case clearsTheLine(keys):
			d.typed.forget()
		}
	}
	return safety.Allow, nil
}
