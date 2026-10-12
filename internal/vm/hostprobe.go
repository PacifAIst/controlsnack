// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"errors"

	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// errNotHere is what the checks of another OS's accelerator return.
var errNotHere = errors.New("not applicable on this operating system")

// osProbe is the HostProbe of the running machine. The three hardware checks are implemented per OS in
// accel_kvm_*.go, accel_whpx_*.go and accel_hvf_*.go; on the other systems they return errNotHere, so every
// target builds with CGO_ENABLED=0.
type osProbe struct {
	host qemu.Host
	inst qemu.Install
}

// NewHostProbe returns the HostProbe for a machine on which QEMU was found as inst.
func NewHostProbe(h qemu.Host, inst qemu.Install) HostProbe { return osProbe{host: h, inst: inst} }

func (o osProbe) GOOS() string                { return o.host.GOOS() }
func (o osProbe) QEMUAccels() []string        { return o.inst.Accels }
func (o osProbe) OpenKVM() error              { return openKVM() }
func (o osProbe) WHPXPresent() (bool, error)  { return whpxPresent() }
func (o osProbe) HVFSupported() (bool, error) { return hvfSupported() }
func (o osProbe) ProbeQEMU(ctx context.Context, accel string) error {
	return qemu.ProbeAccel(ctx, o.host, o.inst, accel)
}
