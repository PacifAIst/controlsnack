// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package killswitch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/platform"
)

// fakeProcs is an in-memory process table.
type fakeProcs struct {
	mu     sync.Mutex
	procs  map[int]*fakeProc
	killed []int
}

type fakeProc struct {
	start string
	alive bool
}

func newFakeProcs() *fakeProcs { return &fakeProcs{procs: map[int]*fakeProc{}} }

func (f *fakeProcs) add(pid int, start string) {
	f.mu.Lock()
	f.procs[pid] = &fakeProc{start: start, alive: true}
	f.mu.Unlock()
}

func (f *fakeProcs) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.procs[pid]
	return ok && p.alive
}

func (f *fakeProcs) StartTime(pid int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.procs[pid]
	if !ok || !p.alive {
		return "", platform.ErrNoSuchProcess
	}
	return p.start, nil
}

func (f *fakeProcs) Kill(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.procs[pid]
	if !ok || !p.alive {
		return platform.ErrNoSuchProcess
	}
	p.alive = false
	f.killed = append(f.killed, pid)
	return nil
}

func (f *fakeProcs) killedList() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.killed...)
}

func entry(id string, ctrl int) Entry {
	return Entry{SessionID: id, ControllerPID: ctrl, ControllerStarted: "c-start", ControlURL: "http://127.0.0.1:9", KillToken: "tok", CreatedAt: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
}

func newRegistry(t *testing.T, procs *fakeProcs) *Registry {
	t.Helper()
	return NewRegistry(filepath.Join(t.TempDir(), "sessions"), procs)
}

func TestAcquireWritesTheLockFile(t *testing.T) {
	procs := newFakeProcs()
	procs.add(100, "c-start")
	reg := newRegistry(t, procs)
	lock, reaped, err := reg.Acquire(entry("s_one", 100))
	if err != nil || reaped != nil {
		t.Fatalf("%v %v", err, reaped)
	}
	data, err := os.ReadFile(filepath.Join(reg.dir, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Entry
	if err := json.Unmarshal(data, &onDisk); err != nil || onDisk.SessionID != "s_one" || onDisk.ControllerPID != 100 || onDisk.KillToken != "tok" {
		t.Fatalf("%v %+v", err, onDisk)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(reg.dir, LockFile))
		if info.Mode().Perm() != 0o600 {
			t.Errorf("lock mode %v, want 0600: it holds the kill token", info.Mode().Perm())
		}
		dinfo, _ := os.Stat(reg.dir)
		if dinfo.Mode().Perm() != 0o700 {
			t.Errorf("sessions dir mode %v, want 0700", dinfo.Mode().Perm())
		}
	}
	// No temporary files are left behind.
	entries, _ := os.ReadDir(reg.dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d files, want only the lock", len(entries))
	}
	if lock.Entry().SessionID != "s_one" || lock.Entry().CreatedAt.IsZero() {
		t.Errorf("%+v", lock.Entry())
	}
	if got, err := reg.Read(); err != nil || got == nil || got.SessionID != "s_one" {
		t.Errorf("Read: %v %+v", err, got)
	}
}

func TestOnlyOneLiveSession(t *testing.T) {
	procs := newFakeProcs()
	procs.add(100, "c-start")
	reg := newRegistry(t, procs)
	if _, _, err := reg.Acquire(entry("s_one", 100)); err != nil {
		t.Fatal(err)
	}
	procs.add(200, "c-start")
	_, _, err := reg.Acquire(entry("s_two", 200))
	var active *ErrSessionActive
	if !errors.As(err, &active) || active.Entry.SessionID != "s_one" {
		t.Fatalf("a second session must be refused while the first lives: %v", err)
	}
	if len(procs.killedList()) != 0 {
		t.Error("a live session must never be reaped")
	}
}

func TestRacingStartersHaveExactlyOneWinner(t *testing.T) {
	procs := newFakeProcs()
	reg := newRegistry(t, procs)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, losses := 0, 0
	for i := 1; i <= 12; i++ {
		procs.add(1000+i, "c-start")
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := reg.Acquire(entry("s_race", 1000+i))
			mu.Lock()
			defer mu.Unlock()
			var active *ErrSessionActive
			switch {
			case err == nil:
				wins++
			case errors.As(err, &active):
				losses++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || losses != 11 {
		t.Fatalf("wins=%d losses=%d; exactly one starter may hold the lock", wins, losses)
	}
}

func TestStaleLockIsReapedAndItsQEMUKilled(t *testing.T) {
	procs := newFakeProcs()
	reg := newRegistry(t, procs)
	// A previous run crashed: controller 100 is gone, its QEMU 101 still runs.
	procs.add(101, "q-start")
	old := entry("s_dead", 100)
	old.QEMUPID, old.QEMUStarted = 101, "q-start"
	if _, _, err := reg.Acquire(old); err != nil {
		t.Fatal(err)
	}
	procs.mu.Lock()
	delete(procs.procs, 100) // controller dead (it was never registered alive)
	procs.mu.Unlock()

	procs.add(200, "c-start")
	lock, reaped, err := reg.Acquire(entry("s_new", 200))
	if err != nil {
		t.Fatal(err)
	}
	if reaped == nil || reaped.Entry.SessionID != "s_dead" || !reaped.KilledQEMU {
		t.Fatalf("reaped = %+v", reaped)
	}
	if got := procs.killedList(); len(got) != 1 || got[0] != 101 {
		t.Errorf("killed %v, want only the orphaned QEMU 101", got)
	}
	if lock.Entry().SessionID != "s_new" {
		t.Error("the new session holds the lock")
	}
	cur, _ := reg.Read()
	if cur.SessionID != "s_new" {
		t.Errorf("lock now belongs to %s", cur.SessionID)
	}
}

func TestPIDReuseNeverKillsAnInnocentProcess(t *testing.T) {
	procs := newFakeProcs()
	reg := newRegistry(t, procs)
	old := entry("s_dead", 100)
	old.QEMUPID, old.QEMUStarted = 101, "q-start"
	if _, _, err := reg.Acquire(old); err != nil {
		t.Fatal(err)
	}
	// Both PIDs have since been taken by unrelated processes with different start times.
	procs.add(100, "someone-elses-start")
	procs.add(101, "an-editor-started-later")

	procs.add(200, "c-start")
	_, reaped, err := reg.Acquire(entry("s_new", 200))
	if err != nil {
		t.Fatalf("a recycled controller pid means the old session is dead: %v", err)
	}
	if reaped == nil || reaped.KilledQEMU {
		t.Fatalf("reaped = %+v", reaped)
	}
	if got := procs.killedList(); len(got) != 0 {
		t.Fatalf("killed %v: processes that merely reuse a recorded pid must be left alone", got)
	}
}

func TestACorruptLockIsNeverDeleted(t *testing.T) {
	procs := newFakeProcs()
	reg := newRegistry(t, procs)
	if err := os.MkdirAll(reg.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"garbage": "{not json", "empty": "", "no pid": `{"session_id":"s_x"}`, "no id": `{"controller_pid":5}`} {
		path := filepath.Join(reg.dir, LockFile)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		procs.add(300, "c-start")
		_, _, err := reg.Acquire(entry("s_new", 300))
		var corrupt *ErrLockCorrupt
		if !errors.As(err, &corrupt) {
			t.Errorf("%s: got %v, want ErrLockCorrupt", name, err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("%s: the unreadable lock must be left in place", name)
		}
		if _, err := reg.ReapStale(); err == nil {
			t.Errorf("%s: ReapStale must not guess", name)
		}
	}
}

func TestUpdateQEMUAndRelease(t *testing.T) {
	procs := newFakeProcs()
	procs.add(100, "c-start")
	reg := newRegistry(t, procs)
	lock, _, err := reg.Acquire(entry("s_one", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.UpdateQEMU(555, "q-777"); err != nil {
		t.Fatal(err)
	}
	cur, _ := reg.Read()
	if cur.QEMUPID != 555 || cur.QEMUStarted != "q-777" || cur.KillToken != "tok" {
		t.Errorf("%+v", cur)
	}
	if lock.Entry().QEMUPID != 555 {
		t.Error("the in-memory entry follows")
	}
	entries, _ := os.ReadDir(reg.dir)
	if len(entries) != 1 {
		t.Errorf("update left temporary files behind: %d entries", len(entries))
	}

	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if cur, _ := reg.Read(); cur != nil {
		t.Error("released lock must be gone")
	}
	if err := lock.Release(); err != nil {
		t.Errorf("Release twice: %v", err)
	}
	if err := lock.UpdateQEMU(1, "x"); err == nil {
		t.Error("updating a released lock must fail")
	}
}

func TestReleaseLeavesAForeignLockAlone(t *testing.T) {
	procs := newFakeProcs()
	procs.add(100, "c-start")
	reg := newRegistry(t, procs)
	lock, _, _ := reg.Acquire(entry("s_one", 100))
	// Something replaced our lock (a forced cleanup, a second session after a reap).
	if err := os.Remove(filepath.Join(reg.dir, LockFile)); err != nil {
		t.Fatal(err)
	}
	procs.add(200, "c-start")
	if _, _, err := reg.Acquire(entry("s_other", 200)); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	cur, _ := reg.Read()
	if cur == nil || cur.SessionID != "s_other" {
		t.Errorf("releasing s_one removed s_other's lock: %+v", cur)
	}
}

func TestAcquireValidatesInput(t *testing.T) {
	reg := newRegistry(t, newFakeProcs())
	if _, _, err := reg.Acquire(Entry{}); err == nil {
		t.Error("an entry without session and pid is invalid")
	}
	if _, _, err := reg.Acquire(Entry{SessionID: "s", ControllerPID: 0}); err == nil {
		t.Error("pid 0 is invalid")
	}
}

func TestReapStaleWithNothingToReap(t *testing.T) {
	procs := newFakeProcs()
	reg := newRegistry(t, procs)
	if r, err := reg.ReapStale(); r != nil || err != nil {
		t.Errorf("no lock: %v %v", r, err)
	}
	procs.add(100, "c-start")
	_, _, _ = reg.Acquire(entry("s_one", 100))
	if r, err := reg.ReapStale(); r != nil || err != nil {
		t.Errorf("a live session is not reaped: %v %v", r, err)
	}
}

func TestRegistryWithTheRealProcessTable(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "sessions"), nil) // nil = the operating system
	self := os.Getpid()
	start, err := platform.OSProcesses{}.StartTime(self)
	if err != nil {
		t.Skipf("no start time on this platform: %v", err)
	}
	e := entry("s_real", self)
	e.ControllerStarted = start
	lock, _, err := reg.Acquire(e)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	cur, _ := reg.Read()
	if reg.IsStale(cur) {
		t.Error("this very process is alive and must not look stale")
	}
	if _, _, err := reg.Acquire(entry("s_two", self)); err == nil {
		t.Error("the live lock must refuse a second session")
	}
}
