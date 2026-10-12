// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package buildinfo

import (
	"fmt"
	"runtime"
)

// Version, Commit and Date are stamped at link time by tools/dev:
//
//	-ldflags "-X github.com/PacifAIst/controlsnack-local/internal/buildinfo.Version=v0.1.0 ..."
//
// A plain `go build` leaves the defaults.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info is an immutable snapshot of the build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Get returns the build metadata of the running binary.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// String renders the snapshot on one line, e.g. "dev (commit abc123, built unknown, go1.26.5 windows/amd64)".
func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s %s/%s)", i.Version, i.Commit, i.Date, i.GoVersion, i.OS, i.Arch)
}
