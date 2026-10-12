// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package vm

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// whvCapabilityCodeHypervisorPresent is WHV_CAPABILITY_CODE WHvCapabilityCodeHypervisorPresent.
const whvCapabilityCodeHypervisorPresent = 0

var (
	winHvPlatform     = windows.NewLazySystemDLL("WinHvPlatform.dll")
	procGetCapability = winHvPlatform.NewProc("WHvGetCapability")
)

// whpxPresent calls WHvGetCapability(WHvCapabilityCodeHypervisorPresent) through a lazy DLL call, so no cgo is
// involved. The output buffer is zeroed and 8 bytes long: the API writes a 32-bit BOOL but wants room for a
// 64-bit value.
//
// A missing DLL is an error. The answer is only "a hypervisor is running": measured on Windows 10 with the
// "Windows Hypervisor Platform" optional feature reported as disabled and WSL2 running, WinHvPlatform.dll was
// present, HypervisorPresent was TRUE and WHvCreatePartition succeeded (D-44). Whether QEMU can really use WHPX
// is therefore decided by the probe launch that follows, never by this call alone.
func whpxPresent() (bool, error) {
	if err := procGetCapability.Find(); err != nil {
		return false, fmt.Errorf("WinHvPlatform.dll cannot be used (the Windows Hypervisor Platform is not installed): %w", err)
	}
	var (
		buf     [8]byte
		written uint32
	)
	hr, _, _ := procGetCapability.Call(
		whvCapabilityCodeHypervisorPresent,
		uintptr(unsafe.Pointer(&buf[0])), //nolint:gosec // the documented calling convention of WHvGetCapability
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&written))) //nolint:gosec // as above
	if hr != 0 {
		return false, fmt.Errorf("WHvGetCapability(HypervisorPresent) failed: HRESULT 0x%08x", uint32(hr)) //nolint:gosec // an HRESULT is 32 bits
	}
	return binary.LittleEndian.Uint32(buf[:4]) != 0, nil
}
