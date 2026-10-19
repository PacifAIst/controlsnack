// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/PacifAIst/controlsnack/internal/config"
)

// addVMFlags registers the flags that choose how QEMU is found and accelerated; run, image build and doctor
// share them (PLAN.md P1-T03, P1-T04, D-32).
func addVMFlags(f *pflag.FlagSet) {
	d := config.Defaults()
	f.String("qemu", d.VM.QemuDir, "directory containing qemu-system-* and qemu-img (default: PATH and known install locations)")
	f.String("accel", d.VM.Accel, "accelerator: auto (detect), kvm (Linux), whpx (Windows), hvf (macOS) or tcg (software)")
	f.Bool("slow", d.VM.Slow, "run without hardware acceleration; shorthand for --accel=tcg")
}

func (a *app) newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Boot the VM, open the viewer and let the model work on a task",
		Long: `Boot a throwaway local Linux VM, open the viewer window and run the model loop.

The runtime VM never has a network device. --allow-net opens a policy-checked HTTP/HTTPS bridge only.
--unsafe relaxes limits and confirmations but never the kill switch, the audit log, the loopback-only viewer or the
no-NIC guarantee; it can only be requested on the command line.`,
		Example: `  controlsnack run --task "Open a terminal and print the kernel version"
  controlsnack run --llm ollama --model qwen2.5vl --step
  controlsnack run --no-llm --viewer none     # boot and hand over, no model`,
		Args:        noArgs,
		Annotations: map[string]string{annotationConfig: cfgValidateKey},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.env.Services.Runner.Run(cmd.Context(), a.cfg())
		},
	}

	d := config.Defaults()
	f := cmd.Flags()

	f.String("task", d.Session.Task, "task for the model (omit to type it in the viewer chat)")

	f.String("llm", d.LLM.Backend, "backend: auto, ollama, lmstudio, llamacpp, openai, anthropic")
	f.String("base-url", d.LLM.BaseURL, "backend base URL (needs an explicit --llm); non-local hosts must use https")
	f.String("model", d.LLM.Model, "model name (default: the first local model that supports vision)")
	f.String("api-key-env", d.LLM.APIKeyEnv, "NAME of the environment variable that holds the API key")
	f.String("tools", d.LLM.Tools, "tool profile: auto, full, compact")
	f.String("obs-format", d.LLM.ObsFormat, "observation format sent to the model: text, json")
	f.String("marks", d.LLM.Marks, "numbered set-of-marks overlay on the model's screenshot: auto, on, off")

	f.Bool("allow-net", d.Net.Allow, "let the guest reach the web over a policy-checked HTTP/HTTPS bridge (no LAN, no raw sockets)")
	f.StringSlice("allow-domain", d.Net.AllowDomains, "with --allow-net: only these domains (and subdomains) are reachable; repeatable")
	f.IntSlice("allow-port", d.Net.AllowPorts, "with --allow-net: allow this port in addition to 80 and 443; repeatable")

	f.Bool("keep", d.Session.Keep, "keep screenshots and the chat transcript after the session")
	f.String("keep-vm", d.Session.KeepVM, "keep the VM disk after the session: --keep-vm=NAME names it, a bare --keep-vm generates a name")
	f.Lookup("keep-vm").NoOptDefVal = "auto"
	f.String("vm", d.Session.VM, "resume the kept VM called NAME")

	f.Bool("unsafe", d.Safety.Unsafe, "relax limits and confirmations (never the kill switch, audit log or sandbox); flag only")
	f.Bool("step", d.Safety.Step, "ask for confirmation before every action")
	f.String("policy", d.Safety.Policy, "policy rule file merged over the built-in rules")
	f.Duration("timeout", d.Safety.Timeout.D(), "session timeout")
	f.Int("max-turns", d.Safety.MaxTurns, "model turns per task before a check-in")
	f.Float64("actions-per-sec", d.Safety.ActionsPerSec, "action rate limit (raising it needs --unsafe)")
	f.Int("actions-per-response", d.Safety.ActionsPerResponse, "actions executed per model response (raising it needs --unsafe)")
	f.Int("actions-per-task", d.Safety.ActionsPerTask, "actions per task before a check-in (raising it needs --unsafe)")

	f.String("viewer", d.Viewer.Mode, "viewer: window (app-mode browser window), browser (default browser tab), none, webview")

	f.String("resolution", d.VM.Resolution, "guest screen size WIDTHxHEIGHT, up to 1920x1080")
	f.Int("memory", d.VM.MemoryMiB, "guest memory in MiB")
	f.Int("cpus", d.VM.CPUs, "guest virtual CPUs")
	f.String("image", d.VM.Image, "base image id or path (default: the newest built image)")
	addVMFlags(f)

	f.Bool("no-llm", d.Session.NoLLM, "boot the VM and hand over without a model (for testing the sandbox)")
	return cmd
}
