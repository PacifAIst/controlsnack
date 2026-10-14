// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"context"
	"errors"
	"fmt"

	"time"
	"unicode/utf8"
)

// ErrNoSpareKeycode means a character is not on the keyboard and there is no free keycode to borrow for it.
var ErrNoSpareKeycode = errors.New("the keyboard has no free keycode to type this character with")

// Timing of synthetic key events: one key's press and release are apart by about this much (xdotool uses 12 ms), so
// that applications that poll the keyboard state see both.
const (
	defaultKeyDelay    = 10 * time.Millisecond
	defaultRemapSettle = 15 * time.Millisecond // clients refresh their keymap on MappingNotify; give them a moment
)

// Keycodes borrowed for characters the keyboard lacks (D-68). An application looks a key event's keycode up in the
// server's mapping when it handles the event, which on a slow machine or a busy application can be long after the
// event was sent: a keycode that is given another character in between types the wrong one (日本語 came out as 日語語).
// So every character gets a keycode of its own, the mapping is kept after the request, and a keycode is only taken over
// by another character after it has been unused for a while.
const (
	maxLent    = 64
	lentMinAge = 2 * time.Second
)

type lentSlot struct {
	code byte
	sym  uint32
	used time.Time
}

// lentKeys is the keycodes the agent has borrowed and the character each carries. It outlives the requests: a Conn
// keeps one, and each request's Keyboard works on it.
type lentKeys struct{ slots []lentSlot }

func (l *lentKeys) find(keysym uint32) (*lentSlot, bool) {
	for i := range l.slots {
		if l.slots[i].sym == keysym {
			return &l.slots[i], true
		}
	}
	return nil, false
}

func (l *lentKeys) has(code byte) bool {
	for i := range l.slots {
		if l.slots[i].code == code {
			return true
		}
	}
	return false
}

// device is what the keyboard needs from the X server: the real one is the XTEST extension plus the core keyboard
// mapping requests, a fake one records what was asked.
type device interface {
	fakeKey(code byte, down bool) error
	// remap sets every column of a keycode to one keysym (0 clears it).
	remap(code byte, per int, keysym uint32) error
	sync() error
}

// Keyboard produces key events for one request. It is not safe for concurrent use; the agent's input pool runs one
// request at a time.
type Keyboard struct {
	dev    device
	km     Keymap
	delay  time.Duration
	settle time.Duration
	sleep  func(time.Duration)
	now    func() time.Time

	held []byte // keys that are down, so an error cannot leave one stuck
	lent *lentKeys
}

// newKeyboard makes the keyboard of one request. lent is the borrowed keycodes of the connection (nil: none yet).
func newKeyboard(dev device, km Keymap, lent *lentKeys) *Keyboard {
	if lent == nil {
		lent = &lentKeys{}
	}
	return &Keyboard{dev: dev, km: km, delay: defaultKeyDelay, settle: defaultRemapSettle, sleep: time.Sleep, now: time.Now, lent: lent}
}

func (k *Keyboard) down(code byte) error {
	if err := k.dev.fakeKey(code, true); err != nil {
		return err
	}
	k.held = append(k.held, code)
	return nil
}

func (k *Keyboard) up(code byte) error {
	// The key leaves the held list only once the server has been told: if the release fails, finish tries again.
	if err := k.dev.fakeKey(code, false); err != nil {
		return err
	}
	for i := len(k.held) - 1; i >= 0; i-- {
		if k.held[i] == code {
			k.held = append(k.held[:i], k.held[i+1:]...)
			break
		}
	}
	return nil
}

// finish releases whatever is still held (last pressed first). It runs after every request, successful or not: a
// modifier that stays down would turn the user's next keystrokes into shortcuts. The borrowed keycodes keep their
// characters (see lentMinAge).
func (k *Keyboard) finish() error {
	var first error
	for len(k.held) > 0 {
		code := k.held[len(k.held)-1]
		k.held = k.held[:len(k.held)-1]
		if err := k.dev.fakeKey(code, false); err != nil && first == nil {
			first = err
		}
	}
	if err := k.dev.sync(); err != nil && first == nil {
		first = err
	}
	return first
}

// strokeFor finds how to type a keysym, borrowing a keycode when the keyboard does not have it.
func (k *Keyboard) strokeFor(keysym uint32) (Stroke, error) {
	for _, ks := range keysymAlternates(keysym) {
		if slot, ok := k.lent.find(ks); ok { // borrowed before, possibly by an earlier request
			slot.used = k.now()
			return Stroke{Code: slot.code}, nil
		}
		if s, ok := k.km.Lookup(ks); ok {
			return s, nil
		}
	}
	if keysym < 0x100 || keysym >= unicodeKeysymBase { // a character: it can be typed by remapping
		code, err := k.borrow(keysym)
		if err != nil {
			return Stroke{}, err
		}
		return Stroke{Code: code}, nil
	}
	return Stroke{}, &KeyError{fmt.Sprintf("this keyboard has no key for keysym %#x", keysym)}
}

// borrow gives a character a keycode of its own: a free one while there are some (up to maxLent), then the one that has
// been unused the longest, after it has been unused for lentMinAge.
func (k *Keyboard) borrow(keysym uint32) (byte, error) {
	l := k.lent
	var slot *lentSlot
	if len(l.slots) < maxLent {
		for _, code := range k.km.Spares(maxLent + len(l.slots)) {
			if !l.has(code) {
				l.slots = append(l.slots, lentSlot{code: code})
				slot = &l.slots[len(l.slots)-1]
				break
			}
		}
	}
	if slot == nil {
		if len(l.slots) == 0 {
			return 0, ErrNoSpareKeycode
		}
		oldest := 0
		for i := range l.slots {
			if l.slots[i].used.Before(l.slots[oldest].used) {
				oldest = i
			}
		}
		slot = &l.slots[oldest]
		if wait := lentMinAge - k.now().Sub(slot.used); wait > 0 {
			k.sleep(wait)
		}
	}
	if err := k.dev.remap(slot.code, k.km.Per, keysym); err != nil {
		slot.sym = 0 // whatever it carried is not known any more
		return 0, fmt.Errorf("remapping keycode %d: %w", slot.code, err)
	}
	slot.sym = keysym
	slot.used = k.now()
	if err := k.dev.sync(); err != nil {
		return 0, err
	}
	k.sleep(k.settle)
	return slot.code, nil
}

// modifier finds a modifier's keycode.
func (k *Keyboard) modifier(keysym uint32, name string) (byte, error) {
	s, ok := k.km.Lookup(keysym)
	if !ok {
		return 0, &KeyError{fmt.Sprintf("this keyboard has no %s key", name)}
	}
	return s.Code, nil
}

// tap presses and releases one stroke, holding the Shift and AltGr that the stroke itself needs for exactly as long as
// the key is down. Modifiers of a combination that were pressed before are not touched.
func (k *Keyboard) tap(s Stroke) error {
	var own []byte // what this tap pressed, to release again
	press := func(keysym uint32, name string) error {
		code, err := k.modifier(keysym, name)
		if err != nil {
			return err
		}
		if err := k.down(code); err != nil {
			return err
		}
		own = append(own, code)
		return nil
	}
	if s.Level3 {
		if err := press(keysymISOLevel3, "AltGr"); err != nil {
			return err
		}
	}
	if s.Shift {
		if err := press(keysymShiftL, "Shift"); err != nil {
			return err
		}
	}
	if err := k.down(s.Code); err != nil {
		return err
	}
	k.sleep(k.delay)
	if err := k.up(s.Code); err != nil {
		return err
	}
	k.sleep(k.delay)
	for i := len(own) - 1; i >= 0; i-- {
		if err := k.up(own[i]); err != nil {
			return err
		}
	}
	return nil
}

// Press presses a combination: its modifiers go down in order, the key is tapped repeat times, the modifiers come up
// in reverse order.
func (k *Keyboard) Press(ctx context.Context, c Combo, repeat int) (err error) {
	if repeat < 1 {
		repeat = 1
	}
	defer func() {
		if ferr := k.finish(); err == nil {
			err = ferr
		}
	}()
	var mods []byte
	for _, ks := range c.Modifiers { // resolve everything before pressing anything
		code, err := k.modifier(ks, keysymName(ks))
		if err != nil {
			return err
		}
		mods = append(mods, code)
	}
	var stroke Stroke
	if c.Key != 0 {
		if stroke, err = k.strokeFor(c.Key); err != nil {
			return err
		}
	}
	for _, code := range mods {
		if err := k.down(code); err != nil {
			return err
		}
		k.sleep(k.delay)
	}
	if c.Key == 0 {
		return nil // modifiers only: down, then finish releases them
	}
	// Shift is already part of a combination that names it; do not press it twice.
	if stroke.Shift {
		if sc, ok := k.km.Lookup(keysymShiftL); ok && contains(mods, sc.Code) {
			stroke.Shift = false
		}
	}
	for i := 0; i < repeat; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := k.tap(stroke); err != nil {
			return err
		}
	}
	return nil
}

func contains(list []byte, v byte) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// keysymName names a modifier keysym for messages.
func keysymName(ks uint32) string {
	for name, v := range modifierKeysyms {
		if v == ks {
			return name
		}
	}
	return fmt.Sprintf("%#x", ks)
}

// Type types text into whatever has the focus. A newline is Return and a tab is Tab; any other control character is
// refused. Characters the keyboard does not have are typed through a borrowed keycode (the method of xdotool type).
func (k *Keyboard) Type(ctx context.Context, text string) (err error) {
	if !utf8.ValidString(text) {
		return &KeyError{"the text is not valid UTF-8"}
	}
	defer func() {
		if ferr := k.finish(); err == nil {
			err = ferr
		}
	}()
	for _, r := range text {
		if err := ctx.Err(); err != nil {
			return err
		}
		var s Stroke
		switch r {
		case '\n', '\r':
			if s, err = k.strokeFor(keysymReturn); err != nil {
				return err
			}
		case '\t':
			if s, err = k.strokeFor(keysymTab); err != nil {
				return err
			}
		default:
			ks, kerr := RuneKeysym(r)
			if kerr != nil {
				return kerr
			}
			if s, err = k.strokeFor(ks); err != nil {
				return err
			}
		}
		if err := k.tap(s); err != nil {
			return err
		}
	}
	return nil
}
