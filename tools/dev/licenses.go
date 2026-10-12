// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"sync"
)

// ownModule is excluded from the report: it is our own AGPL code, not a dependency (D-00).
const ownModule = "github.com/PacifAIst/controlsnack-local"

func cmdLicenses(_ []string) error { return checkLicenses() }

// checkLicenses enforces the D-25 allowlist (Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, ISC) on the host
// binary for every host OS and on the guest agent, which is embedded in the host and so shipped with it.
func checkLicenses() error {
	for _, t := range hostTargets {
		if err := goLicensesCheck(t, hostPkg); err != nil {
			return fmt.Errorf("host %s: %w", t, err)
		}
	}
	// One agent architecture suffices: the dependency set does not depend on GOARCH.
	if err := goLicensesCheck(agentTargets[0], agentPkg); err != nil {
		return fmt.Errorf("agent %s: %w", agentTargets[0], err)
	}
	return nil
}

var (
	licensesOnce sync.Once
	licensesBin  string
	licensesErr  error
)

// goLicensesBinary builds go-licenses for the machine we are running on. `go tool go-licenses` cannot be used
// directly with GOOS set to another platform: it would build the tool for the target and fail to execute it.
func goLicensesBinary() (string, error) {
	licensesOnce.Do(func() {
		licensesBin, licensesErr = capture(nil, "go", "tool", "-n", "go-licenses")
	})
	return licensesBin, licensesErr
}

func goLicensesCheck(t target, pkg string) error {
	bin, err := goLicensesBinary()
	if err != nil {
		return err
	}
	return run(targetEnv(t), bin, "check", pkg, "--allowed_licenses="+allowedLicenses, "--ignore="+ownModule)
}
