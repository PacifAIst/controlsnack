// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jezek/xgb/xproto"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
)

// Limits on text that comes from other programs' windows: titles and classes are whatever an application chose to
// call itself.
const (
	maxTitleRunes = 256
	maxClassRunes = 128
)

var reWindowID = regexp.MustCompile(`^0x[0-9a-f]{1,8}$`)

// FormatWindowID renders a window id the way the protocol carries it: "0x" and lower-case hex.
func FormatWindowID(id uint32) string { return fmt.Sprintf("0x%x", id) }

// ParseWindowID reads an id made by FormatWindowID. Nothing else is accepted: the host never invents ids.
func ParseWindowID(s string) (uint32, error) {
	if !reWindowID.MatchString(s) {
		return 0, fmt.Errorf("%q is not a window id (0x and hex digits, as listed by the windows request)", s)
	}
	v, err := strconv.ParseUint(s[2:], 16, 32)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("%q is not a window id", s)
	}
	return uint32(v), nil
}

// cardinals decodes the value of a format-32 property (the X server is little endian here, checked in checkFormat).
func cardinals(value []byte) []uint32 {
	out := make([]uint32, 0, len(value)/4)
	for i := 0; i+4 <= len(value); i += 4 {
		out = append(out, binary.LittleEndian.Uint32(value[i:]))
	}
	return out
}

// sanitize makes text from a window safe to show: invalid UTF-8 and control characters are dropped, runs of white
// space become one space, and the length is capped.
func sanitize(s string, maxRunes int) string { return textclean.Line(s, maxRunes) }

// textProperty reads a single string out of a property value: the part before the first NUL, cleaned.
func textProperty(value []byte, maxRunes int) string {
	if i := indexNUL(value); i >= 0 {
		value = value[:i]
	}
	return sanitize(string(value), maxRunes)
}

func indexNUL(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}

// parseWMClass splits WM_CLASS ("instance\0Class\0") and returns the class, which names the program ("Xfce4-terminal",
// "Thunar"); the instance only when the class is missing.
func parseWMClass(value []byte) string {
	parts := strings.Split(string(value), "\x00")
	var class string
	switch {
	case len(parts) >= 2 && parts[1] != "":
		class = parts[1]
	case len(parts) >= 1:
		class = parts[0]
	}
	return sanitize(class, maxClassRunes)
}

// frameBounds is a window's outer rectangle: the client area (x, y, w, h in screen pixels) plus the frame the window
// manager draws around it (_NET_FRAME_EXTENTS: left, right, top, bottom).
func frameBounds(x, y, w, h int, left, right, top, bottom uint32) [4]int {
	return [4]int{x - int(left), y - int(top), w + int(left) + int(right), h + int(top) + int(bottom)}
}

// clientMessage builds the 32-byte ClientMessage event that asks the window manager for something (EWMH): format 32,
// five 32-bit values.
func clientMessage(window uint32, typ xproto.Atom, data [5]uint32) string {
	ev := xproto.ClientMessageEvent{Format: 32, Window: xproto.Window(window), Type: typ,
		Data: xproto.ClientMessageDataUnionData32New(data[:])}
	return string(ev.Bytes())
}
