// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

// Facts are what the doctor learned about the machine, as plain data. The per-OS collectors fill them; Diagnose
// reads them. Nothing here changes the system: collecting is read-only (PLAN.md section 7.10, D-33).
type Facts struct {
	OS        string `json:"os"`   // runtime.GOOS
	Arch      string `json:"arch"` // runtime.GOARCH
	OSVersion string `json:"os_version,omitempty"`
	CPUModel  string `json:"cpu_model,omitempty"`

	Linux   *LinuxFacts   `json:"linux,omitempty"`
	Windows *WindowsFacts `json:"windows,omitempty"`
	Darwin  *DarwinFacts  `json:"darwin,omitempty"`

	// Problems are facts that could not be read (a query was denied, a tool is missing); they are reported, never fatal.
	Problems []string `json:"problems,omitempty"`
}

// FeatureState is the state of a Windows optional feature.
type FeatureState string

// The states of an optional feature as Win32_OptionalFeature reports them (InstallState 1 = enabled, 2 = disabled,
// 3 = absent), plus unknown when the query failed.
const (
	FeatureEnabled  FeatureState = "enabled"
	FeatureDisabled FeatureState = "disabled"
	FeatureAbsent   FeatureState = "absent"
	FeatureUnknown  FeatureState = "unknown"
)

// WindowsFacts are the Windows-specific facts of PLAN.md section 7.10.
type WindowsFacts struct {
	Edition   string `json:"edition,omitempty"`    // ProductName, e.g. "Windows 10 Pro"
	EditionID string `json:"edition_id,omitempty"` // "Core" is Home
	Build     string `json:"build,omitempty"`
	Home      bool   `json:"home"`

	// HypervisorPresent is Win32_ComputerSystem.HypervisorPresent: some hypervisor (Hyper-V, WSL2's, Windows
	// Sandbox, Memory Integrity) is running and holds VT-x. Nil when unknown.
	HypervisorPresent *bool `json:"hypervisor_present,omitempty"`
	// FirmwareVirtualization is whether the firmware has virtualization on. It reads false while a hypervisor is
	// running, which is why it is never reported as "off in the BIOS" in that case. Nil when unknown.
	FirmwareVirtualization *bool `json:"firmware_virtualization,omitempty"`

	HypervisorPlatform FeatureState `json:"feature_hypervisor_platform"`      // HypervisorPlatform: what WHPX needs
	VirtualMachinePlat FeatureState `json:"feature_virtual_machine_platform"` // used by WSL2, not required by ControlSnack
	HyperV             FeatureState `json:"feature_hyper_v"`
	WHvHypervisor      *bool        `json:"whv_hypervisor_present,omitempty"` // WHvGetCapability(HypervisorPresent)
	WHvError           string       `json:"whv_error,omitempty"`
	QueryDenied        bool         `json:"cim_query_denied,omitempty"`
}

// LinuxFacts are the Linux-specific facts.
type LinuxFacts struct {
	VirtFlag       string `json:"cpu_virtualization_flag,omitempty"` // vmx or svm; empty if neither (amd64)
	InVM           bool   `json:"inside_a_vm"`                       // the "hypervisor" CPU flag
	KVMModule      string `json:"kvm_module,omitempty"`              // kvm_intel, kvm_amd or kvm, when loaded
	DevKVM         bool   `json:"dev_kvm_exists"`
	DevKVMMode     string `json:"dev_kvm_mode,omitempty"`
	DevKVMOwner    string `json:"dev_kvm_owner,omitempty"`
	DevKVMGroup    string `json:"dev_kvm_group,omitempty"`
	UserCanOpen    bool   `json:"user_can_open_dev_kvm"`
	OpenError      string `json:"dev_kvm_open_error,omitempty"`
	UserInKVMGroup bool   `json:"user_in_kvm_group"`
}

// DarwinFacts are the macOS-specific facts.
type DarwinFacts struct {
	HVSupport    *bool  `json:"kern_hv_support,omitempty"`
	AppleSilicon bool   `json:"apple_silicon"`
	Entitlement  *bool  `json:"qemu_has_hypervisor_entitlement,omitempty"` // nil when it could not be checked
	EntitlementE string `json:"entitlement_error,omitempty"`
}

func boolPtr(b bool) *bool { return &b }
