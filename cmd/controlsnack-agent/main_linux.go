// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"runtime"
	"syscall"

	"github.com/PacifAIst/controlsnack-local/internal/agent"
	"github.com/PacifAIst/controlsnack-local/internal/agent/a11y"
	"github.com/PacifAIst/controlsnack-local/internal/agent/ocr"
	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/agent/x11"
	"github.com/PacifAIst/controlsnack-local/internal/buildinfo"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

func main() { os.Exit(run()) }

func run() int {
	fd := flag.Int("fd", 3, "file descriptor of the virtio-serial port to the host")
	nonce := flag.String("nonce", "", "the nonce of the push that started this agent; the host must repeat it")
	screenSize := flag.String("screen-size", os.Getenv("CS_SCREEN_SIZE"), "screen size WxH to report instead of asking the X server (for tests)")
	logLevel := flag.String("log-level", os.Getenv("CS_LOG_LEVEL"), "debug, info, warn or error (debug logs every request: type, timings, outcome; never content)")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *version {
		fmt.Println("controlsnack-agent", buildinfo.Get())
		return 0
	}

	level, err := server.ParseLogLevel(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "controlsnack-agent:", err)
		return 2
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	cfg, err := config(*nonce, *screenSize)
	if err != nil {
		log.Error("cannot start", "error", err)
		return 2
	}
	cfg.Log = log
	cfg.Handlers = map[string]server.Handler{}
	// Running commands and looking at files need no display.
	cfg.Handlers[protocol.TypeExec] = agent.NewExec().Handle
	cfg.Handlers[protocol.TypeFilePeek] = agent.FilePeek{}.Handle
	// The screen: the agent runs inside the desktop session, so DISPLAY and XAUTHORITY are inherited. Without X it still
	// runs and answers pings, but announces no screenshot capability, which the host reports in plain words.
	if x, err := x11.Open(os.Getenv("DISPLAY")); err != nil {
		log.Warn("no access to the screen: screenshots are not available", "error", err, "display", os.Getenv("DISPLAY"))
	} else {
		defer x.Close()
		shots := agent.NewScreenshots(x)
		cfg.Handlers[protocol.TypeScreenshot] = shots.Handle
		// The input handler reports the screen hash with the screenshots' hasher, so the two can be compared.
		input := agent.NewInput(x, x, shots.Hasher)
		cfg.Handlers[protocol.TypeInput] = input.Handle
		// The accessibility bus is dialed when first used and again if it breaks: the agent starts with the desktop
		// session, and the bus may come up a moment later. Without a session bus there is nothing to dial.
		if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" || os.Getenv("AT_SPI_BUS_ADDRESS") != "" {
			ax := agent.NewA11y(a11y.NewLazyBus(a11y.Dial), x, input)
			cfg.Handlers[protocol.TypeA11yTree] = ax.Tree
			cfg.Handlers[protocol.TypeA11yAction] = ax.Action
			shots.Marks = ax.Service
		} else {
			log.Warn("no session bus: the accessibility tree is not available")
		}
		// Text recognition is tesseract's, installed in the image; without it the capability is not announced.
		if path, err := exec.LookPath("tesseract"); err == nil {
			cfg.Handlers[protocol.TypeOCR] = agent.NewOCR(x, shots.Hasher, ocr.Tesseract{Path: path}).Handle
		} else {
			log.Warn("tesseract is not installed: text recognition is not available")
		}
		desktop := agent.DesktopHandlers{Desktop: x}
		cfg.Handlers[protocol.TypeScreenInfo] = desktop.ScreenInfo
		cfg.Handlers[protocol.TypeWindows] = desktop.Windows
		if *screenSize == "" {
			if w, h, err := x.Size(); err == nil {
				cfg.Display = protocol.Display{W: w, H: h}
			}
		}
	}
	if *fd < 3 {
		log.Error("cannot start", "error", "the port must be on a file descriptor of 3 or more")
		return 2
	}
	port := os.NewFile(uintptr(*fd), "virtio-port") //nolint:gosec // a small descriptor number from our own command line
	if port == nil {
		log.Error("cannot start", "error", fmt.Sprintf("file descriptor %d is not open", *fd))
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	log.Info("agent starting", "version", buildinfo.Get().Version, "sha256", cfg.SelfSHA256[:12], "arch", cfg.Arch, "user", cfg.User)
	if err := server.Serve(ctx, port, port, cfg); err != nil {
		log.Error("the session ended", "error", err)
		return 1
	}
	log.Info("the host disconnected")
	return 0
}

// config gathers what the handshake reports about this agent.
func config(nonce, screenSize string) (server.Config, error) {
	exe, err := os.Executable()
	if err != nil {
		return server.Config{}, err
	}
	sum, err := server.FileSHA256(exe)
	if err != nil {
		return server.Config{}, fmt.Errorf("hashing %s: %w", exe, err)
	}
	arch, err := server.GuestArch(runtime.GOARCH)
	if err != nil {
		return server.Config{}, err
	}
	u, err := user.Current()
	if err != nil {
		return server.Config{}, err
	}
	d, err := server.ParseDisplay(screenSize)
	if err != nil {
		return server.Config{}, err
	}
	return server.Config{Version: buildinfo.Get().Version, SelfSHA256: sum, Arch: arch, User: u.Username, Nonce: nonce, Display: d}, nil
}
