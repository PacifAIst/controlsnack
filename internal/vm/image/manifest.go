// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"time"
)

// maxManifestBytes bounds a manifest file; a real one is a few kilobytes.
const maxManifestBytes = 256 << 10

// Guest architectures, as `uname -m` prints them.
const (
	ArchX86_64  = "x86_64"
	ArchAarch64 = "aarch64"
)

// Upstream records where a base image came from and what was verified about it (PLAN.md section 7.5).
type Upstream struct {
	URL    string `json:"url"`
	SHA512 string `json:"sha512"`
	// SignatureVerified is true only if a signature over the checksum file was checked against a pinned key.
	SignatureVerified bool `json:"signature_verified"`
	// SignatureNote says why not, e.g. that the publisher offers no signature.
	SignatureNote string `json:"signature_note,omitempty"`
}

// Manifest describes a sealed base image. It is written next to the image and is the only record of how the image
// was made.
type Manifest struct {
	ID           string    `json:"id"` // the first 12 hex digits of SHA256
	Created      time.Time `json:"created"`
	Version      string    `json:"controlsnack_version"`
	RecipeSHA256 string    `json:"recipe_sha256"` // the guest recipe (templates, package list) the image was built from
	Upstream     Upstream  `json:"upstream"`
	Arch         string    `json:"arch"`
	SHA256       string    `json:"sha256"` // of the sealed qcow2 file
	Size         int64     `json:"size"`
	Packages     []string  `json:"packages,omitempty"`
}

var (
	reID      = regexp.MustCompile(`^[0-9a-f]{12}$`)
	reHex64   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reHex128  = regexp.MustCompile(`^[0-9a-f]{128}$`)
	rePackage = regexp.MustCompile(`^[a-z0-9][a-z0-9+.\-]{0,99}$`)
)

// IDOf derives an image id from the file's SHA-256.
func IDOf(sha256 string) string {
	if len(sha256) < 12 {
		return ""
	}
	return sha256[:12]
}

// Validate checks every field. An imported manifest is attacker-controlled text, so nothing is taken on trust.
func (m Manifest) Validate() error {
	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }
	switch {
	case !reHex64.MatchString(m.SHA256):
		add("sha256 is not 64 lowercase hex digits")
	case m.ID != IDOf(m.SHA256) || !reID.MatchString(m.ID):
		add("id %q is not the first 12 digits of the sha256", m.ID)
	}
	if m.Arch != ArchX86_64 && m.Arch != ArchAarch64 {
		add("arch %q is not x86_64 or aarch64", m.Arch)
	}
	if m.Size <= 0 || m.Size > 1<<40 {
		add("size %d is not plausible", m.Size)
	}
	if m.Created.IsZero() {
		add("created is missing")
	}
	if !reHex64.MatchString(m.RecipeSHA256) {
		add("recipe_sha256 is not 64 lowercase hex digits")
	}
	if !reHex128.MatchString(m.Upstream.SHA512) {
		add("upstream sha512 is not 128 lowercase hex digits")
	}
	if u, err := url.Parse(m.Upstream.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		add("upstream url %q is not an https URL", m.Upstream.URL)
	}
	if len(m.Version) == 0 || len(m.Version) > 128 {
		add("controlsnack_version is missing or too long")
	}
	if len(m.Packages) > 1000 {
		add("%d packages is too many", len(m.Packages))
	}
	for _, p := range m.Packages {
		if !rePackage.MatchString(p) {
			add("package name %q is not a Debian package name", p)
			break
		}
	}
	if len(m.Upstream.SignatureNote) > 512 {
		add("signature_note is too long")
	}
	return errors.Join(problems...)
}

// ReadManifest reads and validates a manifest file, strictly: unknown fields and trailing data are errors.
func ReadManifest(path string) (Manifest, error) {
	f, err := os.Open(path) //nolint:gosec // a manifest path the caller chose
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, err
	}
	return ParseManifest(data)
}

// ParseManifest decodes and validates manifest bytes.
func ParseManifest(data []byte) (Manifest, error) {
	if len(data) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("manifest is larger than %d bytes", maxManifestBytes)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if dec.More() {
		return Manifest{}, errors.New("manifest: data after the JSON object")
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("manifest is invalid: %w", err)
	}
	return m, nil
}

// Encode returns the manifest as indented JSON.
func (m Manifest) Encode() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
