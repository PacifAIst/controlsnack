// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	pi := OSProcesses{}
	release, err := AcquireLock(path, pi)
	if err != nil {
		t.Fatal(err)
	}
	// A second taker is refused while this process lives, and told who holds it.
	_, err = AcquireLock(path, pi)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.PID != os.Getpid() || locked.Path != path {
		t.Fatalf("%v", err)
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("released")
	}
	release() // twice is harmless
	again, err := AcquireLock(path, pi)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

func TestAcquireLockTakesOverStaleLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	pi := OSProcesses{}
	start, err := pi.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{
		fmt.Sprintf(`{"pid":%d,"start":"another-start-time"}`, os.Getpid()), // the pid was reused by another process
		`{"pid":999999,"start":"x"}`,                                        // no such process
		`not json`, ``, `{"pid":0}`,
	} {
		if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
			t.Fatal(err)
		}
		release, err := AcquireLock(path, pi)
		if err != nil {
			t.Errorf("stale lock %q: %v", stale, err)
			continue
		}
		release()
	}
	// A lock of a live process (this one, with its true start time) is respected.
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"pid":%d,"start":%q}`, os.Getpid(), start)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path, pi); err == nil {
		t.Error("a live holder keeps the lock")
	}
	if _, err := AcquireLock(filepath.Join(t.TempDir(), "missing-dir", "x.lock"), pi); err == nil {
		t.Error("a directory that does not exist")
	}
}
