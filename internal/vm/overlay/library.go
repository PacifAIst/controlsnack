// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package overlay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
)

var (
	// ErrNoVM means there is no kept VM of that name.
	ErrNoVM = errors.New("no such kept VM")
	// ErrExists means a kept VM of that name already exists.
	ErrExists = errors.New("a kept VM of that name already exists")
)

var reName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// windowsReserved are names Windows will not let a directory have.
var windowsReserved = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true}

// ValidateName checks a kept VM's name: it becomes a directory name, so it is 1 to 40 lower-case letters, digits
// and hyphens, starting with a letter or digit, and not a name Windows reserves.
func ValidateName(name string) error {
	if !reName.MatchString(name) {
		return fmt.Errorf("%q is not a valid VM name: use 1 to 40 characters from a-z, 0-9 and -, starting with a letter or digit", name)
	}
	if windowsReserved[name] {
		return fmt.Errorf("%q is a reserved name on Windows", name)
	}
	return nil
}

// Meta is vm.json: what a kept VM was made from.
type Meta struct {
	Name     string    `json:"name"`
	BaseID   string    `json:"base_image_id"`
	BasePath string    `json:"base_image_path"`
	Arch     string    `json:"arch"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used"`
	Version  string    `json:"controlsnack_version"`
}

// VM is a kept VM: a directory with a disk that is an overlay on a base image.
type VM struct {
	Meta
	Dir  string
	Disk string // <dir>/disk.qcow2
	Vars string // <dir>/vars.fd: the UEFI variable store of an aarch64 guest, kept with the disk
}

// BaseMismatchError says a kept VM belongs to another base image than the one that would be used.
type BaseMismatchError struct {
	Name, Have, Want string
}

func (e *BaseMismatchError) Error() string {
	return fmt.Sprintf("kept VM %q was made from image %s, but image %s was selected; use --image %s, or remove the VM with 'controlsnack vm rm %s'",
		e.Name, e.Have, e.Want, e.Have, e.Name)
}

// Library is the directory of kept VMs, <state>/vms. Each VM is <name>/{disk.qcow2,vm.json}.
type Library struct {
	Dir string
	// Overlay makes an overlay disk (default image.CreateOverlay); tests substitute a fake.
	Overlay func(ctx context.Context, img, backing, dest, size string) error
	Locker  platform.ProcessInspector
	Now     func() time.Time
}

func (l Library) overlay() func(context.Context, string, string, string, string) error {
	if l.Overlay != nil {
		return l.Overlay
	}
	return image.CreateOverlay
}

func (l Library) locker() platform.ProcessInspector {
	if l.Locker != nil {
		return l.Locker
	}
	return platform.OSProcesses{}
}

func (l Library) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l Library) vmDir(name string) string { return filepath.Join(l.Dir, name) }

// lockPath is next to the VM directory, not in it, so removing the directory never has to deal with the lock.
func (l Library) lockPath(name string) string { return filepath.Join(l.Dir, "."+name+".lock") }

func (l Library) load(name string) (VM, error) {
	dir := l.vmDir(name)
	data, err := os.ReadFile(filepath.Join(dir, "vm.json")) //nolint:gosec // inside the library, name validated
	if errors.Is(err, os.ErrNotExist) {
		return VM{}, fmt.Errorf("%w: %s", ErrNoVM, name)
	}
	if err != nil {
		return VM{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return VM{}, fmt.Errorf("kept VM %q: vm.json is damaged: %w", name, err)
	}
	if m.Name != name {
		return VM{}, fmt.Errorf("kept VM %q: vm.json belongs to %q", name, m.Name)
	}
	return VM{Meta: m, Dir: dir, Disk: filepath.Join(dir, "disk.qcow2"), Vars: filepath.Join(dir, "vars.fd")}, nil
}

func (l Library) save(m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(l.vmDir(m.Name), "vm.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Create makes a new kept VM named name as an overlay on base. qemuImg is the qemu-img to use.
func (l Library) Create(ctx context.Context, qemuImg, name string, base image.Image, version string) (VM, error) {
	if err := ValidateName(name); err != nil {
		return VM{}, err
	}
	if base.Unmanaged {
		return VM{}, errors.New("a kept VM needs a base image from the image store, which has an id to remember it by")
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return VM{}, err
	}
	dir := l.vmDir(name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return VM{}, fmt.Errorf("%w: %s", ErrExists, name)
		}
		return VM{}, err
	}
	fail := func(err error) (VM, error) { _ = os.RemoveAll(dir); return VM{}, err }
	basePath, err := filepath.Abs(base.Path)
	if err != nil {
		return fail(err)
	}
	disk := filepath.Join(dir, "disk.qcow2")
	if err := l.overlay()(ctx, qemuImg, basePath, disk, ""); err != nil {
		return fail(err)
	}
	now := l.now().UTC()
	m := Meta{Name: name, BaseID: base.Manifest.ID, BasePath: basePath, Arch: base.Manifest.Arch, Created: now, LastUsed: now, Version: version}
	if err := l.save(m); err != nil {
		return fail(err)
	}
	return VM{Meta: m, Dir: dir, Disk: disk, Vars: filepath.Join(dir, "vars.fd")}, nil
}

// Open returns the kept VM, after checking that it belongs to base and that its disk really is an overlay on it.
func (l Library) Open(name string, base image.Image) (VM, error) {
	if err := ValidateName(name); err != nil {
		return VM{}, err
	}
	vm, err := l.load(name)
	if err != nil {
		return VM{}, err
	}
	if base.Unmanaged || vm.BaseID != base.Manifest.ID {
		want := base.Manifest.ID
		if base.Unmanaged {
			want = "(a file outside the store)"
		}
		return VM{}, &BaseMismatchError{Name: name, Have: vm.BaseID, Want: want}
	}
	if err := CheckBacking(vm.Disk, base.Path); err != nil {
		return VM{}, fmt.Errorf("kept VM %q: %w", name, err)
	}
	return vm, nil
}

// Acquire marks the VM as in use by this process, so two sessions never share one disk. The lock goes away with
// the process.
func (l Library) Acquire(name string) (release func(), err error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if _, err := l.load(name); err != nil {
		return nil, err
	}
	release, err = platform.AcquireLock(l.lockPath(name), l.locker())
	var locked *platform.LockedError
	if errors.As(err, &locked) {
		return nil, fmt.Errorf("kept VM %q is in use by another controlsnack session (process %d)", name, locked.PID)
	}
	return release, err
}

// Touch records that the VM was used now.
func (l Library) Touch(name string) error {
	vm, err := l.load(name)
	if err != nil {
		return err
	}
	vm.LastUsed = l.now().UTC()
	return l.save(vm.Meta)
}

// List returns the kept VMs, most recently used first, and a description of every directory that could not be read.
func (l Library) List() (vms []VM, problems []string, err error) {
	entries, err := os.ReadDir(l.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || ValidateName(e.Name()) != nil {
			continue
		}
		vm, lerr := l.load(e.Name())
		if lerr != nil {
			problems = append(problems, lerr.Error())
			continue
		}
		vms = append(vms, vm)
	}
	sort.Slice(vms, func(i, j int) bool {
		if !vms[i].LastUsed.Equal(vms[j].LastUsed) {
			return vms[i].LastUsed.After(vms[j].LastUsed)
		}
		return vms[i].Name < vms[j].Name
	})
	return vms, problems, nil
}

// Remove deletes a kept VM. A VM that a session is using is not removed.
func (l Library) Remove(name string) (VM, error) {
	if err := ValidateName(name); err != nil {
		return VM{}, err
	}
	vm, err := l.load(name)
	if err != nil {
		return VM{}, err
	}
	release, err := l.Acquire(name)
	if err != nil {
		return vm, err
	}
	defer release()
	// os.RemoveAll deletes a symbolic link itself and never follows it out of the directory.
	if err := os.RemoveAll(vm.Dir); err != nil {
		return vm, err
	}
	return vm, nil
}
