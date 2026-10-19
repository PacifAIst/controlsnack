// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package vm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
)

// pfVirtFirmwareEnabled is PF_VIRT_FIRMWARE_ENABLED for IsProcessorFeaturePresent.
const pfVirtFirmwareEnabled = 21

// cimScript asks WMI for what the doctor needs. Each query is isolated so that a denied one does not hide the others.
// It only reads.
const cimScript = `
$ErrorActionPreference = 'Stop'
$r = [ordered]@{}
try { $r.hypervisorPresent = [bool](Get-CimInstance -ClassName Win32_ComputerSystem).HypervisorPresent } catch { $r.csError = $_.Exception.Message }
try { $p = @(Get-CimInstance -ClassName Win32_Processor)[0]; $r.cpuName = [string]$p.Name; $r.firmwareVirt = [bool]$p.VirtualizationFirmwareEnabled } catch { $r.cpuError = $_.Exception.Message }
try { $r.features = @(Get-CimInstance -ClassName Win32_OptionalFeature | Where-Object { $_.Name -in 'HypervisorPlatform','VirtualMachinePlatform','Microsoft-Hyper-V-All' } | ForEach-Object { [ordered]@{ name = $_.Name; state = [int]$_.InstallState } }) } catch { $r.featError = $_.Exception.Message }
$r | ConvertTo-Json -Compress -Depth 5
`

// collectFacts reads the registry and WMI. It never elevates and changes nothing.
func collectFacts(ctx context.Context, h qemu.Host, _ *qemu.Install) Facts {
	f := Facts{OS: "windows", Arch: h.GOARCH()}
	w := &WindowsFacts{HypervisorPlatform: FeatureUnknown, VirtualMachinePlat: FeatureUnknown, HyperV: FeatureUnknown}
	f.Windows = w

	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		w.Edition, _, _ = k.GetStringValue("ProductName")
		w.EditionID, _, _ = k.GetStringValue("EditionID")
		w.Build, _, _ = k.GetStringValue("CurrentBuild")
		display, _, _ := k.GetStringValue("DisplayVersion")
		_ = k.Close()
		// Windows 11 still writes "Windows 10" into ProductName; the build number tells them apart.
		if n, err := strconv.Atoi(w.Build); err == nil && n >= 22000 {
			w.Edition = strings.Replace(w.Edition, "Windows 10", "Windows 11", 1)
		}
		f.OSVersion = strings.TrimSpace(w.Edition + " " + display + " (build " + w.Build + ")")
	} else {
		f.Problems = append(f.Problems, "registry: "+err.Error())
	}
	w.Home = strings.HasPrefix(w.EditionID, "Core")
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		f.CPUModel, _, _ = k.GetStringValue("ProcessorNameString")
		f.CPUModel = strings.Join(strings.Fields(f.CPUModel), " ")
		_ = k.Close()
	}
	// While a hypervisor runs this reads false whatever the firmware says; Diagnose never reports "off in the BIOS"
	// unless HypervisorPresent is false as well.
	w.FirmwareVirtualization = boolPtr(windows.IsProcessorFeaturePresent(pfVirtFirmwareEnabled))

	out, err := runPowerShell(ctx, cimScript)
	if err != nil {
		w.QueryDenied = true
		f.Problems = append(f.Problems, "WMI queries through PowerShell: "+oneLine(err.Error()))
	} else if cpu, problems, perr := applyCIM(out, w); perr != nil {
		f.Problems = append(f.Problems, perr.Error())
	} else {
		f.Problems = append(f.Problems, problems...)
		if f.CPUModel == "" {
			f.CPUModel = strings.Join(strings.Fields(cpu), " ")
		}
	}

	present, werr := whpxPresent()
	if werr != nil {
		w.WHvError = oneLine(werr.Error())
	} else {
		w.WHvHypervisor = boolPtr(present)
	}
	return f
}

// runPowerShell runs a script with Windows PowerShell, non-interactively, without a profile.
func runPowerShell(ctx context.Context, script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	exe := filepath.Join(os.Getenv("SystemRoot"), `System32\WindowsPowerShell\v1.0\powershell.exe`)
	u16 := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(u16)*2)
	for _, c := range u16 {
		buf = binary.LittleEndian.AppendUint16(buf, c)
	}
	cmd := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", base64.StdEncoding.EncodeToString(buf)) //nolint:gosec // fixed system executable and a constant script
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, &psError{msg}
		}
		return nil, err
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}

type psError struct{ msg string }

func (e *psError) Error() string { return e.msg }
