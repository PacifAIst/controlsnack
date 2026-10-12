// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package vm

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// collectFacts reads /proc, /sys and /dev; it starts nothing and changes nothing.
func collectFacts(_ context.Context, h qemu.Host, _ *qemu.Install) Facts {
	f := Facts{OS: h.GOOS(), Arch: h.GOARCH()}
	l := &LinuxFacts{}
	f.Linux = l

	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		f.OSVersion = parseOSRelease(string(data))
	}
	if f.OSVersion == "" {
		f.OSVersion = "Linux"
	}
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		f.CPUModel, l.VirtFlag, l.InVM = parseCPUInfo(string(data))
	} else {
		f.Problems = append(f.Problems, "/proc/cpuinfo: "+err.Error())
	}

	for _, m := range []string{"kvm_intel", "kvm_amd", "kvm"} {
		if _, err := os.Stat("/sys/module/" + m); err == nil {
			l.KVMModule = m
			break
		}
	}

	fi, err := os.Stat(kvmDevice)
	if err != nil {
		return f
	}
	l.DevKVM = true
	l.DevKVMMode = fmt.Sprintf("%04o", fi.Mode().Perm())
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		l.DevKVMOwner = strconv.Itoa(int(st.Uid))
		if u, err := user.LookupId(l.DevKVMOwner); err == nil {
			l.DevKVMOwner = u.Username
		}
		l.DevKVMGroup = strconv.Itoa(int(st.Gid))
		if g, err := user.LookupGroupId(l.DevKVMGroup); err == nil {
			l.DevKVMGroup = g.Name
		}
	}
	if err := openKVM(); err != nil {
		l.OpenError = err.Error()
	} else {
		l.UserCanOpen = true
	}
	if u, err := user.Current(); err == nil {
		if gids, err := u.GroupIds(); err == nil {
			for _, gid := range gids {
				if g, err := user.LookupGroupId(gid); err == nil && g.Name == "kvm" {
					l.UserInKVMGroup = true
				}
			}
		}
	}
	return f
}
