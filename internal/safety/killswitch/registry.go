// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package killswitch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/platform"
)

// LockFile is the name of the active-session record inside the sessions directory.
const LockFile = "active.json"

// Entry is the content of the active-session lock (PLAN.md section 5.6). It is how `controlsnack kill` finds the
// running session and how a new run recognizes a stale one.
type Entry struct {
	SessionID         string    `json:"session_id"`
	ControllerPID     int       `json:"controller_pid"`
	ControllerStarted string    `json:"controller_started"`
	QEMUPID           int       `json:"qemu_pid,omitempty"`
	QEMUStarted       string    `json:"qemu_started,omitempty"`
	ControlURL        string    `json:"control_url"`
	KillToken         string    `json:"kill_token"`
	CreatedAt         time.Time `json:"created_at"`
}

// ErrSessionActive is returned when another live session already holds the lock: there is one active session per
// user (D-20).
type ErrSessionActive struct{ Entry Entry }

func (e *ErrSessionActive) Error() string {
	return fmt.Sprintf("another session is already running (id %s, controller pid %d); stop it first, or run `controlsnack kill`",
		e.Entry.SessionID, e.Entry.ControllerPID)
}

// ErrLockCorrupt is returned when the lock file exists but cannot be understood. It is never deleted
// automatically: it may belong to a live session, and guessing wrong would allow two sessions at once.
type ErrLockCorrupt struct {
	Path string
	Err  error
}

func (e *ErrLockCorrupt) Error() string {
	return fmt.Sprintf("the session lock %s is unreadable (%v); if no session is running, delete the file", e.Path, e.Err)
}
func (e *ErrLockCorrupt) Unwrap() error { return e.Err }

// Reaped describes a stale session that was cleaned up.
type Reaped struct {
	Entry      Entry
	KilledQEMU bool
}

// Registry manages the lock file in a sessions directory.
type Registry struct {
	dir  string
	proc platform.ProcessInspector
	now  func() time.Time
}

// NewRegistry creates a registry over dir (created on first use with mode 0700).
func NewRegistry(dir string, proc platform.ProcessInspector) *Registry {
	if proc == nil {
		proc = platform.OSProcesses{}
	}
	return &Registry{dir: dir, proc: proc, now: time.Now}
}

func (r *Registry) path() string { return filepath.Join(r.dir, LockFile) }

// Read returns the current lock entry, or nil if there is none.
func (r *Registry) Read() (*Entry, error) {
	data, err := os.ReadFile(r.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, &ErrLockCorrupt{Path: r.path(), Err: err}
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, &ErrLockCorrupt{Path: r.path(), Err: err}
	}
	if e.SessionID == "" || e.ControllerPID <= 0 {
		return nil, &ErrLockCorrupt{Path: r.path(), Err: errors.New("missing session id or controller pid")}
	}
	return &e, nil
}

// IsStale reports whether the session in e is dead: its controller no longer runs, or the PID now belongs to a
// different process (its start time differs).
func (r *Registry) IsStale(e *Entry) bool {
	return !platform.SameProcess(r.proc, e.ControllerPID, e.ControllerStarted)
}

// ReapStale removes the lock of a dead session and, if its QEMU is still running and provably the same process
// (alive, same start time), kills it. It returns nil if there is no lock or the session is alive.
func (r *Registry) ReapStale() (*Reaped, error) {
	e, err := r.Read()
	if err != nil || e == nil {
		return nil, err
	}
	if !r.IsStale(e) {
		return nil, nil
	}
	reaped := &Reaped{Entry: *e}
	if e.QEMUPID > 0 && platform.SameProcess(r.proc, e.QEMUPID, e.QEMUStarted) {
		if err := r.proc.Kill(e.QEMUPID); err != nil && !errors.Is(err, platform.ErrNoSuchProcess) {
			return nil, fmt.Errorf("killing the orphaned QEMU (pid %d): %w", e.QEMUPID, err)
		}
		reaped.KilledQEMU = true
	}
	if err := os.Remove(r.path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("removing the stale lock: %w", err)
	}
	return reaped, nil
}

// Lock is the lock held by the running session.
type Lock struct {
	r     *Registry
	mu    sync.Mutex
	entry Entry
	done  bool
}

// Acquire takes the active-session lock. A stale lock left by a dead session is reaped first (its orphaned QEMU
// killed); a live one fails with *ErrSessionActive. The file is created atomically with its full content, mode
// 0600, so a concurrent reader never sees a half-written lock and two starters cannot both succeed.
func (r *Registry) Acquire(e Entry) (lock *Lock, reaped *Reaped, err error) {
	if e.SessionID == "" || e.ControllerPID <= 0 {
		return nil, nil, errors.New("a lock needs a session id and the controller pid")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = r.now().UTC()
	}
	if err := platform.EnsureDir(r.dir); err != nil {
		return nil, nil, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := r.createExclusive(e)
		if err == nil {
			return &Lock{r: r, entry: e}, reaped, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, nil, err
		}
		cur, rerr := r.Read()
		if rerr != nil {
			return nil, nil, rerr
		}
		if cur == nil {
			continue // it vanished between our attempt and the read: try again
		}
		if !r.IsStale(cur) {
			return nil, nil, &ErrSessionActive{Entry: *cur}
		}
		got, rerr := r.ReapStale()
		if rerr != nil {
			return nil, nil, rerr
		}
		if got != nil {
			reaped = got
		}
	}
	return nil, nil, errors.New("could not take the session lock: it keeps changing under us")
}

// createExclusive writes the entry to a temporary file and links it into place, which fails if the lock exists.
func (r *Registry) createExclusive(e Entry) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.dir, ".active-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	// os.CreateTemp creates the file with mode 0600.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, r.path()); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fs.ErrExist
		}
		// Some file systems cannot hard-link; fall back to exclusive creation.
		f, ferr := os.OpenFile(r.path(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // fixed file name inside our own directory
		if ferr != nil {
			return ferr
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(r.path())
			return errors.Join(werr, cerr)
		}
	}
	return nil
}

// Entry returns a copy of the entry as last written.
func (l *Lock) Entry() Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.entry
}

// UpdateQEMU records the VM process once it exists so a later `kill` or reaper can end it. The file is replaced
// atomically.
func (l *Lock) UpdateQEMU(pid int, started string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return errors.New("the lock was released")
	}
	l.entry.QEMUPID, l.entry.QEMUStarted = pid, started
	return l.r.replace(l.entry)
}

// replace atomically overwrites the lock file.
func (r *Registry) replace(e Entry) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.dir, ".active-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	var last error
	for i := 0; i < 5; i++ { // Windows can refuse while a reader has the file open
		if last = os.Rename(tmpName, r.path()); last == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

// Release removes the lock, but only if it is still ours: a lock that was replaced by another session is left
// alone.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return nil
	}
	l.done = true
	cur, err := l.r.Read()
	if err != nil {
		return err
	}
	if cur == nil || cur.SessionID != l.entry.SessionID {
		return nil
	}
	if err := os.Remove(l.r.path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
