// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The parsers below turn what the operating system prints into Facts. They are plain functions of text, so they are
// tested on every OS with samples taken from real machines; the collectors that call them are per-OS.

// parseCPUInfo reads /proc/cpuinfo: the model, which hardware virtualization flag the CPU has (vmx or svm), and
// whether the CPU reports it runs under a hypervisor.
func parseCPUInfo(s string) (model, virtFlag string, inVM bool) {
	for _, line := range strings.Split(s, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch {
		case (key == "model name" || key == "Hardware" || key == "cpu model") && model == "":
			model = val
		case key == "flags" || key == "Features":
			for _, flag := range strings.Fields(val) {
				switch flag {
				case "vmx", "svm":
					if virtFlag == "" {
						virtFlag = flag
					}
				case "hypervisor":
					inVM = true
				}
			}
		}
	}
	return model, virtFlag, inVM
}

// parseOSRelease returns PRETTY_NAME from /etc/os-release.
func parseOSRelease(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// cimResult is the JSON the Windows collector's PowerShell script prints.
type cimResult struct {
	HypervisorPresent *bool  `json:"hypervisorPresent"`
	CSError           string `json:"csError"`
	CPUName           string `json:"cpuName"`
	FirmwareVirt      *bool  `json:"firmwareVirt"`
	CPUError          string `json:"cpuError"`
	Features          []struct {
		Name  string `json:"name"`
		State int    `json:"state"`
	} `json:"features"`
	FeatError string `json:"featError"`
}

// applyCIM merges the PowerShell output into w and returns the CPU name. Failures of individual queries become
// problems rather than errors: a denied query must not stop the report (PLAN.md section 7.10).
func applyCIM(data []byte, w *WindowsFacts) (cpuName string, problems []string, err error) {
	var r cimResult
	if err := json.Unmarshal(data, &r); err != nil {
		return "", nil, fmt.Errorf("the PowerShell output is not JSON: %w", err)
	}
	if r.HypervisorPresent != nil {
		w.HypervisorPresent = r.HypervisorPresent
	}
	if r.FirmwareVirt != nil {
		w.FirmwareVirtualization = r.FirmwareVirt
	}
	w.HypervisorPlatform, w.VirtualMachinePlat, w.HyperV = FeatureUnknown, FeatureUnknown, FeatureUnknown
	for _, f := range r.Features {
		switch f.Name {
		case "HypervisorPlatform":
			w.HypervisorPlatform = featureState(f.State)
		case "VirtualMachinePlatform":
			w.VirtualMachinePlat = featureState(f.State)
		case "Microsoft-Hyper-V-All":
			w.HyperV = featureState(f.State)
		}
	}
	for _, e := range []struct{ what, msg string }{{"Win32_ComputerSystem", r.CSError}, {"Win32_Processor", r.CPUError}, {"Win32_OptionalFeature", r.FeatError}} {
		if e.msg != "" {
			problems = append(problems, e.what+": "+oneLine(e.msg))
			w.QueryDenied = true
		}
	}
	return r.CPUName, problems, nil
}

// featureState maps Win32_OptionalFeature.InstallState.
func featureState(s int) FeatureState {
	switch s {
	case 1:
		return FeatureEnabled
	case 2:
		return FeatureDisabled
	case 3:
		return FeatureAbsent
	}
	return FeatureUnknown
}

// hasHypervisorEntitlement reports whether `codesign -d --entitlements -` output grants the hypervisor entitlement.
func hasHypervisorEntitlement(out string) bool {
	return strings.Contains(out, "com.apple.security.hypervisor")
}

// oneLine collapses all white space, newlines included, to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
