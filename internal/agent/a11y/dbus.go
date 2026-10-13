// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Names of AT-SPI on D-Bus.
const (
	ifaceAccessible = "org.a11y.atspi.Accessible"
	ifaceComponent  = "org.a11y.atspi.Component"
	ifaceAction     = "org.a11y.atspi.Action"
	ifaceText       = "org.a11y.atspi.Text"
	ifaceValue      = "org.a11y.atspi.Value"
	ifaceEditable   = "org.a11y.atspi.EditableText"
	ifaceProps      = "org.freedesktop.DBus.Properties"

	launcherName = "org.a11y.Bus"
	launcherPath = "/org/a11y/bus"
	registryName = "org.a11y.atspi.Registry"
	registryRoot = "/org/a11y/atspi/accessible/root"
	// nullPath is how AT-SPI says "no object".
	nullPath = "/org/a11y/atspi/null"

	// coordScreen asks for extents relative to the screen (ATSPI_COORD_TYPE_SCREEN).
	coordScreen uint32 = 0

	// callTimeout bounds one call: an application that does not answer (it is frozen, or busy in a dialog) must not
	// hold the agent.
	callTimeout = 4 * time.Second
	dialTimeout = 5 * time.Second
)

// dbusBus is the accessibility bus as AT-SPI speaks it.
type dbusBus struct {
	conn    *dbus.Conn
	timeout time.Duration
}

// DialSession connects to the session bus at address and from there to the accessibility bus.
func DialSession(ctx context.Context, address string) (Bus, error) {
	session, err := connect(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("%w: the session bus: %w", ErrNoBus, err)
	}
	defer session.Close()
	cctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var addr string
	if err := session.Object(launcherName, launcherPath).CallWithContext(cctx, launcherName+".GetAddress", 0).Store(&addr); err != nil {
		return nil, fmt.Errorf("%w: asking the session for the accessibility bus: %w", ErrNoBus, err)
	}
	if addr == "" {
		return nil, fmt.Errorf("%w: the session returned no address", ErrNoBus)
	}
	return DialAddress(ctx, addr)
}

// DialAddress connects to an accessibility bus at a known address.
func DialAddress(ctx context.Context, address string) (Bus, error) {
	conn, err := connect(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoBus, err)
	}
	return &dbusBus{conn: conn, timeout: callTimeout}, nil
}

// connect dials, authenticates and says hello, within the dial timeout. The context passed to godbus would tie the
// life of the connection to it, so the wait is done here instead.
func connect(ctx context.Context, address string) (*dbus.Conn, error) {
	type result struct {
		conn *dbus.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		c, err := dbus.Connect(address)
		done <- result{c, err}
	}()
	timer := time.NewTimer(dialTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.conn, r.err
	case <-ctx.Done():
		err := ctx.Err()
		go func() {
			if r := <-done; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, err
	case <-timer.C:
		go func() {
			if r := <-done; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, errors.New("timed out")
	}
}

func (b *dbusBus) obj(r Ref) dbus.BusObject { return b.conn.Object(r.Bus, dbus.ObjectPath(r.Path)) }

// call makes one method call under the call timeout.
func (b *dbusBus) call(ctx context.Context, r Ref, iface, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.obj(r).CallWithContext(ctx, iface+"."+method, 0, args...)
}

// property reads one property.
func (b *dbusBus) property(ctx context.Context, r Ref, iface, name string) (dbus.Variant, error) {
	var v dbus.Variant
	if err := b.call(ctx, r, ifaceProps, "Get", iface, name).Store(&v); err != nil {
		return v, mapErr(err)
	}
	return v, nil
}

// Dbus error names that mean something to the walker.
var (
	unsupportedErrors = map[string]bool{
		"org.freedesktop.DBus.Error.UnknownMethod":    true,
		"org.freedesktop.DBus.Error.UnknownInterface": true,
		"org.freedesktop.DBus.Error.UnknownProperty":  true,
		"org.freedesktop.DBus.Error.NotSupported":     true,
		"org.freedesktop.DBus.Error.InvalidArgs":      true,
		"org.freedesktop.DBus.Error.PropertyReadOnly": true,
	}
	goneErrors = map[string]bool{
		"org.freedesktop.DBus.Error.UnknownObject":  true,
		"org.freedesktop.DBus.Error.ServiceUnknown": true,
		"org.freedesktop.DBus.Error.NameHasNoOwner": true,
	}
)

// mapErr turns D-Bus errors into the bus errors of this package: an interface the element does not have, an element
// that no longer exists. Anything else is returned as it is.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var name string
	var de dbus.Error
	var pe *dbus.Error
	switch {
	case errors.As(err, &de):
		name = de.Name
	case errors.As(err, &pe) && pe != nil:
		name = pe.Name
	}
	switch {
	case unsupportedErrors[name]:
		return fmt.Errorf("%w (%s)", ErrUnsupported, name)
	case goneErrors[name]:
		return fmt.Errorf("%w (%s)", ErrGone, name)
	}
	return err
}

// elementRef is how AT-SPI writes a reference to an object: a bus name and a path.
type elementRef struct {
	Bus  string
	Path dbus.ObjectPath
}

func refs(in []elementRef) []Ref {
	out := make([]Ref, 0, len(in))
	for _, e := range in {
		if e.Bus == "" || e.Path == nullPath || e.Path == "" {
			continue
		}
		out = append(out, Ref{Bus: e.Bus, Path: string(e.Path)})
	}
	return out
}

func (b *dbusBus) Apps(ctx context.Context) ([]App, error) {
	var kids []elementRef
	if err := b.call(ctx, Ref{Bus: registryName, Path: registryRoot}, ifaceAccessible, "GetChildren").Store(&kids); err != nil {
		return nil, mapErr(err)
	}
	list := refs(kids)
	apps := make([]App, len(list))
	ok := make([]bool, len(list))
	var wg sync.WaitGroup
	for i, r := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := App{Ref: r}
			if v, err := b.property(ctx, r, ifaceAccessible, "Name"); err == nil {
				a.Name, _ = v.Value().(string)
			} else if errors.Is(err, ErrGone) {
				return
			}
			cctx, cancel := context.WithTimeout(ctx, b.timeout)
			defer cancel()
			var pid uint32
			if err := b.conn.BusObject().CallWithContext(cctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, r.Bus).Store(&pid); err == nil {
				a.PID = int(pid)
			}
			apps[i], ok[i] = a, true
		}()
	}
	wg.Wait()
	out := apps[:0]
	for i, a := range apps {
		if ok[i] {
			out = append(out, a)
		}
	}
	return out, nil
}

// Info asks for the role, the states and the accessible properties at once: three calls in flight together, so an
// element costs one round trip.
func (b *dbusBus) Info(ctx context.Context, r Ref) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	o := b.obj(r)
	role := o.GoWithContext(ctx, ifaceAccessible+".GetRole", 0, nil)
	state := o.GoWithContext(ctx, ifaceAccessible+".GetState", 0, nil)
	props := o.GoWithContext(ctx, ifaceProps+".GetAll", 0, nil, ifaceAccessible)
	<-role.Done
	<-state.Done
	<-props.Done

	var info Info
	var roleN uint32
	if err := role.Store(&roleN); err != nil {
		return info, mapErr(err)
	}
	var words []uint32
	if err := state.Store(&words); err != nil {
		return info, mapErr(err)
	}
	var all map[string]dbus.Variant
	if err := props.Store(&all); err != nil {
		return info, mapErr(err)
	}
	info.Role = roleN
	info.State = NewStateSet(words)
	if v, ok := all["Name"]; ok {
		info.Name, _ = v.Value().(string)
	}
	if v, ok := all["Description"]; ok {
		info.Description, _ = v.Value().(string)
	}
	if v, ok := all["ChildCount"]; ok {
		if n, ok := v.Value().(int32); ok {
			info.ChildCount = int(n)
		}
	}
	return info, nil
}

func (b *dbusBus) Children(ctx context.Context, r Ref) ([]Ref, error) {
	var kids []elementRef
	if err := b.call(ctx, r, ifaceAccessible, "GetChildren").Store(&kids); err != nil {
		return nil, mapErr(err)
	}
	return refs(kids), nil
}

func (b *dbusBus) Extents(ctx context.Context, r Ref) (x, y, w, h int, err error) {
	var e struct{ X, Y, W, H int32 }
	if err := b.call(ctx, r, ifaceComponent, "GetExtents", coordScreen).Store(&e); err != nil {
		return 0, 0, 0, 0, mapErr(err)
	}
	return int(e.X), int(e.Y), int(e.W), int(e.H), nil
}

func (b *dbusBus) Actions(ctx context.Context, r Ref) ([]string, error) {
	var acts []struct{ Name, Description, KeyBinding string }
	if err := b.call(ctx, r, ifaceAction, "GetActions").Store(&acts); err != nil {
		return nil, mapErr(err)
	}
	names := make([]string, len(acts))
	for i, a := range acts {
		names[i] = a.Name
	}
	return names, nil
}

func (b *dbusBus) Text(ctx context.Context, r Ref, max int) (string, error) {
	v, err := b.property(ctx, r, ifaceText, "CharacterCount")
	if err != nil {
		return "", err
	}
	count, _ := v.Value().(int32)
	end := min(int(count), max)
	if end <= 0 {
		return "", nil
	}
	var s string
	if err := b.call(ctx, r, ifaceText, "GetText", int32(0), int32(end)).Store(&s); err != nil { //nolint:gosec // end is at most the character count, an int32
		return "", mapErr(err)
	}
	return s, nil
}

func (b *dbusBus) Number(ctx context.Context, r Ref) (float64, error) {
	v, err := b.property(ctx, r, ifaceValue, "CurrentValue")
	if err != nil {
		return 0, err
	}
	f, ok := v.Value().(float64)
	if !ok {
		return 0, ErrUnsupported
	}
	return f, nil
}

func (b *dbusBus) DoAction(ctx context.Context, r Ref, index int) (bool, error) {
	var done bool
	if err := b.call(ctx, r, ifaceAction, "DoAction", int32(index)).Store(&done); err != nil { //nolint:gosec // an action index is a small number
		return false, mapErr(err)
	}
	return done, nil
}

func (b *dbusBus) GrabFocus(ctx context.Context, r Ref) (bool, error) {
	var took bool
	if err := b.call(ctx, r, ifaceComponent, "GrabFocus").Store(&took); err != nil {
		return false, mapErr(err)
	}
	return took, nil
}

func (b *dbusBus) SetText(ctx context.Context, r Ref, text string) (bool, error) {
	var done bool
	if err := b.call(ctx, r, ifaceEditable, "SetTextContents", text).Store(&done); err != nil {
		return false, mapErr(err)
	}
	return done, nil
}

func (b *dbusBus) Close() error { return b.conn.Close() }
