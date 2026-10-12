// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Dirs are the per-user directories controlsnack uses (PLAN.md section 6.12).
type Dirs struct {
	// Config holds config.yaml: <user config dir>/controlsnack.
	Config string
	// Cache holds downloaded and built base images and the model probe cache: <user cache dir>/controlsnack.
	Cache string
	// State holds what must survive a session: audit logs, the active-session lock, kept VMs.
	// Linux: $XDG_STATE_HOME/controlsnack (default ~/.local/state/controlsnack);
	// macOS: ~/Library/Application Support/ControlSnack; Windows: %LOCALAPPDATA%\ControlSnack.
	State string
}

// Environment is what directory resolution reads from the machine; it is a struct of functions so every OS can be
// tested on any OS.
type Environment struct {
	GOOS          string
	Getenv        func(string) string
	UserHomeDir   func() (string, error)
	UserConfigDir func() (string, error)
	UserCacheDir  func() (string, error)
}

// HostEnvironment is the real machine.
func HostEnvironment() Environment {
	return Environment{
		GOOS: runtime.GOOS, Getenv: os.Getenv, UserHomeDir: os.UserHomeDir,
		UserConfigDir: os.UserConfigDir, UserCacheDir: os.UserCacheDir,
	}
}

// ResolveDirs works out the directories for an environment. It does not create them.
func ResolveDirs(env Environment) (Dirs, error) {
	cfg, err := env.UserConfigDir()
	if err != nil {
		return Dirs{}, fmt.Errorf("cannot locate the user config directory: %w", err)
	}
	cache, err := env.UserCacheDir()
	if err != nil {
		return Dirs{}, fmt.Errorf("cannot locate the user cache directory: %w", err)
	}
	state, err := stateDir(env)
	if err != nil {
		return Dirs{}, err
	}
	return Dirs{Config: filepath.Join(cfg, "controlsnack"), Cache: filepath.Join(cache, "controlsnack"), State: state}, nil
}

// DefaultDirs resolves the directories of the machine the program runs on.
func DefaultDirs() (Dirs, error) { return ResolveDirs(HostEnvironment()) }

func stateDir(env Environment) (string, error) {
	home := func() (string, error) {
		h, err := env.UserHomeDir()
		if err != nil || h == "" {
			return "", errors.New("cannot locate the home directory")
		}
		return h, nil
	}
	switch env.GOOS {
	case "windows":
		if v := env.Getenv("LocalAppData"); v != "" {
			return filepath.Join(v, "ControlSnack"), nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "AppData", "Local", "ControlSnack"), nil
	case "darwin":
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "Library", "Application Support", "ControlSnack"), nil
	default:
		// The XDG specification ignores a relative $XDG_STATE_HOME.
		if v := env.Getenv("XDG_STATE_HOME"); v != "" && strings.HasPrefix(v, "/") {
			return filepath.Join(v, "controlsnack"), nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".local", "state", "controlsnack"), nil
	}
}

// AuditDir is where audit logs live.
func (d Dirs) AuditDir() string { return filepath.Join(d.State, "audit") }

// SessionsDir holds the active-session lock.
func (d Dirs) SessionsDir() string { return filepath.Join(d.State, "sessions") }

// VMsDir holds kept VMs.
func (d Dirs) VMsDir() string { return filepath.Join(d.State, "vms") }

// ImagesDir holds the base images.
func (d Dirs) ImagesDir() string { return filepath.Join(d.Cache, "images") }

// EnsureDir creates dir (and its parents) with mode 0700 if it does not exist and returns an error if it exists but
// is not a directory. Secrets such as the kill token live under these directories, so they are never group- or
// world-accessible.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	return nil
}
