// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package platform

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// processStartTime reads field 22 of /proc/PID/stat, the start time in clock ticks since boot.
func processStartTime(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoSuchProcess
		}
		return "", err
	}
	return parseProcStatStart(string(data))
}

// parseProcStatStart extracts the start time from the content of /proc/PID/stat. The command name is in
// parentheses and may itself contain spaces and parentheses, so parsing starts after the last ')'.
func parseProcStatStart(stat string) (string, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return "", fmt.Errorf("unexpected /proc stat format")
	}
	fields := strings.Fields(stat[i+1:])
	// fields[0] is the state (field 3), so the start time (field 22) is fields[19].
	if len(fields) < 20 {
		return "", fmt.Errorf("unexpected /proc stat format: %d fields", len(fields))
	}
	return "linux:" + fields[19], nil
}
