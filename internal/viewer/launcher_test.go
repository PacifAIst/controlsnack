// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/viewer"
	"github.com/PacifAIst/controlsnack-local/web"
)

// machine is a fake computer: the files it has, its environment and the programs on its PATH.
type machine struct {
	files map[string]bool
	env   map[string]string
	path  map[string]string
}

func (m machine) finder(goos string) *viewer.Finder {
	return &viewer.Finder{
		GOOS:   goos,
		Getenv: func(k string) string { return m.env[k] },
		Stat: func(p string) (fs.FileInfo, error) {
			if m.files[p] {
				return fakeFile{}, nil
			}
			return nil, fs.ErrNotExist
		},
		LookPath: func(name string) (string, error) {
			if p, ok := m.path[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
	}
}

type fakeFile struct{ fs.FileInfo }

func (fakeFile) IsDir() bool { return false }

func TestTheBrowserSearchOnEachSystem(t *testing.T) {
	pf, pf86, local := `C:\Program Files`, `C:\Program Files (x86)`, `C:\Users\u\AppData\Local`
	winEnv := map[string]string{"ProgramFiles": pf, "ProgramFiles(x86)": pf86, "LOCALAPPDATA": local}
	j := filepath.Join
	edge86, edge := j(pf86, "Microsoft", "Edge", "Application", "msedge.exe"), j(pf, "Microsoft", "Edge", "Application", "msedge.exe")
	chrome, chromeLocal := j(pf, "Google", "Chrome", "Application", "chrome.exe"), j(local, "Google", "Chrome", "Application", "chrome.exe")
	chromium, brave := j(local, "Chromium", "Application", "chrome.exe"), j(pf, "BraveSoftware", "Brave-Browser", "Application", "brave.exe")
	for name, tc := range map[string]struct {
		goos string
		m    machine
		want string
	}{
		"windows: Edge first":            {"windows", machine{files: set(edge86, chrome, brave), env: winEnv}, edge86},
		"windows: Edge in Program Files": {"windows", machine{files: set(edge, chrome), env: winEnv}, edge},
		"windows: Chrome without Edge":   {"windows", machine{files: set(chrome, brave), env: winEnv}, chrome},
		"windows: Chrome for one user":   {"windows", machine{files: set(chromeLocal), env: winEnv}, chromeLocal},
		"windows: Chromium":              {"windows", machine{files: set(chromium, brave), env: winEnv}, chromium},
		"windows: Brave last":            {"windows", machine{files: set(brave), env: winEnv}, brave},
		"windows: none":                  {"windows", machine{files: set(), env: winEnv}, ""},
		"windows: no environment":        {"windows", machine{files: set(edge), env: nil}, ""},
		"mac: Edge first":                {"darwin", machine{files: set("/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")}, "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"},
		"mac: Chrome":                    {"darwin", machine{files: set("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")}, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
		"mac: in the home folder":        {"darwin", machine{files: set(j("/Users/u", "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser")), env: map[string]string{"HOME": "/Users/u"}}, j("/Users/u", "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser")},
		"mac: none":                      {"darwin", machine{files: set()}, ""},
		"linux: Edge first":              {"linux", machine{path: map[string]string{"microsoft-edge": "/usr/bin/microsoft-edge", "google-chrome": "/usr/bin/google-chrome"}}, "/usr/bin/microsoft-edge"},
		"linux: Chrome":                  {"linux", machine{path: map[string]string{"google-chrome-stable": "/usr/bin/google-chrome-stable"}}, "/usr/bin/google-chrome-stable"},
		"linux: Chromium":                {"linux", machine{path: map[string]string{"chromium-browser": "/snap/bin/chromium-browser", "brave-browser": "/usr/bin/brave-browser"}}, "/snap/bin/chromium-browser"},
		"linux: Brave":                   {"linux", machine{path: map[string]string{"brave-browser": "/usr/bin/brave-browser"}}, "/usr/bin/brave-browser"},
		"linux: none":                    {"linux", machine{path: map[string]string{"firefox": "/usr/bin/firefox"}}, ""},
	} {
		if got := tc.m.finder(tc.goos).Browser(); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func set(paths ...string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

// recorder stands for the machine's way of starting programs and opening addresses.
type recorder struct {
	started  [][]string
	opened   []string
	startErr error
	openErr  error
	proc     *fakeProc
}

type fakeProc struct {
	killed bool
	done   chan struct{}
	err    error
}

func newFakeProc() *fakeProc { return &fakeProc{done: make(chan struct{})} }
func (p *fakeProc) Kill() error {
	if !p.killed {
		p.killed = true
		close(p.done)
	}
	return p.err
}
func (p *fakeProc) Done() <-chan struct{} { return p.done }

func (r *recorder) options(o viewer.LaunchOptions) viewer.LaunchOptions {
	o.Start = func(path string, args []string) (viewer.Process, error) {
		if r.startErr != nil {
			return nil, r.startErr
		}
		r.started = append(r.started, append([]string{path}, args...))
		if r.proc == nil {
			r.proc = newFakeProc()
		}
		return r.proc, nil
	}
	o.OpenDefault = func(address string) error {
		r.opened = append(r.opened, address)
		return r.openErr
	}
	return o
}

const viewerURL = "http://127.0.0.1:43211/auth?t=0123456789abcdef"

func TestTheAppWindowIsOpenedAsThePlanSays(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "viewer-profile")
	r := &recorder{}
	m := machine{files: set("/b/msedge.exe")}
	w, err := viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: viewerURL, ProfileDir: profile, Browser: "/b/msedge.exe", Finder: m.finder("linux")}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/b/msedge.exe", "--app=" + viewerURL, "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check", "--disable-extensions", "--disable-sync", "--window-size=1440,900"}
	if len(r.started) != 1 || strings.Join(r.started[0], "\n") != strings.Join(want, "\n") {
		t.Fatalf("started %q\nwant    %q", r.started, want)
	}
	if w.Kind != "app" || w.Name != "Microsoft Edge" || w.Fallback || len(r.opened) != 0 || !strings.Contains(w.Notice, "Microsoft Edge") {
		t.Errorf("window %+v, opened %q", w, r.opened)
	}
	if st, err := os.Stat(profile); err != nil || !st.IsDir() {
		t.Errorf("the profile directory: %v", err)
	}
	if w.Closed() == nil {
		t.Error("an app window cannot be watched")
	}
	// Its own size and extra arguments.
	r2 := &recorder{}
	if _, err := viewer.Launch(r2.options(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: viewerURL, ProfileDir: profile, Browser: "/b/chrome", Width: 1100, Height: 700, ExtraArgs: []string{"--headless=new"}})); err != nil {
		t.Fatal(err)
	}
	if got := r2.started[0]; got[len(got)-2] != "--window-size=1100,700" || got[len(got)-1] != "--headless=new" {
		t.Errorf("arguments %q", got)
	}
	// Ending it ends the browser and takes the profile away, once.
	if err := w.Kill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.proc.killed {
		t.Error("the browser was not ended")
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Errorf("the profile is still there: %v", err)
	}
	select {
	case <-w.Closed():
	default:
		t.Error("Closed is not closed after Kill")
	}
	if err := w.Kill(context.Background()); err != nil {
		t.Errorf("a second Kill: %v", err)
	}
}

func TestTheNamesOfBrowsers(t *testing.T) {
	for path, want := range map[string]string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`: "Microsoft Edge", "/usr/bin/google-chrome-stable": "Google Chrome",
		"/snap/bin/chromium-browser": "Chromium", "/usr/bin/brave-browser": "Brave", "/opt/odd/browser": "browser",
	} {
		r := &recorder{}
		w, err := viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: viewerURL, ProfileDir: filepath.Join(t.TempDir(), "p"), Browser: path}))
		if err != nil {
			t.Fatal(err)
		}
		if w.Name != want {
			t.Errorf("%s is called %q, want %q", path, w.Name, want)
		}
	}
}

func TestWhenThereIsNoWindowTheDefaultBrowserIsTried(t *testing.T) {
	// No Chromium-based browser on the machine.
	r := &recorder{}
	w, err := viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: viewerURL, ProfileDir: filepath.Join(t.TempDir(), "p"), Finder: machine{path: map[string]string{}}.finder("linux")}))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.started) != 0 || len(r.opened) != 1 || r.opened[0] != viewerURL || w.Kind != "browser" || !w.Fallback || !strings.Contains(w.Notice, "No Chromium-based browser") || !strings.Contains(w.Notice, viewerURL) {
		t.Errorf("window %+v, started %q, opened %q", w, r.started, r.opened)
	}
	if w.Closed() != nil || w.Kill(context.Background()) != nil {
		t.Error("a default browser tab is not ours to watch or end")
	}

	// The browser is there but cannot be started: the profile directory is taken away and the default browser is tried.
	profile := filepath.Join(t.TempDir(), "p")
	r = &recorder{startErr: errors.New("exec format error")}
	w, err = viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: viewerURL, ProfileDir: profile, Browser: "/b/chrome"}))
	if err != nil || w.Kind != "browser" || !w.Fallback || !strings.Contains(w.Notice, "could not be started: exec format error") || len(r.opened) != 1 {
		t.Errorf("window %+v, error %v", w, err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Error("the profile of a window that did not start was left behind")
	}

	// Nothing works: the person is told the address.
	r = &recorder{startErr: errors.New("no"), openErr: errors.New("no opener")}
	w, err = viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: viewerURL, ProfileDir: filepath.Join(t.TempDir(), "p"), Browser: "/b/chrome"}))
	if err != nil || w.Kind != "" || !w.Fallback || !strings.Contains(w.Notice, "Open this address yourself") || !strings.Contains(w.Notice, viewerURL) || !strings.Contains(w.Notice, "no opener") {
		t.Errorf("window %+v, error %v", w, err)
	}
}

func TestTheOtherModes(t *testing.T) {
	// browser: the default browser, and never a window of its own.
	r := &recorder{}
	w, err := viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeBrowser, URL: viewerURL, Browser: "/b/chrome"}))
	if err != nil || len(r.started) != 0 || len(r.opened) != 1 || w.Kind != "browser" || w.Fallback {
		t.Errorf("browser: %+v, %v, started %q, opened %q", w, err, r.started, r.opened)
	}
	// none: nothing.
	r = &recorder{}
	w, err = viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeNone}))
	if err != nil || len(r.started)+len(r.opened) != 0 || w.Kind != "" || w.Fallback || w.Notice != "" {
		t.Errorf("none: %+v, %v", w, err)
	}
	// none needs no address at all.
	// webview: not built; the window is made instead and the person is told.
	r = &recorder{}
	w, err = viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeWebview, URL: viewerURL, ProfileDir: filepath.Join(t.TempDir(), "p"), Browser: "/b/msedge.exe"}))
	if err != nil || w.Kind != "app" || !w.Fallback || !strings.HasPrefix(w.Notice, "The web view is not available in this build; the viewer is open") {
		t.Errorf("webview: %+v, %v", w, err)
	}
}

func TestLaunchRefusesWhatIsWrong(t *testing.T) {
	for name, o := range map[string]viewer.LaunchOptions{
		"no mode":                     {URL: viewerURL},
		"an unknown mode":             {Mode: "popup", URL: viewerURL},
		"an address that is not ours": {Mode: viewer.ModeBrowser, URL: "http://example.com/"},
		"https":                       {Mode: viewer.ModeBrowser, URL: "https://127.0.0.1:43211/"},
		"localhost":                   {Mode: viewer.ModeBrowser, URL: "http://localhost:43211/"},
		"every interface":             {Mode: viewer.ModeBrowser, URL: "http://0.0.0.0:43211/"},
		"no port":                     {Mode: viewer.ModeBrowser, URL: "http://127.0.0.1/"},
		"port zero":                   {Mode: viewer.ModeBrowser, URL: "http://127.0.0.1:0/"},
		"a user in the address":       {Mode: viewer.ModeBrowser, URL: "http://u:p@127.0.0.1:43211/"},
		"another host after an at":    {Mode: viewer.ModeBrowser, URL: "http://127.0.0.1:43211@evil.example/"},
		"a space":                     {Mode: viewer.ModeBrowser, URL: "http://127.0.0.1:43211/a b"},
		"a quote":                     {Mode: viewer.ModeBrowser, URL: `http://127.0.0.1:43211/"x`},
		"a line break":                {Mode: viewer.ModeBrowser, URL: "http://127.0.0.1:43211/\n--evil"},
		"no address":                  {Mode: viewer.ModeWindow, ProfileDir: "p"},
		"a window without a profile":  {Mode: viewer.ModeWindow, URL: viewerURL, Browser: "/b/chrome"},
	} {
		r := &recorder{}
		if w, err := viewer.Launch(r.options(o)); err == nil {
			t.Errorf("%s: accepted (%+v)", name, w)
		}
		if len(r.started)+len(r.opened) != 0 {
			t.Errorf("%s: something was started or opened", name)
		}
	}
}

func TestAProfileThatCannotBeMadeFallsBackToTheDefaultBrowser(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	w, err := viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: viewerURL, ProfileDir: filepath.Join(blocker, "profile"), Browser: "/b/chrome"}))
	if err != nil || w.Kind != "browser" || !w.Fallback || !strings.Contains(w.Notice, "profile directory could not be made") || len(r.started) != 0 {
		t.Errorf("window %+v, error %v", w, err)
	}
}

func TestEndingAWindowSaysWhatWentWrong(t *testing.T) {
	r := &recorder{proc: newFakeProc()}
	r.proc.err = errors.New("access denied")
	profile := filepath.Join(t.TempDir(), "p")
	w, err := viewer.Launch(r.options(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: viewerURL, ProfileDir: profile, Browser: "/b/chrome"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Kill(context.Background()); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("Kill: %v", err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Error("the profile was left behind when ending the browser failed")
	}
}

// The viewer in a real browser, opened through the launcher, with no screen: the page loads, the socket connects, and the
// window's end is the socket's end.
func TestARealBrowserOpenedByTheLauncher(t *testing.T) {
	if os.Getenv("CS_NO_BROWSER_TESTS") != "" {
		t.Skip("CS_NO_BROWSER_TESTS is set")
	}
	exe := viewer.DefaultFinder().Browser()
	if exe == "" {
		t.Skip("no Chromium-based browser on this machine")
	}
	h := startHubWith(t, func(o *viewer.Options) { o.Static = web.Static() })
	link, err := h.s.AuthURL()
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(t.TempDir(), "viewer-profile")
	w, err := viewer.Launch(viewer.LaunchOptions{Mode: viewer.ModeWindow, URL: link, ProfileDir: profile, ExtraArgs: []string{"--headless=new", "--disable-gpu"}})
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "app" {
		t.Fatalf("window %+v", w)
	}
	t.Cleanup(func() { _ = w.Kill(context.Background()) })
	select {
	case <-h.connect:
	case <-time.After(30 * time.Second):
		t.Fatal("the window never connected")
	}
	if !h.hub.Connected() {
		t.Error("not connected")
	}
	if _, err := os.Stat(filepath.Join(profile, "Default")); err != nil && !os.IsNotExist(err) {
		t.Errorf("profile: %v", err)
	}
	if err := w.Kill(context.Background()); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-h.gone:
	case <-time.After(15 * time.Second):
		t.Fatal("the socket did not close when the window was ended")
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Errorf("the profile is still there: %v", err)
	}
	select {
	case <-w.Closed():
	case <-time.After(5 * time.Second):
		t.Error("Closed was not closed")
	}
}
