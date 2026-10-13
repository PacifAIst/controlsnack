// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// PrepareOptions describe the files one VM launch needs. NewPrepare turns them into a PrepareFunc.
type PrepareOptions struct {
	Install qemu.Install
	// Base is the sealed base image; the VM runs on a fresh qcow2 overlay of it, which is deleted afterwards.
	Base string
	// Disk, when set, is used as it is instead of an overlay (a kept VM); it is never deleted here.
	Disk string
	// Cache is the disk cache mode of the spec ("" = unsafe); kept VMs use "writeback".
	Cache string
	// Name is the session id (the VM is named controlsnack-<Name>).
	Name string
	// Dir is the session directory. It is created if it is missing and holds the overlay, the serial log and the
	// UEFI variable store; the caller owns it and decides what stays after the session.
	Dir string
	// Net adds the second virtio-serial port of --allow-net.
	Net bool

	CPUs, MemoryMiB, Width, Height int
	// ExtraDirs are directories QEMU may touch in addition to Dir and the socket directory (a kept VM's directory).
	ExtraDirs []string
}

// NewPrepare returns the PrepareFunc of a launch: for every attempt a fresh socket directory (or loopback ports),
// a fresh overlay (unless Disk is set) and, on aarch64, a fresh copy of the UEFI variable store. Cleanup removes what
// the attempt created.
func NewPrepare(o PrepareOptions) (PrepareFunc, error) {
	switch {
	case o.Base == "" && o.Disk == "":
		return nil, errors.New("preparing a VM: neither a base image nor a disk")
	case !qemu.ValidName(o.Name):
		return nil, fmt.Errorf("preparing a VM: %q is not a valid VM name (1 to 40 characters of a-z, 0-9 and -)", o.Name)
	case o.Dir == "":
		return nil, errors.New("preparing a VM: no session directory")
	}
	return func(ctx context.Context, attempt int) (Prepared, error) {
		if err := os.MkdirAll(o.Dir, 0o700); err != nil {
			return Prepared{}, err
		}
		sockDir, cleanSock, err := qemu.NewSocketDir()
		if err != nil {
			return Prepared{}, err
		}
		var created []string
		cleanup := func() {
			cleanSock()
			for _, f := range created {
				_ = os.Remove(f)
			}
		}
		fail := func(err error) (Prepared, error) { cleanup(); return Prepared{}, err }

		disk := o.Disk
		if disk == "" {
			disk = filepath.Join(o.Dir, "disk.qcow2")
			_ = os.Remove(disk) // the overlay of a failed first attempt
			if err := image.CreateOverlay(ctx, o.Install.Img, o.Base, disk, ""); err != nil {
				return fail(err)
			}
			created = append(created, disk)
		}
		vars := ""
		if o.Install.Arch == qemu.ArchAarch64 {
			fw, err := o.Install.RequireFirmware()
			if err != nil {
				return fail(err)
			}
			vars = filepath.Join(o.Dir, "vars.fd")
			if err := copyFile(fw.Vars, vars); err != nil {
				return fail(err)
			}
			created = append(created, vars)
		}
		names := []string{"agent0", "qmp0"}
		if o.Net {
			names = append(names, "net0")
		}
		eps, err := qemu.AllocateEndpoints(ctx, runtime.GOOS, sockDir, names...)
		if err != nil {
			return fail(err)
		}
		spec := qemu.RuntimeSpec{
			Install: o.Install, Name: o.Name, CPUs: o.CPUs, MemoryMiB: o.MemoryMiB, Width: o.Width, Height: o.Height,
			Disk: disk, Cache: o.Cache, Vars: vars, Serial: filepath.Join(o.Dir, "serial.log"), Agent: eps[0], QMP: eps[1],
			AllowedDirs: append([]string{o.Dir, sockDir}, o.ExtraDirs...),
		}
		if o.Net {
			spec.Net = &eps[2]
		}
		if attempt > 1 {
			_ = os.Remove(spec.Serial) // the console of the first attempt is not this one's
		}
		return Prepared{Spec: spec, Dir: o.Dir, Cleanup: cleanup}, nil
	}, nil
}

// ParseResolution reads "WIDTHxHEIGHT" (the --resolution value; the configuration has already checked its range).
func ParseResolution(s string) (w, h int, err error) {
	ws, hs, ok := strings.Cut(s, "x")
	w, werr := strconv.Atoi(ws)
	h, herr := strconv.Atoi(hs)
	if !ok || werr != nil || herr != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("%q is not a screen size like 1280x800", s)
	}
	return w, h, nil
}
