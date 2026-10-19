// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package vm

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
)

// collectFacts reads sysctl and asks codesign about the QEMU binary; it changes nothing.
func collectFacts(ctx context.Context, h qemu.Host, inst *qemu.Install) Facts {
	f := Facts{OS: "darwin", Arch: h.GOARCH()}
	d := &DarwinFacts{AppleSilicon: h.GOARCH() == "arm64"}
	f.Darwin = d

	if brand, err := unix.Sysctl("machdep.cpu.brand_string"); err == nil {
		f.CPUModel = brand
	} else if d.AppleSilicon {
		f.CPUModel = "Apple Silicon"
	}
	if ver := runTool(ctx, "sw_vers", "-productVersion"); ver != "" {
		f.OSVersion = "macOS " + ver
	}
	if v, err := unix.SysctlUint32("kern.hv_support"); err == nil {
		d.HVSupport = boolPtr(v == 1)
	} else {
		f.Problems = append(f.Problems, "sysctl kern.hv_support: "+err.Error())
	}
	if inst != nil {
		out, err := exec.CommandContext(withTimeout(ctx), "codesign", "-d", "--entitlements", "-", inst.System).CombinedOutput() //nolint:gosec // the discovered QEMU binary
		if err != nil && len(out) == 0 {
			d.EntitlementE = err.Error()
		} else {
			d.Entitlement = boolPtr(hasHypervisorEntitlement(string(out)))
		}
	}
	return f
}

func withTimeout(ctx context.Context) context.Context {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	go func() { <-c.Done(); cancel() }()
	return c
}

func runTool(ctx context.Context, name string, args ...string) string {
	out, err := exec.CommandContext(withTimeout(ctx), name, args...).Output() //nolint:gosec // fixed system tools
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
