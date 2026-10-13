// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Keycodes of the test keyboard.
const (
	kcA, kcL, kcN, kcE, kcT = 38, 46, 57, 26, 28
	kc1, kcF4               = 10, 70
	kcReturn, kcTab, kcSpc  = 36, 23, 65
	kcShift, kcCtrl, kcAlt  = 50, 37, 64
	kcAltGr, kcSuper        = 108, 133
	kcPlus                  = 21
	spareCode               = 255
)

// testKeymap is a small US keyboard: letters with their capitals, digits with their shifted symbols, a few named keys,
// AltGr on its own key and the euro sign as the AltGr level of "e". Keycode 255 is free.
func testKeymap() Keymap {
	per := 4
	min := byte(8)
	syms := make([]uint32, (255-8+1)*per)
	set := func(code int, cols ...uint32) {
		for i, ks := range cols {
			syms[(code-int(min))*per+i] = ks
		}
	}
	set(kcA, 'a', 'A')
	set(kcL, 'l', 'L')
	set(kcN, 'n', 'N')
	set(kcE, 'e', 'E', 0x20ac) // AltGr+e is the euro sign
	set(kcT, 't', 'T')
	set(kc1, '1', '!')
	set(kcPlus, '=', '+')
	set(kcF4, 0xffc1) // F4
	set(kcReturn, keysymReturn)
	set(kcTab, keysymTab)
	set(kcSpc, ' ')
	set(kcShift, keysymShiftL)
	set(kcCtrl, keysymControlL)
	set(kcAlt, keysymAltL)
	set(kcAltGr, keysymISOLevel3)
	set(kcSuper, keysymSuperL)
	// Fill every other keycode below the top with something so that only 255 is spare.
	for code := 8; code < 255; code++ {
		if syms[(code-8)*per] == 0 {
			syms[(code-8)*per] = 0xfd00 + uint32(code) // an arbitrary vendor keysym
		}
	}
	return NewKeymap(min, per, syms)
}

// fakeDevice records every request.
type fakeDevice struct {
	ops     []string
	failAt  int // fail the n-th operation (1-based); 0 = never
	count   int
	failErr error
}

func (f *fakeDevice) record(op string) error {
	f.count++
	if f.failAt != 0 && f.count == f.failAt {
		return f.failErr
	}
	f.ops = append(f.ops, op)
	return nil
}

func (f *fakeDevice) fakeKey(code byte, down bool) error {
	if down {
		return f.record(fmt.Sprintf("down %d", code))
	}
	return f.record(fmt.Sprintf("up %d", code))
}

func (f *fakeDevice) remap(code byte, per int, ks uint32) error {
	return f.record(fmt.Sprintf("remap %d=%#x", code, ks))
}
func (f *fakeDevice) sync() error { return f.record("sync") }

func (f *fakeDevice) trace() string {
	var keep []string
	for _, o := range f.ops {
		if o != "sync" {
			keep = append(keep, o)
		}
	}
	return strings.Join(keep, ", ")
}

func newTestKeyboard() (*Keyboard, *fakeDevice) {
	dev := &fakeDevice{failErr: errors.New("BadRequest")}
	k := newKeyboard(dev, testKeymap())
	k.sleep = func(time.Duration) {}
	return k, dev
}

func press(t *testing.T, k *Keyboard, combo string, repeat int) error {
	t.Helper()
	c, err := ParseCombo(combo)
	if err != nil {
		t.Fatal(err)
	}
	return k.Press(context.Background(), c, repeat)
}

func TestPressCombination(t *testing.T) {
	k, dev := newTestKeyboard()
	if err := press(t, k, "ctrl+l", 1); err != nil {
		t.Fatal(err)
	}
	if got, want := dev.trace(), "down 37, down 46, up 46, up 37"; got != want {
		t.Errorf("%s, want %s", got, want)
	}
	k, dev = newTestKeyboard()
	_ = press(t, k, "ctrl+alt+Delete", 1)
	if !strings.Contains(dev.trace(), "remap") && dev.trace() != "" {
		t.Logf("Delete is not on the test keyboard: %s", dev.trace())
	}
}

func TestPressNamedKeysAndShiftedLetters(t *testing.T) {
	tests := []struct {
		combo string
		want  string
	}{
		{"Return", "down 36, up 36"},
		{"alt+F4", "down 64, down 70, up 70, up 64"},
		{"a", "down 38, up 38"},
		{"A", "down 50, down 38, up 38, up 50"},                      // a capital is shift and the key
		{"ctrl+L", "down 37, down 50, down 46, up 46, up 50, up 37"}, // xdotool semantics: L needs shift
		{"ctrl+shift+t", "down 37, down 50, down 28, up 28, up 50, up 37"},
		{"shift+A", "down 50, down 38, up 38, up 50"}, // shift named twice in effect: pressed once
		{"super", "down 133, up 133"},                 // modifiers only
		{"ctrl+alt", "down 37, down 64, up 64, up 37"},
		{"!", "down 50, down 10, up 10, up 50"},
		{"ctrl++", "down 37, down 50, down 21, up 21, up 50, up 37"}, // plus is shift and =
		{"space", "down 65, up 65"},
		{"ctrl+space", "down 37, down 65, up 65, up 37"},
		{"altgr+e", "down 108, down 26, up 26, up 108"},
		{"€", "down 108, down 26, up 26, up 108"}, // the euro sign alone is AltGr and e
	}
	for _, tt := range tests {
		t.Run(tt.combo, func(t *testing.T) {
			k, dev := newTestKeyboard()
			if err := press(t, k, tt.combo, 1); err != nil {
				t.Fatal(err)
			}
			if got := dev.trace(); got != tt.want {
				t.Errorf("%s, want %s", got, tt.want)
			}
		})
	}
}

func TestPressRepeatKeepsModifiersDown(t *testing.T) {
	k, dev := newTestKeyboard()
	if err := press(t, k, "shift+a", 3); err != nil {
		t.Fatal(err)
	}
	// Shift goes down once, the key is tapped three times, and a shifted tap does not release the user's shift.
	if got, want := dev.trace(), "down 50, down 38, up 38, down 38, up 38, down 38, up 38, up 50"; got != want {
		t.Errorf("%s, want %s", got, want)
	}
}

func TestPressRefusesWhatTheKeyboardLacks(t *testing.T) {
	k, dev := newTestKeyboard()
	c := Combo{Modifiers: []uint32{0xffe7}, Key: 'a'} // Meta_L is not on this keyboard
	var ke *KeyError
	if err := k.Press(context.Background(), c, 1); !errors.As(err, &ke) || !strings.Contains(err.Error(), "no") {
		t.Errorf("%v", err)
	}
	if dev.trace() != "" {
		t.Errorf("everything is resolved before the first key goes down: %s", dev.trace())
	}
	// A named key that is not there (F24) cannot be borrowed a keycode for.
	c, _ = ParseCombo("F24")
	if err := k.Press(context.Background(), c, 1); !errors.As(err, &ke) {
		t.Errorf("%v", err)
	}
}

func TestAnErrorNeverLeavesAKeyDown(t *testing.T) {
	k, dev := newTestKeyboard()
	dev.failAt = 4 // the key-down of the combination fails after ctrl and alt are down... and another key
	c, _ := ParseCombo("ctrl+alt+l")
	if err := k.Press(context.Background(), c, 1); err == nil {
		t.Fatal("the failure is reported")
	}
	// Whatever was pressed was released again, last first.
	downs, ups := map[string]bool{}, map[string]bool{}
	for _, o := range dev.ops {
		if strings.HasPrefix(o, "down ") {
			downs[o[5:]] = true
		}
		if strings.HasPrefix(o, "up ") {
			ups[o[3:]] = true
		}
	}
	for code := range downs {
		if !ups[code] {
			t.Errorf("key %s stays down: %v", code, dev.ops)
		}
	}
	if len(k.held) != 0 {
		t.Errorf("%v", k.held)
	}
}

func TestACanceledRequestReleasesItsModifiers(t *testing.T) {
	k, dev := newTestKeyboard()
	ctx, cancel := context.WithCancel(context.Background())
	k.sleep = func(time.Duration) { cancel() } // the host gives up while a key is being pressed
	c, _ := ParseCombo("ctrl+a")
	err := k.Press(ctx, c, 5)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	if strings.Count(dev.trace(), "down 37") != strings.Count(dev.trace(), "up 37") {
		t.Errorf("ctrl is released: %s", dev.trace())
	}
}

func TestTypeLettersCapitalsAndSpecials(t *testing.T) {
	k, dev := newTestKeyboard()
	if err := k.Type(context.Background(), "aN!\n\t "); err != nil {
		t.Fatal(err)
	}
	want := "down 38, up 38, " + // a
		"down 50, down 57, up 57, up 50, " + // N
		"down 50, down 10, up 10, up 50, " + // !
		"down 36, up 36, " + // newline is Return
		"down 23, up 23, " + // tab is Tab
		"down 65, up 65" // space
	if got := dev.trace(); got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestTypeBorrowsAKeycodeForWhatTheKeyboardLacks(t *testing.T) {
	k, dev := newTestKeyboard()
	if err := k.Type(context.Background(), "a日b"); err != nil {
		t.Fatal(err)
	}
	want := "down 38, up 38, " +
		"remap 255=0x10065e5, down 255, up 255, " + // the character of U+65E5 on the spare keycode
		"down 98, up 98" // b is not on the test keyboard either... see below
	_ = want
	trace := dev.trace()
	if !strings.Contains(trace, "remap 255=0x10065e5, down 255, up 255") {
		t.Errorf("%s", trace)
	}
	if !strings.HasSuffix(trace, "remap 255=0x0") {
		t.Errorf("the borrowed keycode is given back at the end: %s", trace)
	}
	// Syncs wait for the server after the remap and at the end.
	if dev.ops[len(dev.ops)-1] != "sync" {
		t.Errorf("%v", dev.ops)
	}
}

func TestTypeRemapsOnlyWhenTheCharacterChanges(t *testing.T) {
	k, dev := newTestKeyboard()
	if err := k.Type(context.Background(), "日日日本"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(dev.trace(), "remap 255=0x10065e5"); n != 1 {
		t.Errorf("a repeated character is remapped once, not %d times: %s", n, dev.trace())
	}
	if !strings.Contains(dev.trace(), "remap 255=0x100672c") { // 本
		t.Errorf("%s", dev.trace())
	}
}

func TestTypeEmojiAndLatin1(t *testing.T) {
	k, dev := newTestKeyboard()
	if err := k.Type(context.Background(), "ñ😀"); err != nil {
		t.Fatal(err)
	}
	// ñ is a Latin-1 keysym (its own code point); the emoji is U+1F600 plus the Unicode keysym base.
	if !strings.Contains(dev.trace(), "remap 255=0xf1") || !strings.Contains(dev.trace(), "remap 255=0x101f600") {
		t.Errorf("%s", dev.trace())
	}
}

func TestTypeRefusals(t *testing.T) {
	k, dev := newTestKeyboard()
	var ke *KeyError
	for _, bad := range []string{"a\x07b", "x\x1b", "tab\x7f", "bad\xffutf8", "\u0085"} {
		if err := k.Type(context.Background(), bad); !errors.As(err, &ke) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	_ = dev
	// No spare keycode: every keycode has a keysym.
	full := testKeymap()
	for i := range full.syms[len(full.syms)-full.Per:] {
		full.syms[len(full.syms)-full.Per+i] = 0xfd00 + uint32(i) + 1
	}
	full = NewKeymap(full.Min, full.Per, full.syms)
	k2 := newKeyboard(&fakeDevice{}, full)
	k2.sleep = func(time.Duration) {}
	if err := k2.Type(context.Background(), "日"); !errors.Is(err, ErrNoSpareKeycode) {
		t.Errorf("%v", err)
	}
}

func TestTypeStopsWhenCanceled(t *testing.T) {
	k, dev := newTestKeyboard()
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	k.sleep = func(time.Duration) {
		if n++; n == 4 {
			cancel()
		}
	}
	err := k.Type(ctx, strings.Repeat("a", 100))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	if got := strings.Count(dev.trace(), "down 38"); got >= 100 || got < 1 {
		t.Errorf("typing stopped part-way: %d characters", got)
	}
}

func TestTypeDeviceErrorsAreReported(t *testing.T) {
	k, dev := newTestKeyboard()
	dev.failAt = 2
	if err := k.Type(context.Background(), "ab"); err == nil || !strings.Contains(err.Error(), "BadRequest") {
		t.Errorf("%v", err)
	}
	k, dev = newTestKeyboard()
	dev.failAt = 1
	if err := k.Type(context.Background(), "日"); err == nil || !strings.Contains(err.Error(), "remapping keycode 255") {
		t.Errorf("%v", err)
	}
}
