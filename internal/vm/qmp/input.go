// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qmp

import (
	"fmt"
	"regexp"
)

// absMax is the top of QEMU's absolute pointer range (INPUT_EVENT_ABS_MAX).
const absMax = 0x7fff

// InputEvent is one entry of input-send-event: {"type": ..., "data": ...}.
type InputEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Buttons input-send-event knows.
var buttons = map[string]bool{
	"left": true, "middle": true, "right": true, "wheel-up": true, "wheel-down": true,
	"side": true, "extra": true, "wheel-left": true, "wheel-right": true,
}

var reQCode = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// MoveTo returns the two events that put the pointer at pixel (x, y) of a screen w by h pixels. QEMU's absolute
// axes run from 0 to 0x7fff over the whole screen; out-of-range positions are clamped to the screen.
func MoveTo(x, y, w, h int) ([]InputEvent, error) {
	if w < 2 || h < 2 {
		return nil, fmt.Errorf("a screen of %dx%d pixels", w, h)
	}
	scale := func(v, size int) int {
		v = max(0, min(v, size-1))
		return (v*absMax + (size-1)/2) / (size - 1)
	}
	return []InputEvent{
		{Type: "abs", Data: map[string]any{"axis": "x", "value": scale(x, w)}},
		{Type: "abs", Data: map[string]any{"axis": "y", "value": scale(y, h)}},
	}, nil
}

// Button returns a mouse button press or release.
func Button(name string, down bool) (InputEvent, error) {
	if !buttons[name] {
		return InputEvent{}, fmt.Errorf("unknown mouse button %q", name)
	}
	return InputEvent{Type: "btn", Data: map[string]any{"down": down, "button": name}}, nil
}

// Key returns a key press or release by QEMU key code (qcode), e.g. "ret", "ctrl", "a", "f5".
func Key(qcode string, down bool) (InputEvent, error) {
	if !reQCode.MatchString(qcode) {
		return InputEvent{}, fmt.Errorf("%q is not a QEMU key code", qcode)
	}
	return InputEvent{Type: "key", Data: map[string]any{
		"down": down,
		"key":  map[string]any{"type": "qcode", "data": qcode},
	}}, nil
}
