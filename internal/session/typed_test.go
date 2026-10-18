// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// focus puts the focus on a window of a class, as the model's last look saw it ("" for no window).
func (r *dgrid) focus(id, class string) {
	r.t.Helper()
	var info protocol.ScreenInfoRes
	if id != "" {
		info.ActiveWindow = &protocol.Window{ID: id, Class: class, Active: true}
	}
	r.d.Exec.SetView(vision.RawSeen{Info: info})
}

// Each action here is a model response of its own, so that the limit on actions in one response is not what is tested.
func (r *dgrid) typeText(text string) Step {
	r.d.Limits.BeginResponse(1)
	return r.run(&actions.TypeTextArgs{Text: text}, "type")
}

func (r *dgrid) press(keys string) Step {
	r.d.Limits.BeginResponse(1)
	return r.run(&actions.KeyArgs{Keys: keys}, "press")
}

func refusedStep(t *testing.T, s Step, why string) {
	t.Helper()
	if s.Result.OK || s.Result.Code != actions.CodePolicyDenied || !strings.Contains(s.Result.Detail, why) {
		t.Errorf("not refused for %s: %+v", why, s.Result)
	}
}

func TestACommandTypedIntoATerminalAndEndedIsJudgedLikeAnExecOne(t *testing.T) {
	r := newDgrid(t)
	r.focus("0x1", "Xfce4-terminal")
	before := r.agent.count()
	refusedStep(t, r.typeText("rm -rf /\n"), "DENY-ROOT-WIPE")
	if r.agent.count() != before {
		t.Error("the text that wipes the machine reached the guest")
	}
	// An ordinary command goes through.
	if s := r.typeText("uname -r\n"); !s.Result.OK {
		t.Errorf("an ordinary command: %+v", s.Result)
	}
	if s := r.typeText("ls"); !s.Result.OK { // not ended: nothing to judge yet
		t.Errorf("a line that is not ended: %+v", s.Result)
	}
	// One bad line among good ones refuses the lot, whatever the order.
	if s := r.press("ctrl+u"); !s.Result.OK { // what was typed ("ls") is still on the line: start again
		t.Fatalf("%+v", s.Result)
	}
	refusedStep(t, r.typeText("ls\nrm -rf /\npwd\n"), "DENY-ROOT-WIPE")
	refusedStep(t, r.typeText("rm -rf /\nls\n"), "DENY-ROOT-WIPE")
}

func TestTextThatIsRefusedIsNotTypedSoWhatWasThereBeforeStaysTheLine(t *testing.T) {
	r := newDgrid(t)
	r.asker.answer, r.asker.approve = true, false // the person rejects what they are asked
	r.focus("0x1", "Xfce4-terminal")
	if s := r.typeText("rm notes.txt"); !s.Result.OK {
		t.Fatalf("%+v", s.Result)
	}
	// This text would never reach the screen, so the line is still the one typed before.
	refusedStep(t, r.typeText("; rm -rf /\n"), "DENY-ROOT-WIPE")
	if s := r.press("Return"); s.Result.OK {
		t.Errorf("Return sent the line typed before, which needs the person's approval, and it ran: %+v", s.Result)
	}
}

func TestTypingIntoAPasswordFieldOfATerminalWindowStillAsksThePerson(t *testing.T) {
	r := newDgrid(t)
	r.asker.answer, r.asker.approve = true, false
	// The rules of the screen and the rules of the command are both asked, and the stricter answer stands.
	r.d.Exec.SetView(vision.RawSeen{
		Info: protocol.ScreenInfoRes{ActiveWindow: &protocol.Window{ID: "0x1", Class: "Xfce4-terminal"}},
		Tree: &protocol.A11yTreeRes{Generation: "g1", Nodes: []protocol.A11yNode{
			{ID: "a3", Role: "password text", Name: "Password", Bounds: protocol.Rect{300, 300, 200, 24}, States: []string{"focused", "editable"}},
		}},
	})
	if s := r.typeText("hunter2"); s.Result.OK {
		t.Errorf("a password typed without the person's word: %+v", s.Result)
	}
}

func TestACommandTypedInPiecesIsJudgedWhenReturnSendsIt(t *testing.T) {
	r := newDgrid(t)
	r.focus("0x1", "Xfce4-terminal")
	if s := r.typeText("rm -rf"); !s.Result.OK {
		t.Fatalf("half a command: %+v", s.Result)
	}
	if s := r.typeText(" /"); !s.Result.OK {
		t.Fatalf("the rest of it, without a line break: %+v", s.Result)
	}
	before := r.agent.count()
	refusedStep(t, r.press("Return"), "DENY-ROOT-WIPE")
	if r.agent.count() != before {
		t.Error("the key that sends a wiping command was pressed")
	}
	// The line is still on the screen: the key is refused again, until the line is thrown away.
	refusedStep(t, r.press("KP_Enter"), "DENY-ROOT-WIPE")
	if s := r.press("ctrl+u"); !s.Result.OK {
		t.Fatalf("throwing the line away: %+v", s.Result)
	}
	if s := r.press("Return"); !s.Result.OK {
		t.Errorf("an empty line after it was cleared: %+v", s.Result)
	}
	// A new ordinary command is not held back by the old one.
	if s := r.typeText("pwd"); !s.Result.OK {
		t.Fatalf("%+v", s.Result)
	}
	if s := r.press("Return"); !s.Result.OK {
		t.Errorf("an ordinary command at Return: %+v", s.Result)
	}
}

func TestACommandThatNeedsConfirmationTypedIntoATerminalAsksThePerson(t *testing.T) {
	r := newDgrid(t)
	r.asker.answer, r.asker.approve = true, false
	r.focus("0x1", "Xfce4-terminal")
	if s := r.typeText("rm notes.txt"); !s.Result.OK {
		t.Fatalf("%+v", s.Result)
	}
	s := r.press("Return")
	if s.Result.OK {
		t.Fatalf("the person rejected it and it ran: %+v", s.Result)
	}
	reqs := r.asker.requests()
	if len(reqs) != 1 || reqs[0].Tool != "key" || len(reqs[0].Rules) == 0 || reqs[0].Rules[0].ID != "CONFIRM-DELETE" {
		t.Errorf("the person was asked: %+v", reqs)
	}
}

func TestWhatIsTypedElsewhereIsNotACommand(t *testing.T) {
	r := newDgrid(t)
	// An editor: the text is text.
	r.focus("0x2", "Mousepad")
	if s := r.typeText("rm -rf /\n"); !s.Result.OK {
		t.Errorf("text in an editor: %+v", s.Result)
	}
	// No window in focus.
	r.focus("", "")
	if s := r.typeText("rm -rf /\n"); !s.Result.OK {
		t.Errorf("no window: %+v", s.Result)
	}
	// A line typed into a terminal does not follow the person to another window.
	r.focus("0x1", "Xfce4-terminal")
	_ = r.typeText("rm -rf /")
	r.focus("0x2", "Mousepad")
	if s := r.press("Return"); !s.Result.OK {
		t.Errorf("Return in an editor: %+v", s.Result)
	}
	r.focus("0x1", "Xfce4-terminal")
	if s := r.press("Return"); !s.Result.OK {
		t.Errorf("back in the terminal the line is gone: %+v", s.Result)
	}
}

func TestLinesThatAreNotCommandsAreNotRefusedForThat(t *testing.T) {
	r := newDgrid(t)
	r.focus("0x1", "Xfce4-terminal")
	// A line for a program that reads it (a language's prompt, an editor) is no shell syntax; the rules would refuse it only because
	// they cannot tell what it is.
	for _, line := range []string{"print(1 +)\n", "if x then\n", "def f(:\n"} {
		if s := r.typeText(line); !s.Result.OK {
			t.Errorf("%q: %+v", line, s.Result)
		}
	}
}

func TestTheHelpersOfTypedLines(t *testing.T) {
	for class, want := range map[string]bool{"Xfce4-terminal": true, "xterm": true, "gnome-terminal-server": true, "konsole": true, "Mousepad": false, "Chromium": false, "": false} {
		if isTerminal(class) != want {
			t.Errorf("%q: %v", class, !want)
		}
	}
	for keys, want := range map[string]bool{"Return": true, "return": true, "Enter": true, "KP_Enter": true, "ctrl+j": true, "ctrl+m": true, " Return ": true, "ctrl+l": false, "a": false} {
		if endsTheLine(keys) != want {
			t.Errorf("endsTheLine(%q) = %v", keys, !want)
		}
	}
	for keys, want := range map[string]bool{"ctrl+c": true, "ctrl+u": true, "Escape": true, "ctrl+d": true, "Return": false, "ctrl+l": false} {
		if clearsTheLine(keys) != want {
			t.Errorf("clearsTheLine(%q) = %v", keys, !want)
		}
	}
	lines, rest := splitLines("one\r\ntwo\nthree")
	if strings.Join(lines, "|") != "one|two" || rest != "three" {
		t.Errorf("%v %q", lines, rest)
	}
	if lines, rest := splitLines("one\n"); len(lines) != 1 || rest != "" {
		t.Errorf("%v %q", lines, rest)
	}
	if lines, rest := splitLines("old\rmac"); strings.Join(lines, "|") != "old" || rest != "mac" {
		t.Errorf("%v %q", lines, rest)
	}
}
