// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package overlay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxBackingName bounds the backing file name read from a qcow2 header (the format allows 1023 bytes).
const maxBackingName = 1023

// BackingFile returns the backing file recorded in a qcow2 file's header, or "" if it has none. It reads only the
// first 20 bytes and the name; it does not trust any other field of the file.
//
//	0   magic "QFI\xfb"     4  version      8  backing file offset (8 bytes)    16  backing file name size (4 bytes)
func BackingFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a disk path of this program
	if err != nil {
		return "", err
	}
	defer f.Close()
	var head [20]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return "", fmt.Errorf("%s is too short to be a qcow2 image", path)
	}
	if string(head[:4]) != "QFI\xfb" {
		return "", fmt.Errorf("%s is not a qcow2 image", path)
	}
	off, size := binary.BigEndian.Uint64(head[8:16]), binary.BigEndian.Uint32(head[16:20])
	if off == 0 || size == 0 {
		return "", nil
	}
	if size > maxBackingName {
		return "", fmt.Errorf("%s: the backing file name is %d bytes long", path, size)
	}
	name := make([]byte, size)
	if _, err := f.ReadAt(name, int64(off)); err != nil { //nolint:gosec // off is checked by ReadAt against the file
		return "", fmt.Errorf("%s: reading the backing file name: %w", path, err)
	}
	return string(name), nil
}

// sameFile reports whether two paths name the same location, comparing cleaned, slash-normalized paths (and
// ignoring case on Windows drive paths, where the file system does).
func sameFile(a, b string) bool {
	norm := func(p string) string {
		p = filepath.ToSlash(filepath.Clean(p))
		if len(p) >= 2 && p[1] == ':' {
			p = strings.ToLower(p)
		}
		return p
	}
	return norm(a) == norm(b)
}

// CheckBacking requires the overlay at disk to be backed by exactly the base image at want. An overlay whose
// backing file was changed would show the guest some other file as its disk.
func CheckBacking(disk, want string) error {
	got, err := BackingFile(disk)
	if err != nil {
		return err
	}
	if got == "" {
		return errors.New("the disk has no backing image; it was not made by controlsnack")
	}
	if !sameFile(got, want) {
		return fmt.Errorf("the disk is backed by %s, not by the base image %s", got, want)
	}
	return nil
}
