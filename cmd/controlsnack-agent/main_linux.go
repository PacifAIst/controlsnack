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
	"os/signal"
	"os/user"
	"runtime"
	"syscall"

	"github.com/PacifAIst/controlsnack-local/internal/agent/server"
	"github.com/PacifAIst/controlsnack-local/internal/buildinfo"
)

func main() { os.Exit(run()) }

func run() int {
	fd := flag.Int("fd", 3, "file descriptor of the virtio-serial port to the host")
	nonce := flag.String("nonce", "", "the nonce of the push that started this agent; the host must repeat it")
	display := flag.String("display", os.Getenv("CS_DISPLAY"), "screen size WxH to report (default: the size the VM starts with)")
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
	cfg, err := config(*nonce, *display)
	if err != nil {
		log.Error("cannot start", "error", err)
		return 2
	}
	cfg.Log = log
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
func config(nonce, display string) (server.Config, error) {
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
	d, err := server.ParseDisplay(display)
	if err != nil {
		return server.Config{}, err
	}
	return server.Config{Version: buildinfo.Get().Version, SelfSHA256: sum, Arch: arch, User: u.Username, Nonce: nonce, Display: d}, nil
}
