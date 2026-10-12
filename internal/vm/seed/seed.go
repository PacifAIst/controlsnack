// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
)

// Label is the volume label cloud-init's NoCloud datasource looks for.
const Label = "cidata"

const (
	blockSize = 2048
	// maxFile bounds each of the files on the seed; the real ones are well under 200 KiB.
	maxFile = 4 << 20
	// overhead is room for the system area, the volume descriptors (primary and Joliet), path tables and directory
	// records, which the library does not size for us.
	overhead = 512 << 10
)

// Files are the NoCloud files. user-data and meta-data are required; network-config is optional.
type Files struct {
	UserData      []byte
	MetaData      []byte
	NetworkConfig []byte
}

// The file names NoCloud reads.
const (
	NameUserData      = "user-data"
	NameMetaData      = "meta-data"
	NameNetworkConfig = "network-config"
)

func (f Files) validate() error {
	check := func(name string, data []byte, required bool) error {
		switch {
		case len(data) == 0 && required:
			return fmt.Errorf("%s is empty", name)
		case len(data) > maxFile:
			return fmt.Errorf("%s is %d bytes, more than the %d allowed", name, len(data), maxFile)
		case !utf8.Valid(data):
			return fmt.Errorf("%s is not valid UTF-8", name)
		}
		for _, b := range data {
			if b == 0 {
				return fmt.Errorf("%s contains a NUL byte", name)
			}
		}
		return nil
	}
	return errors.Join(check(NameUserData, f.UserData, true), check(NameMetaData, f.MetaData, true), check(NameNetworkConfig, f.NetworkConfig, false))
}

// Build writes a NoCloud seed ISO to path: volume label "cidata", the files at the top level under their own names
// (Rock Ridge and Joliet keep the lower-case names with the hyphen; plain ISO 9660 would not). The file appears
// atomically, and an existing one is replaced.
func Build(path string, f Files) error {
	if err := f.validate(); err != nil {
		return err
	}
	content := int64(len(f.UserData) + len(f.MetaData) + len(f.NetworkConfig))
	size := (overhead + 2*content + blockSize - 1) / blockSize * blockSize

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".seed-*.iso")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // gone after a successful rename
	if err := tmp.Truncate(size); err != nil {
		_ = tmp.Close()
		return err
	}
	workspace, err := os.MkdirTemp("", "cs-seed-")
	if err != nil {
		_ = tmp.Close()
		return err
	}
	defer func() { _ = os.RemoveAll(workspace) }()

	fsys, err := iso9660.Create(file.New(tmp, false), size, 0, blockSize, workspace)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("creating the ISO: %w", err)
	}
	for _, e := range []struct {
		name string
		data []byte
	}{{NameUserData, f.UserData}, {NameMetaData, f.MetaData}, {NameNetworkConfig, f.NetworkConfig}} {
		if len(e.data) == 0 {
			continue
		}
		w, err := fsys.OpenFile("/"+e.name, os.O_CREATE|os.O_RDWR)
		if err != nil {
			_ = tmp.Close()
			return fmt.Errorf("adding %s: %w", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("writing %s: %w", e.name, err)
		}
		if err := w.Close(); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("writing %s: %w", e.name, err)
		}
	}
	if err := fsys.Finalize(iso9660.FinalizeOptions{RockRidge: true, Joliet: true, VolumeIdentifier: Label}); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("finalizing the ISO: %w", err)
	}
	if err := padDescriptor(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fixing the volume descriptor: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// The text fields of the primary volume descriptor (ECMA-119 8.4) as [start, end) byte ranges: system identifier,
// volume identifier, volume set, publisher, data preparer, application, and the three file identifiers.
var descriptorTextFields = [][2]int{{8, 40}, {40, 72}, {190, 318}, {318, 446}, {446, 574}, {574, 702}, {702, 739}, {739, 776}, {776, 813}}

// primaryDescriptorOffset is where the primary volume descriptor starts: sector 16.
const primaryDescriptorOffset = 16 * blockSize

// padDescriptor makes the text fields of the primary volume descriptor spec-conforming: they are padded with
// spaces, and the library pads them with NUL bytes (blkid copes with that, a strict reader may not; the Joliet
// descriptor is already padded correctly).
func padDescriptor(f *os.File) error {
	desc := make([]byte, blockSize)
	if _, err := f.ReadAt(desc, primaryDescriptorOffset); err != nil {
		return err
	}
	if desc[0] != 1 || string(desc[1:6]) != "CD001" {
		return errors.New("sector 16 is not a primary volume descriptor")
	}
	for _, r := range descriptorTextFields {
		for i := r[0]; i < r[1]; i++ {
			if desc[i] == 0 {
				desc[i] = ' '
			}
		}
	}
	_, err := f.WriteAt(desc, primaryDescriptorOffset)
	return err
}

// Read opens a seed ISO and returns its volume label and the NoCloud files in it. Missing optional files come back
// empty.
func Read(path string) (label string, f Files, err error) {
	in, err := os.Open(path) //nolint:gosec // a path the caller chose
	if err != nil {
		return "", Files{}, err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return "", Files{}, err
	}
	fsys, err := iso9660.Read(file.New(in, true), st.Size(), 0, blockSize)
	if err != nil {
		return "", Files{}, fmt.Errorf("reading the ISO: %w", err)
	}
	label = fsys.Label()
	for _, e := range []struct {
		name string
		dst  *[]byte
	}{{NameUserData, &f.UserData}, {NameMetaData, &f.MetaData}, {NameNetworkConfig, &f.NetworkConfig}} {
		// io/fs rules: unrooted names.
		data, rerr := readFile(fsys, e.name)
		if rerr == nil {
			*e.dst = data
		}
	}
	return label, f, nil
}

func readFile(fsys *iso9660.FileSystem, name string) ([]byte, error) {
	entries, err := fsys.ReadDir(".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Name() == name {
			return fsys.ReadFile(name)
		}
	}
	return nil, os.ErrNotExist
}
