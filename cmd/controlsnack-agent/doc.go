// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Command controlsnack-agent is the guest agent. The host pushes it into the VM over virtio-serial at the first
// boot of every session (PLAN.md D-04); it runs as the unprivileged user cs inside the XFCE session.
//
// The agent is Linux-only and always built with CGO_ENABLED=0 for linux/amd64 and linux/arm64. On other
// operating systems main_other.go provides a stub so that `go build ./...` and `go vet ./...` stay green.
package main
