// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package a11y

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// This file speaks to a private dbus-daemon with a stand-in for an AT-SPI application, written from the AT-SPI
// interface definitions. It checks the calls and the decoding of the real bus implementation; the guest end-to-end
// test checks that real toolkits answer the same way.

type rect struct{ X, Y, W, H int32 }

type atNode struct {
	role      uint32
	state     []uint32
	name      string
	desc      string
	children  []string
	ext       *rect // nil: no Component
	actions   []string
	text      *string // nil: no Text
	value     *float64
	editable  bool
	focusable bool
}

type fakeApp struct {
	mu    sync.Mutex
	conn  *dbus.Conn
	nodes map[string]*atNode
	log   []string
}

func (a *fakeApp) record(s string) {
	a.mu.Lock()
	a.log = append(a.log, s)
	a.mu.Unlock()
}

func (a *fakeApp) node(path string) *atNode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nodes[path]
}

// One exported type per interface, as the AT-SPI bridges do.
type accessibleIf struct {
	app  *fakeApp
	path string
}

func (o accessibleIf) GetRole() (uint32, *dbus.Error) { return o.app.node(o.path).role, nil }
func (o accessibleIf) GetState() ([]uint32, *dbus.Error) {
	return o.app.node(o.path).state, nil
}
func (o accessibleIf) GetChildren() ([]elementRef, *dbus.Error) {
	var out []elementRef
	for _, c := range o.app.node(o.path).children {
		out = append(out, elementRef{Bus: o.app.conn.Names()[0], Path: dbus.ObjectPath(c)})
	}
	if out == nil {
		out = []elementRef{}
	}
	return out, nil
}

type propsIf struct {
	app  *fakeApp
	path string
}

func (o propsIf) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	all, derr := o.GetAll(iface)
	if derr != nil {
		return dbus.Variant{}, derr
	}
	v, ok := all[name]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", []any{name})
	}
	return v, nil
}

func (o propsIf) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	n := o.app.node(o.path)
	switch iface {
	case ifaceAccessible:
		return map[string]dbus.Variant{
			"Name": dbus.MakeVariant(n.name), "Description": dbus.MakeVariant(n.desc),
			"ChildCount": dbus.MakeVariant(int32(len(n.children))), "Locale": dbus.MakeVariant("en_US.UTF-8"),
		}, nil
	case ifaceText:
		if s := o.app.text(o.path); s != nil {
			return map[string]dbus.Variant{"CharacterCount": dbus.MakeVariant(int32(len([]rune(*s))))}, nil
		}
	case ifaceValue:
		if n.value != nil {
			return map[string]dbus.Variant{"CurrentValue": dbus.MakeVariant(*n.value)}, nil
		}
	}
	return nil, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []any{iface})
}

func (o propsIf) Set(string, string, dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", nil)
}

type componentIf struct {
	app  *fakeApp
	path string
}

func (o componentIf) GetExtents(coord uint32) (rect, *dbus.Error) {
	o.app.record("extents coord=" + string(rune('0'+coord)))
	return *o.app.node(o.path).ext, nil
}
func (o componentIf) GrabFocus() (bool, *dbus.Error) {
	o.app.record("focus " + o.path)
	return o.app.node(o.path).focusable, nil
}

type actionIf struct {
	app  *fakeApp
	path string
}

func (o actionIf) GetActions() ([]struct{ Name, Description, KeyBinding string }, *dbus.Error) {
	var out []struct{ Name, Description, KeyBinding string }
	for _, a := range o.app.node(o.path).actions {
		out = append(out, struct{ Name, Description, KeyBinding string }{a, "does " + a, ""})
	}
	return out, nil
}
func (o actionIf) DoAction(i int32) (bool, *dbus.Error) {
	acts := o.app.node(o.path).actions
	if int(i) >= len(acts) || i < 0 {
		return false, nil
	}
	o.app.record("do " + o.path + " " + acts[i])
	return true, nil
}

type textIf struct {
	app  *fakeApp
	path string
}

func (o textIf) GetText(start, end int32) (string, *dbus.Error) {
	r := []rune(*o.app.text(o.path))
	if start < 0 || end > int32(len(r)) || start > end {
		return "", dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	return string(r[start:end]), nil
}

type editableIf struct {
	app  *fakeApp
	path string
}

func (o editableIf) SetTextContents(s string) (bool, *dbus.Error) {
	n := o.app.node(o.path)
	o.app.mu.Lock()
	n.text = &s
	o.app.mu.Unlock()
	o.app.record("set " + o.path + " " + s)
	return true, nil
}

// export puts a node on the bus with the interfaces it has.
func (a *fakeApp) export(path string, n *atNode) {
	a.mu.Lock()
	a.nodes[path] = n
	a.mu.Unlock()
	p := dbus.ObjectPath(path)
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(a.conn.Export(accessibleIf{a, path}, p, ifaceAccessible))
	must(a.conn.Export(propsIf{a, path}, p, ifaceProps))
	if n.ext != nil {
		must(a.conn.Export(componentIf{a, path}, p, ifaceComponent))
	}
	if len(n.actions) > 0 {
		must(a.conn.Export(actionIf{a, path}, p, ifaceAction))
	}
	if n.text != nil {
		must(a.conn.Export(textIf{a, path}, p, ifaceText))
	}
	if n.editable {
		must(a.conn.Export(editableIf{a, path}, p, ifaceEditable))
	}
}

func (a *fakeApp) unexport(path string) {
	p := dbus.ObjectPath(path)
	for _, i := range []string{ifaceAccessible, ifaceProps, ifaceComponent, ifaceAction, ifaceText, ifaceEditable} {
		_ = a.conn.Export(nil, p, i)
	}
}

type launcher struct{ address string }

func (l launcher) GetAddress() (string, *dbus.Error) { return l.address, nil }

type registry struct{ app string }

func (r registry) GetChildren() ([]elementRef, *dbus.Error) {
	return []elementRef{{Bus: r.app, Path: registryRoot}}, nil
}
func (r registry) GetRole() (uint32, *dbus.Error)    { return 75, nil }
func (r registry) GetState() ([]uint32, *dbus.Error) { return []uint32{0, 0}, nil }

// startDaemon runs a private session bus and returns its address.
func startDaemon(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	dir, err := os.MkdirTemp("", "cs-dbus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "bus")
	cmd := exec.CommandContext(t.Context(), path, "--session", "--nofork", "--address=unix:path="+sock)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	for range 100 {
		if _, err := os.Stat(sock); err == nil {
			return "unix:path=" + sock
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the private bus did not start")
	return ""
}

func connectTo(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func str(s string) *string { return &s }

// startDesktop starts a private bus with a launcher, a registry and one application:
//
//	root (application "Fake")
//	  frame "Files" 0,0 800x600 [active]
//	    button "Open" [click, other]           at 10,20 60x24
//	    entry "Name" text "héllo" editable      at 100,20 120x24
//	    slider "Volume" value 0.25
//	    label "Plain"                          (no Component)
func startDesktop(t *testing.T) (addr string, app *fakeApp) {
	addr = startDaemon(t)

	l := connectTo(t, addr)
	if err := l.Export(launcher{addr}, "/org/a11y/bus", launcherName); err != nil {
		t.Fatal(err)
	}
	if r, err := l.RequestName(launcherName, 0); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("%v %v", r, err)
	}

	app = &fakeApp{conn: connectTo(t, addr), nodes: map[string]*atNode{}}
	const base = "/org/a11y/atspi/accessible/"
	active := uint32(1<<StateActive | 1<<StateEnabled | 1<<StateSensitive | 1<<StateShowing | 1<<StateVisible)
	shownWords := []uint32{1<<StateEnabled | 1<<StateSensitive | 1<<StateShowing | 1<<StateVisible, 0}
	app.export(registryRoot, &atNode{role: 75, name: "Fake", state: []uint32{0, 0}, children: []string{base + "frame"}})
	app.export(base+"frame", &atNode{role: roleFrame, name: "Files", state: []uint32{active, 0}, ext: &rect{0, 0, 800, 600},
		children: []string{base + "open", base + "name", base + "volume", base + "plain"}})
	app.export(base+"open", &atNode{role: rolePushButton, name: "Open", state: shownWords, ext: &rect{10, 20, 60, 24}, actions: []string{"click", "other"}, focusable: true})
	app.export(base+"name", &atNode{role: roleEntry, name: "Name", state: []uint32{shownWords[0] | 1<<StateEditable | 1<<StateFocusable, 0},
		ext: &rect{100, 20, 120, 24}, text: str("héllo"), editable: true})
	v := 0.25
	app.export(base+"volume", &atNode{role: roleSlider, name: "Volume", state: shownWords, ext: &rect{300, 20, 100, 20}, value: &v})
	app.export(base+"plain", &atNode{role: roleLabel, name: "Plain", desc: "no component", state: shownWords})

	r := connectTo(t, addr)
	if err := r.Export(registry{app: app.conn.Names()[0]}, registryRoot, ifaceAccessible); err != nil {
		t.Fatal(err)
	}
	if err := r.Export(propsIf{app: &fakeApp{nodes: map[string]*atNode{registryRoot: {name: "Registry"}}}, path: registryRoot}, registryRoot, ifaceProps); err != nil {
		t.Fatal(err)
	}
	if rep, err := r.RequestName(registryName, 0); err != nil || rep != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("%v %v", rep, err)
	}
	return addr, app
}

func dialFake(t *testing.T) (Bus, *fakeApp) {
	t.Helper()
	addr, app := startDesktop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bus, err := DialSession(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus, app
}

func TestDBusAppsAndInfo(t *testing.T) {
	bus, app := dialFake(t)
	ctx := context.Background()
	apps, err := bus.Apps(ctx)
	if err != nil || len(apps) != 1 {
		t.Fatalf("%+v %v", apps, err)
	}
	if apps[0].Name != "Fake" || apps[0].PID != os.Getpid() || apps[0].Bus != app.conn.Names()[0] || apps[0].Path != registryRoot {
		t.Errorf("app %+v (this process is %d)", apps[0], os.Getpid())
	}

	kids, err := bus.Children(ctx, apps[0].Ref)
	if err != nil || len(kids) != 1 {
		t.Fatalf("%v %v", kids, err)
	}
	info, err := bus.Info(ctx, kids[0])
	if err != nil {
		t.Fatal(err)
	}
	if RoleName(info.Role) != "frame" || info.Name != "Files" || info.ChildCount != 4 || !info.State.Has(StateActive) || !info.State.Showing() {
		t.Errorf("%+v", info)
	}
	grand, err := bus.Children(ctx, kids[0])
	if err != nil || len(grand) != 4 || grand[0].Path != "/org/a11y/atspi/accessible/open" {
		t.Errorf("%v %v", grand, err)
	}
	if none, err := bus.Children(ctx, grand[0]); err != nil || len(none) != 0 {
		t.Errorf("a leaf has no children: %v %v", none, err)
	}
	if got, _ := bus.Info(ctx, grand[3]); got.Description != "no component" {
		t.Errorf("description %q", got.Description)
	}
}

func TestDBusElementInterfaces(t *testing.T) {
	bus, app := dialFake(t)
	ctx := context.Background()
	apps, _ := bus.Apps(ctx)
	frame, _ := bus.Children(ctx, apps[0].Ref)
	kids, _ := bus.Children(ctx, frame[0])
	open, name, volume, plain := kids[0], kids[1], kids[2], kids[3]

	if x, y, w, h, err := bus.Extents(ctx, open); err != nil || x != 10 || y != 20 || w != 60 || h != 24 {
		t.Errorf("extents %d,%d %dx%d %v", x, y, w, h, err)
	}
	if !slices.Contains(app.logged(), "extents coord=0") {
		t.Errorf("extents are asked in screen coordinates: %v", app.logged())
	}
	if _, _, _, _, err := bus.Extents(ctx, plain); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an element without Component: %v", err)
	}

	if acts, err := bus.Actions(ctx, open); err != nil || !slices.Equal(acts, []string{"click", "other"}) {
		t.Errorf("actions %v %v", acts, err)
	}
	if _, err := bus.Actions(ctx, plain); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an element without Action: %v", err)
	}

	if s, err := bus.Text(ctx, name, 100); err != nil || s != "héllo" {
		t.Errorf("text %q %v", s, err)
	}
	if s, err := bus.Text(ctx, name, 2); err != nil || s != "hé" {
		t.Errorf("text is cut by characters, not bytes: %q %v", s, err)
	}
	if _, err := bus.Text(ctx, plain, 100); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an element without Text: %v", err)
	}
	if f, err := bus.Number(ctx, volume); err != nil || f != 0.25 {
		t.Errorf("number %v %v", f, err)
	}
	if _, err := bus.Number(ctx, open); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an element without Value: %v", err)
	}

	if done, err := bus.DoAction(ctx, open, 0); err != nil || !done {
		t.Errorf("do action %v %v", done, err)
	}
	if done, err := bus.DoAction(ctx, open, 5); err != nil || done {
		t.Errorf("an action that does not exist is not done: %v %v", done, err)
	}
	if took, err := bus.GrabFocus(ctx, open); err != nil || !took {
		t.Errorf("focus %v %v", took, err)
	}
	if took, err := bus.GrabFocus(ctx, name); err != nil || took {
		t.Errorf("a field that refuses the focus: %v %v", took, err)
	}
	if done, err := bus.SetText(ctx, name, "new ✓"); err != nil || !done {
		t.Errorf("set text %v %v", done, err)
	}
	if s, _ := bus.Text(ctx, name, 100); s != "new ✓" {
		t.Errorf("after set text: %q", s)
	}
	if _, err := bus.SetText(ctx, open, "x"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an element that is not editable: %v", err)
	}
	for _, want := range []string{"do /org/a11y/atspi/accessible/open click", "focus /org/a11y/atspi/accessible/open", "set /org/a11y/atspi/accessible/name new ✓"} {
		if !slices.Contains(app.logged(), want) {
			t.Errorf("the application never saw %q: %v", want, app.logged())
		}
	}
}

func TestDBusElementThatWentAway(t *testing.T) {
	bus, app := dialFake(t)
	ctx := context.Background()
	apps, _ := bus.Apps(ctx)
	frame, _ := bus.Children(ctx, apps[0].Ref)
	kids, _ := bus.Children(ctx, frame[0])
	app.unexport(kids[0].Path)
	// libdbus-based bridges answer for a destroyed object as for any unknown method, so either mapping is right.
	if _, err := bus.Info(ctx, kids[0]); !errors.Is(err, ErrGone) && !errors.Is(err, ErrUnsupported) {
		t.Errorf("an object that is no longer exported: %v", err)
	}
	if _, err := bus.Info(ctx, Ref{Bus: ":1.9999", Path: kids[0].Path}); !errors.Is(err, ErrGone) {
		t.Errorf("an application that is not there: %v", err)
	}
}

func TestDBusWalkAndServiceOnTheRealBus(t *testing.T) {
	bus, app := dialFake(t)
	svc := NewService(bus, &fakeDesktop{w: &Window{PID: os.Getpid(), Title: "Files"}}, &fakeDriver{})
	res, err := svc.Tree(context.Background(), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a1=frame:Files", "a2=button:Open", "a3=entry:Name", "a4=slider:Volume"}
	if got := ids(res); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v (the label has no Component, so no area, so it is not reported)", got, want)
	}
	if res.Entries[2].Value != "héllo" || res.Entries[3].Value != "0.25" || !slices.Equal(res.Entries[1].Actions, []string{"click", "other"}) {
		t.Errorf("%+v", res.Entries)
	}
	if b := res.Entries[1]; b.X != 10 || b.Y != 20 || b.W != 60 || b.H != 24 {
		t.Errorf("bounds %+v", b)
	}

	if out, err := svc.Act(context.Background(), "a2", res.Generation, OpClick, ""); err != nil || out.Method != "action:click" {
		t.Errorf("%+v %v", out, err)
	}
	if out, err := svc.Act(context.Background(), "a3", res.Generation, OpSetText, "abc"); err != nil || out.Method != "editable_text" {
		t.Errorf("%+v %v", out, err)
	}
	if !slices.Contains(app.logged(), "set /org/a11y/atspi/accessible/name abc") {
		t.Errorf("%v", app.logged())
	}
}

func TestDialWithoutABus(t *testing.T) {
	t.Setenv("AT_SPI_BUS_ADDRESS", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	if _, err := Dial(context.Background()); !errors.Is(err, ErrNoBus) {
		t.Errorf("no session: %v", err)
	}
	if _, err := DialAddress(context.Background(), "unix:path=/nonexistent/cs-bus"); !errors.Is(err, ErrNoBus) {
		t.Errorf("no socket: %v", err)
	}
	if _, err := DialSession(context.Background(), "unix:path=/nonexistent/cs-bus"); !errors.Is(err, ErrNoBus) {
		t.Errorf("no session socket: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DialAddress(ctx, "unix:path=/nonexistent/cs-bus"); err == nil {
		t.Error("a canceled dial succeeded")
	}
}

func TestDialFindsTheBusThroughTheEnvironment(t *testing.T) {
	addr, _ := startDesktop(t)
	t.Setenv("AT_SPI_BUS_ADDRESS", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	bus, err := Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if apps, err := bus.Apps(context.Background()); err != nil || len(apps) != 1 {
		t.Errorf("%v %v", apps, err)
	}
	t.Setenv("AT_SPI_BUS_ADDRESS", addr)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	bus2, err := Dial(context.Background())
	if err != nil {
		t.Fatalf("a known accessibility bus address is used as it is: %v", err)
	}
	_ = bus2.Close()
}

func TestMapErr(t *testing.T) {
	if mapErr(nil) != nil {
		t.Error("nil stays nil")
	}
	plain := errors.New("plain")
	if got := mapErr(plain); !errors.Is(got, plain) {
		t.Errorf("unknown errors are returned as they are: %v", got)
	}
	for name, want := range map[string]error{
		"org.freedesktop.DBus.Error.UnknownMethod":    ErrUnsupported,
		"org.freedesktop.DBus.Error.UnknownInterface": ErrUnsupported,
		"org.freedesktop.DBus.Error.InvalidArgs":      ErrUnsupported,
		"org.freedesktop.DBus.Error.UnknownObject":    ErrGone,
		"org.freedesktop.DBus.Error.ServiceUnknown":   ErrGone,
		"org.freedesktop.DBus.Error.NameHasNoOwner":   ErrGone,
	} {
		if got := mapErr(dbus.Error{Name: name}); !errors.Is(got, want) {
			t.Errorf("%s: %v", name, got)
		}
		if got := mapErr(&dbus.Error{Name: name}); !errors.Is(got, want) {
			t.Errorf("%s (pointer): %v", name, got)
		}
	}
	if got := mapErr(dbus.Error{Name: "org.freedesktop.DBus.Error.NoReply"}); errors.Is(got, ErrUnsupported) || errors.Is(got, ErrGone) {
		t.Errorf("a missing reply is neither: %v", got)
	}
}

// text reads a node's text under the lock: SetTextContents changes it from another goroutine.
func (a *fakeApp) text(path string) *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nodes[path].text
}

// logged returns what the application has been asked to do so far.
func (a *fakeApp) logged() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.log)
}
