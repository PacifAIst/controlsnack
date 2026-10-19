// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/vm/agentclient"
)

// TestE2EOCR reads the real guest's screen with tesseract through the agent: a terminal shows known text, and the lines
// that come back must contain it, with boxes inside the terminal's window.
func TestE2EOCR(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	if !slices.Contains(m.Ack.Capabilities, protocol.CapOCR) {
		t.Fatalf("no ocr capability: %v", m.Ack.Capabilities)
	}
	time.Sleep(20 * time.Second)

	var res protocol.ExecRes
	cmd := `(setsid xfce4-terminal --geometry=70x12 -e "bash -c 'clear; echo ControlSnack OCR test line 12345; echo Second line: Hello World; echo; echo File Edit View Go Help; sleep 900'" >/dev/null 2>&1 &); echo launched`
	if _, err := m.Client.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: cmd}, nil, &res); err != nil || res.Stdout != "launched\n" {
		t.Fatalf("%+v %v", res, err)
	}
	var term *protocol.Window
	deadline := time.Now().Add(3 * time.Minute)
	for term == nil {
		if time.Now().After(deadline) {
			t.Fatal("the terminal did not open")
		}
		time.Sleep(5 * time.Second)
		var ws protocol.WindowsRes
		if _, err := m.Client.Call(ctx, protocol.TypeWindows, protocol.WindowsReq{Op: "list"}, nil, &ws); err != nil {
			t.Fatal(err)
		}
		for i := range ws.Windows {
			if strings.EqualFold(ws.Windows[i].Class, "Xfce4-terminal") {
				term = &ws.Windows[i]
			}
		}
	}
	time.Sleep(8 * time.Second) // the text is drawn
	t.Logf("terminal: %+v", *term)

	read := func(req protocol.OCRReq) (protocol.OCRRes, time.Duration) {
		t.Helper()
		var out protocol.OCRRes
		begin := time.Now()
		if _, err := m.Client.Call(ctx, protocol.TypeOCR, req, nil, &out); err != nil {
			t.Fatalf("ocr %+v: %v", req, err)
		}
		return out, time.Since(begin)
	}
	dump := func(o protocol.OCRRes) string {
		var b strings.Builder
		for _, l := range o.Lines {
			b.WriteString("\n  " + l.ID + " " + l.Text + "  " + fmtRect(l.Bounds) + " conf=" + itoa(l.Conf))
		}
		return b.String()
	}

	// The whole screen, budget scaled for tcg.
	full, took := read(protocol.OCRReq{BudgetMS: 90000})
	t.Logf("ocr of the screen: %d lines in %s%s", len(full.Lines), took.Round(time.Millisecond), dump(full))
	text := strings.ToLower(joinLines(full))
	for _, want := range []string{"controlsnack", "12345", "hello world"} {
		if !strings.Contains(text, want) {
			t.Errorf("the screen shows %q, but the recognized text is %q", want, text)
		}
	}
	for _, l := range full.Lines {
		if l.Bounds[2] <= 0 || l.Bounds[3] <= 0 || l.Bounds[0] < 0 || l.Bounds[1] < 0 || l.Bounds[0]+l.Bounds[2] > 1280 || l.Bounds[1]+l.Bounds[3] > 800 {
			t.Errorf("%s has bounds outside the screen: %v", l.ID, l.Bounds)
		}
	}
	var line *protocol.OCRLine
	for i := range full.Lines {
		if strings.Contains(strings.ToLower(full.Lines[i].Text), "12345") {
			line = &full.Lines[i]
		}
	}
	if line != nil && (line.Bounds[0] < term.Bounds[0] || line.Bounds[1] < term.Bounds[1] || line.Bounds[0]+line.Bounds[2] > term.Bounds[0]+term.Bounds[2] || line.Bounds[1]+line.Bounds[3] > term.Bounds[1]+term.Bounds[3]) {
		t.Errorf("the line %v is not inside the terminal %v", line.Bounds, term.Bounds)
	}

	// Asked again, the same screen gives the same answer at once.
	again, took2 := read(protocol.OCRReq{BudgetMS: 90000})
	if again.Generation != full.Generation || len(again.Lines) != len(full.Lines) || took2 > 3*time.Second {
		t.Errorf("a second reading of the same screen: generation %s vs %s, %d vs %d lines, %s", again.Generation, full.Generation, len(again.Lines), len(full.Lines), took2)
	}

	// A region: only the terminal.
	reg := protocol.Rect{term.Bounds[0], term.Bounds[1], term.Bounds[2], term.Bounds[3]}
	part, took3 := read(protocol.OCRReq{Region: &reg, BudgetMS: 90000})
	t.Logf("ocr of the terminal: %d lines in %s%s", len(part.Lines), took3.Round(time.Millisecond), dump(part))
	if !strings.Contains(strings.ToLower(joinLines(part)), "controlsnack") {
		t.Errorf("the terminal's text: %q", joinLines(part))
	}
	for _, l := range part.Lines {
		if l.Bounds[0] < reg[0]-2 || l.Bounds[1] < reg[1]-2 || l.Bounds[0]+l.Bounds[2] > reg[0]+reg[2]+2 || l.Bounds[1]+l.Bounds[3] > reg[1]+reg[3]+2 {
			t.Errorf("%s %v is outside the region %v", l.ID, l.Bounds, reg)
		}
	}
	if part.Generation == full.Generation {
		t.Errorf("another region is another generation")
	}

	// The same region at other enlargements: what each costs and what it still reads. At 1 the 10-pixel text of the
	// interface is lost in part (measured: "ControlSnack" and the menu entries disappear); 3 costs more and reads no more.
	small, tookSmall := read(protocol.OCRReq{Region: &reg, BudgetMS: 120000, Scale: 1})
	t.Logf("ocr of the terminal at scale 1: %d lines in %s%s", len(small.Lines), tookSmall.Round(time.Millisecond), dump(small))
	big, tookBig := read(protocol.OCRReq{Region: &reg, BudgetMS: 120000, Scale: 3})
	t.Logf("ocr of the terminal at scale 3: %d lines in %s%s", len(big.Lines), tookBig.Round(time.Millisecond), dump(big))
	if !strings.Contains(strings.ToLower(joinLines(big)), "12345") {
		t.Errorf("at scale 3 the terminal's text: %q", joinLines(big))
	}

	// Refusals.
	var re *agentclient.RemoteError
	for name, req := range map[string]protocol.OCRReq{
		"language not installed": {Lang: "xyz"},
		"bad language":           {Lang: "eng;rm"},
		"region too small":       {Region: &protocol.Rect{0, 0, 4, 4}},
		"region off the screen":  {Region: &protocol.Rect{1200, 700, 200, 200}},
	} {
		if _, err := m.Client.Call(ctx, protocol.TypeOCR, req, nil, nil); !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func joinLines(o protocol.OCRRes) string {
	var parts []string
	for _, l := range o.Lines {
		parts = append(parts, l.Text)
	}
	return strings.Join(parts, " | ")
}

func fmtRect(r protocol.Rect) string {
	return "[" + itoa(r[0]) + " " + itoa(r[1]) + " " + itoa(r[2]) + " " + itoa(r[3]) + "]"
}

func itoa(n int) string { return strconv.Itoa(n) }
