// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin

package vm

func hvfSupported() (bool, error) { return false, errNotHere }
