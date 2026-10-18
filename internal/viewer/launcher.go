// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The viewer modes of --viewer (viewer.mode).
const (
	ModeWindow  = "window"  // the viewer in a window of its own (a Chromium-based browser in app mode), else the default browser
	ModeBrowser = "browser" // the default browser
	ModeNone    = "none"    // no viewer
	ModeWebview = "webview" // a native web view: not built (PLAN.md Phase 9, only if asked for)
)

// DefaultWindowSize is the size the app window opens at (PLAN.md section 10.2).
const (
	DefaultWidth  = 1440
	DefaultHeight = 900
)

// browserNames are the browsers the window can be made with, in order of preference (PLAN.md section 10.4).
var (
	windowsBrowsers = []struct{ vendor, app, exe string }{
		{"Microsoft", "Edge", "msedge.exe"},
		{"Google", "Chrome", "chrome.exe"},
		{"Chromium", "", "chrome.exe"},
		{"BraveSoftware", "Brave-Browser", "brave.exe"},
	}
	macBrowsers = []string{
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
	}
	unixBrowsers = []string{
		"microsoft-edge", "microsoft-edge-stable", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser",
	}
)

// Finder finds browsers. The fields are the parts of the machine it looks at, so that a test can show it another one.
type Finder struct {
	GOOS     string
	Getenv   func(string) string
	Stat     func(string) (fs.FileInfo, error)
	LookPath func(string) (string, error)
}

// DefaultFinder looks at this machine.
func DefaultFinder() Finder {
	return Finder{GOOS: runtime.GOOS, Getenv: os.Getenv, Stat: os.Stat, LookPath: exec.LookPath}
}

func (f Finder) isFile(p string) bool {
	st, err := f.Stat(p)
	return err == nil && !st.IsDir()
}

// Browser returns the path of a Chromium-based browser installed on the machine, or "".
func (f Finder) Browser() string {
	switch f.GOOS {
	case "windows":
		var roots []string
		for _, v := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			if r := f.Getenv(v); r != "" {
				roots = append(roots, r)
			}
		}
		for _, b := range windowsBrowsers {
			for _, root := range roots {
				p := filepath.Join(root, b.vendor, b.app, "Application", b.exe)
				if b.app == "" {
					p = filepath.Join(root, b.vendor, "Application", b.exe)
				}
				if f.isFile(p) {
					return p
				}
			}
		}
	case "darwin":
		for _, p := range macBrowsers {
			if f.isFile(p) {
				return p
			}
		}
		if home := f.Getenv("HOME"); home != "" {
			for _, p := range macBrowsers {
				if q := filepath.Join(home, p); f.isFile(q) {
					return q
				}
			}
		}
	default:
		for _, name := range unixBrowsers {
			if p, err := f.LookPath(name); err == nil && p != "" {
				return p
			}
		}
	}
	return ""
}

// checkViewerURL accepts only the viewer's own address: it goes on a command line, and nothing but a loopback address with a
// port is ever opened.
func checkViewerURL(raw string) error {
	if strings.ContainsAny(raw, " \t\r\n\"'`") {
		return errors.New("the viewer address has characters that do not belong in it")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil {
		return fmt.Errorf("the viewer address %q is not a plain http address", raw)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host != "127.0.0.1" || port == "" || port == "0" {
		return fmt.Errorf("the viewer address %q is not on 127.0.0.1 with a port", raw)
	}
	return nil
}

// Process is a started program that can be ended.
type Process interface {
	Kill() error
	// Done is closed when the program has ended.
	Done() <-chan struct{}
}

// openDefault opens an address in the default browser.
func openDefault(goos, address string) error {
	var cmd *exec.Cmd
	switch goos {
	case "windows":
		cmd = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", address) //nolint:gosec // the viewer's own address, checked
	case "darwin":
		cmd = exec.CommandContext(context.Background(), "open", address) //nolint:gosec // the viewer's own address, checked
	default:
		cmd = exec.CommandContext(context.Background(), "xdg-open", address) //nolint:gosec // the viewer's own address, checked
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// LaunchOptions say how to open the viewer.
type LaunchOptions struct {
	// Mode is one of the Mode constants.
	Mode string
	// URL is the login link of the viewer (Server.AuthURL). It is used once, and only by the window it is given to.
	URL string
	// ProfileDir is a directory for the window's own browser profile, made for the session and removed with the window.
	ProfileDir string
	// Browser is the path of the browser to use instead of looking for one.
	Browser string
	// Width and Height are the window's size (default 1440 by 900).
	Width, Height int
	// ExtraArgs are added to the browser's command line (the tests run it without a screen).
	ExtraArgs []string

	// The parts of the machine; the zero value is this machine.
	Finder      *Finder
	Start       func(path string, args []string) (Process, error)
	OpenDefault func(address string) error
}

// Window is what was opened.
type Window struct {
	// Kind says what happened: "app" (a window of its own), "browser" (the default browser), "" (nothing was opened).
	Kind string
	// Name is the browser that was used, when it is known.
	Name string
	// Notice is a line for the person: what was opened, or what to do themselves.
	Notice string
	// Fallback says that the window of its own could not be made and the default browser was used or the person was asked.
	Fallback bool

	proc    Process
	done    <-chan struct{}
	profile string
}

// browserName is the browser's name for a message, from its path.
func browserName(path string) string {
	b := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(b, "edge"):
		return "Microsoft Edge"
	case strings.Contains(b, "brave"):
		return "Brave"
	case strings.Contains(b, "chromium"):
		return "Chromium"
	case strings.Contains(b, "chrome"):
		return "Google Chrome"
	}
	return filepath.Base(path)
}

// Args is the command line of the app window (PLAN.md section 10.4).
func appArgs(o LaunchOptions) []string {
	w, h := o.Width, o.Height
	if w <= 0 || h <= 0 {
		w, h = DefaultWidth, DefaultHeight
	}
	args := []string{
		"--app=" + o.URL,
		"--user-data-dir=" + o.ProfileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-sync",
		fmt.Sprintf("--window-size=%d,%d", w, h),
	}
	return append(args, o.ExtraArgs...)
}

// Launch opens the viewer the way the mode says. It never fails the session over the window: when the window of its own cannot
// be made, the default browser is tried, and then the person is told the address; an error is for a mode or an address that is
// wrong, which is a bug of the caller.
func Launch(o LaunchOptions) (*Window, error) {
	switch o.Mode {
	case ModeNone:
		return &Window{}, nil
	case ModeWindow, ModeBrowser:
	case ModeWebview:
		w, err := Launch(withMode(o, ModeWindow))
		if err != nil {
			return nil, err
		}
		w.Fallback = true
		w.Notice = "The web view is not available in this build; " + lowerFirst(w.Notice)
		return w, nil
	default:
		return nil, fmt.Errorf("viewer mode %q is not one of window, browser, none or webview", o.Mode)
	}
	if err := checkViewerURL(o.URL); err != nil {
		return nil, err
	}
	finder := DefaultFinder()
	if o.Finder != nil {
		finder = *o.Finder
	}
	if o.Mode == ModeWindow {
		path := o.Browser
		if path == "" {
			path = finder.Browser()
		}
		if path == "" {
			return openInDefault(o, finder.GOOS, "no Chromium-based browser (Edge, Chrome, Chromium or Brave) was found")
		}
		if o.ProfileDir == "" {
			return nil, errors.New("a window of its own needs a profile directory")
		}
		if err := os.MkdirAll(o.ProfileDir, 0o700); err != nil {
			return openInDefault(o, finder.GOOS, "the window's profile directory could not be made: "+err.Error())
		}
		start := o.Start
		if start == nil {
			start = startProcess
		}
		proc, err := start(path, appArgs(o))
		if err != nil {
			_ = os.RemoveAll(o.ProfileDir)
			return openInDefault(o, finder.GOOS, browserName(path)+" could not be started: "+err.Error())
		}
		name := browserName(path)
		return &Window{Kind: "app", Name: name, Notice: "The viewer is open in a window of " + name + ".", proc: proc, done: proc.Done(), profile: o.ProfileDir}, nil
	}
	return openInDefault(o, finder.GOOS, "")
}

func withMode(o LaunchOptions, mode string) LaunchOptions {
	o.Mode = mode
	return o
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// openInDefault opens the address in the default browser; when that fails too, the person is told the address.
func openInDefault(o LaunchOptions, goos, why string) (*Window, error) {
	open := o.OpenDefault
	if open == nil {
		open = func(address string) error { return openDefault(goos, address) }
	}
	fallback := why != ""
	if err := open(o.URL); err != nil {
		if why != "" {
			why += "; "
		}
		return &Window{Fallback: true, Notice: fmt.Sprintf("%sthe default browser could not be opened (%v). Open this address yourself, once, in a browser on this machine: %s", capitalize(why), err, o.URL)}, nil
	}
	notice := "The viewer was opened in your default browser."
	if fallback {
		notice = capitalize(why) + ", so the viewer was opened in your default browser. If it did not appear, open this address yourself, once: " + o.URL
	}
	return &Window{Kind: "browser", Notice: notice, Fallback: fallback}, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Closed is closed when the window's browser has ended (the person closed the window, or Kill ran). It is nil for a window
// that is not one of ours to watch.
func (w *Window) Closed() <-chan struct{} {
	return w.done
}

// Kill ends the window's browser and removes its profile. It is the teardown step of the kill switch and of a normal end,
// safe to call twice.
func (w *Window) Kill(ctx context.Context) error {
	if w.proc == nil {
		return nil
	}
	proc, profile := w.proc, w.profile
	w.proc, w.profile = nil, ""
	var errs []error
	if err := proc.Kill(); err != nil {
		errs = append(errs, fmt.Errorf("ending the viewer window: %w", err))
	}
	select {
	case <-proc.Done():
	case <-time.After(3 * time.Second):
		errs = append(errs, errors.New("the viewer window did not end in 3 s"))
	case <-ctx.Done():
	}
	// The browser may hold its profile open for a moment after it has gone.
	var rerr error
	for i := 0; i < 10; i++ {
		if rerr = os.RemoveAll(profile); rerr == nil {
			break
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			i = 10
		}
	}
	if rerr != nil {
		errs = append(errs, fmt.Errorf("removing the window's profile: %w", rerr))
	}
	return errors.Join(errs...)
}
