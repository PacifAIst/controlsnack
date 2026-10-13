// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

// Main is the process entry point: it wires the real environment and signal handling and returns the exit code.
//
// SIGINT, SIGTERM and SIGHUP cancel the root context, which is how the run command learns it must take the kill
// path (PLAN.md P1-T05, section 6.8). A second signal does not wait for any cleanup and ends the process.
func Main() int {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, handledSignals(signal.Ignored)...)
	defer signal.Stop(sigs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forwardSignals(sigs, cancel, func() { os.Exit(exitcode.Killed) })

	return Run(ctx, Env{}, os.Args[1:])
}

// handledSignals lists the signals that stop a run. SIGHUP and SIGINT that were ignored when the process started
// stay ignored: that is what `nohup` and a background job in a script ask for, and signal.Notify would otherwise
// switch them back on. SIGTERM is always handled, so `kill <pid>` works whatever the parent did; the kill switch's
// other paths (the viewer, `controlsnack kill`) do not depend on signals at all.
func handledSignals(ignored func(os.Signal) bool) []os.Signal {
	sigs := []os.Signal{syscall.SIGTERM}
	for _, s := range []os.Signal{os.Interrupt, syscall.SIGHUP} {
		if !ignored(s) {
			sigs = append(sigs, s)
		}
	}
	return sigs
}

// forwardSignals cancels the context on the first signal and calls force on the second.
func forwardSignals(sigs <-chan os.Signal, cancel context.CancelFunc, force func()) {
	<-sigs
	cancel()
	<-sigs
	force()
}
