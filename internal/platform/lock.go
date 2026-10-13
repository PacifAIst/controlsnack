// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// LockedError means a lock is held by a process that is still running.
type LockedError struct {
	Path string
	PID  int
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("%s is in use by process %d", e.Path, e.PID)
}

type lockBody struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// AcquireLock takes an exclusive lock by creating path. The file records the process id and the process's start
// time: a lock left behind by a process that no longer exists (or whose id has since been reused by another
// program, which the start time reveals) is taken over, while a lock held by a live process makes this return a
// *LockedError. The returned function releases the lock.
func AcquireLock(path string, pi ProcessInspector) (release func(), err error) {
	start, _ := pi.StartTime(os.Getpid())
	body, _ := json.Marshal(lockBody{PID: os.Getpid(), Start: start})
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the caller's lock path
		if err == nil {
			_, werr := f.Write(body)
			if cerr := f.Close(); werr != nil || cerr != nil {
				_ = os.Remove(path)
				return nil, errors.Join(werr, cerr)
			}
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // the caller's lock path
		var held lockBody
		if rerr == nil && json.Unmarshal(data, &held) == nil && SameProcess(pi, held.PID, held.Start) {
			return nil, &LockedError{Path: path, PID: held.PID}
		}
		_ = os.Remove(path) // stale or unreadable: its owner is gone
	}
	return nil, fmt.Errorf("could not take the lock %s", path)
}
