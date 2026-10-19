// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/PacifAIst/controlsnack/internal/platform"
	"github.com/PacifAIst/controlsnack/internal/vm/overlay"
)

// fakeOverlay makes a disk that looks like a qcow2 overlay on a base image, as far as the library checks.
func fakeOverlay(_ context.Context, _, backing, dest, _ string) error {
	if !filepath.IsAbs(backing) {
		return errors.New("backing path must be absolute")
	}
	data := make([]byte, 512)
	copy(data, "QFI\xfb")
	binary.BigEndian.PutUint64(data[8:], 256)
	binary.BigEndian.PutUint32(data[16:], uint32(len(backing)))
	copy(data[256:], backing)
	return os.WriteFile(dest, data, 0o600)
}

var libraryClock atomic.Int64

// testLibrary is the library of kept VMs of a test: its disks are fakes and its clock moves a minute with every look, so that
// "used later than made" can be told on a machine whose clock is coarse.
func testLibrary(dirs platform.Dirs) overlay.Library {
	return overlay.Library{Dir: dirs.VMsDir(), Overlay: fakeOverlay, Now: func() time.Time {
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(libraryClock.Add(1)) * time.Minute)
	}}
}
