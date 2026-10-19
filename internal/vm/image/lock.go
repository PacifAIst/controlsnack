// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PacifAIst/controlsnack/internal/platform"
)

// acquireBuildLock allows one build at a time in a store. A lock left by a process that no longer exists is taken
// over (platform.AcquireLock checks the process id and its start time).
func acquireBuildLock(dir string, pi platform.ProcessInspector) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	release, err = platform.AcquireLock(filepath.Join(dir, ".build.lock"), pi)
	var locked *platform.LockedError
	if errors.As(err, &locked) {
		return nil, fmt.Errorf("another image build is running (process %d); wait for it, or stop it", locked.PID)
	}
	return release, err
}
