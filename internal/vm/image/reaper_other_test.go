// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package image

// dispatchReaper has nothing to do where there is no reaper.
func dispatchReaper() {}
