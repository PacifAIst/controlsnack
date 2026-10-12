// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package safety

// Invariant is one of the guarantees that holds in every mode, --unsafe included (PLAN.md section 6.1, D-17).
// None of them has a configuration switch, and TestNoConfigKeyCanSwitchAnInvariantOff keeps it that way.
type Invariant struct {
	ID        int
	Name      string
	Statement string
	// Enforced names where the guarantee is implemented, for the README's guarantee table (PLAN.md section 14).
	Enforced string
}

var invariants = []Invariant{
	{1, "loopback-only listeners", "Every listener binds 127.0.0.1 only; startup aborts otherwise.",
		"platform.ListenLoopback; tools/dev check (repo step)"},
	{2, "no host-sharing devices in QEMU", "The QEMU argument list never contains host-sharing or network devices.",
		"safety.AssertQEMUArgv (closed option allowlist)"},
	{3, "no NIC, policy-checked egress only", "The runtime VM has no NIC; egress exists only through the policy dialer, which always blocks non-public addresses.",
		"safety.AssertQEMUArgv; safety/egress"},
	{4, "no host-side tools", "No tool executes on the host; the model has no host file or clipboard access.",
		"llm/actions has no host tool; internal/agent runs only in the guest"},
	{5, "kill switch cannot be disabled", "The kill switch works from every trigger and cannot be disabled.",
		"safety/killswitch"},
	{6, "audit always on", "The audit log is always written and hash-chained.",
		"safety/audit"},
	{7, "untrusted text is fenced", "Untrusted text is always fenced; the viewer renders text via textContent only.",
		"safety/untrusted; web/ (P6-T06 scan)"},
	{8, "viewer authentication", "The viewer requires the session cookie plus Host and Origin checks.",
		"internal/viewer"},
	{9, "schema validation first", "Every action is schema-validated before policy evaluation.",
		"llm/actions; session"},
}

// Invariants returns a copy of the nine guarantees of PLAN.md section 6.1.
func Invariants() []Invariant {
	return append([]Invariant(nil), invariants...)
}
