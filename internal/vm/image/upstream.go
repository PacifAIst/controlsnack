// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"context"
	"fmt"
	"strings"
)

// maxChecksumFile bounds the SHA512SUMS download; the real one is under 10 KiB.
const maxChecksumFile = 1 << 20

// UpstreamSpec describes where the base image comes from: Debian's published "generic" image and the SHA512SUMS
// file in the same directory (PLAN.md section 7.5, step 2). It is the generic variant and not genericcloud on purpose:
// the genericcloud kernel (linux-image-cloud) is built without DRM, USB and most input drivers (CONFIG_DRM is not set
// in 6.12.107+deb13-cloud-amd64), so no X server can start on it and the USB tablet and keyboard do not exist.
type UpstreamSpec struct {
	// BaseURL is the directory, with a trailing slash.
	BaseURL string
	// Version is Debian's release number as it appears in file names.
	Version string
	// SignatureNote is recorded in the manifest. Debian publishes no signature for SHA512SUMS (checked 2026-10-12:
	// SHA512SUMS.sign, .asc and .sig all answer 404), so the image is trusted on HTTPS plus the checksum list.
	SignatureNote string
}

// DebianStable is Debian 13 "trixie", the current stable release.
var DebianStable = UpstreamSpec{
	BaseURL:       "https://cloud.debian.org/images/cloud/trixie/latest/",
	Version:       "13",
	SignatureNote: "Debian publishes no signature for SHA512SUMS; verified over HTTPS against the checksum list from cloud.debian.org",
}

// FileName is the image file for a guest architecture, e.g. debian-13-generic-amd64.qcow2.
func (u UpstreamSpec) FileName(arch string) (string, error) {
	var debArch string
	switch arch {
	case ArchX86_64:
		debArch = "amd64"
	case ArchAarch64:
		debArch = "arm64"
	default:
		return "", fmt.Errorf("no Debian cloud image for architecture %q", arch)
	}
	return "debian-" + u.Version + "-generic-" + debArch + ".qcow2", nil
}

// ImageURL is where the image is downloaded from.
func (u UpstreamSpec) ImageURL(arch string) (string, error) {
	name, err := u.FileName(arch)
	if err != nil {
		return "", err
	}
	return u.BaseURL + name, nil
}

// ChecksumsURL is the SHA512SUMS file of the directory.
func (u UpstreamSpec) ChecksumsURL() string {
	return strings.TrimSuffix(u.BaseURL, "/") + "/SHA512SUMS"
}

// Target is a resolved download: the URL and the SHA-512 the publisher lists for it.
type Target struct {
	URL      string
	FileName string
	SHA512   string
}

// Resolve fetches the checksum list and finds the entry for the architecture's image.
func (u UpstreamSpec) Resolve(ctx context.Context, d Downloader, arch string) (Target, error) {
	name, err := u.FileName(arch)
	if err != nil {
		return Target{}, err
	}
	data, err := d.FetchText(ctx, u.ChecksumsURL(), maxChecksumFile)
	if err != nil {
		return Target{}, fmt.Errorf("fetching the checksum list: %w", err)
	}
	sum, err := ParseSHA512SUMS(data, name)
	if err != nil {
		return Target{}, err
	}
	return Target{URL: u.BaseURL + name, FileName: name, SHA512: sum}, nil
}

// Record returns the manifest entry for a resolved target.
func (u UpstreamSpec) Record(t Target) Upstream {
	return Upstream{URL: t.URL, SHA512: t.SHA512, SignatureVerified: false, SignatureNote: u.SignatureNote}
}
