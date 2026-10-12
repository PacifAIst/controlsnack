// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !windows

package vm

func whpxPresent() (bool, error) { return false, errNotHere }
