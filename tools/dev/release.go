// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// releaseExtras are copied next to the binary when they exist.
var releaseExtras = []string{"LICENSE", "README.md", "TESTING.md", "THIRD_PARTY_LICENSES.md"}

// cmdRelease builds the five host binaries (CGO_ENABLED=0, -trimpath, version stamped, agents embedded) and
// packs them into archives with a SHA256SUMS file. Windows gets a .zip, everything else a .tar.gz.
func cmdRelease(args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	out := fs.String("o", filepath.Join("dist", "release"), "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The host embeds the guest agents (D-04), so they must be fresh before any host binary is built.
	if err := cmdAgent(nil); err != nil {
		return err
	}
	if err := os.RemoveAll(*out); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	version, _, _ := buildStamp()
	mtime := commitTime()

	var sums []string
	for _, t := range hostTargets {
		stage, err := os.MkdirTemp("", "controlsnack-release-")
		if err != nil {
			return err
		}
		bin := filepath.Join(stage, exeName("controlsnack", t))
		if err := goBuild(t, hostPkg, bin, true); err != nil {
			_ = os.RemoveAll(stage)
			return fmt.Errorf("host %s: %w", t, err)
		}
		files := []string{bin}
		for _, extra := range releaseExtras {
			if _, err := os.Stat(extra); err == nil {
				files = append(files, extra)
			}
		}
		base := fmt.Sprintf("controlsnack_%s_%s_%s", version, t.GOOS, t.GOARCH)
		var archive string
		if t.GOOS == "windows" {
			archive = filepath.Join(*out, base+".zip")
			err = writeZip(archive, base, files, mtime)
		} else {
			archive = filepath.Join(*out, base+".tar.gz")
			err = writeTarGz(archive, base, files, mtime)
		}
		_ = os.RemoveAll(stage)
		if err != nil {
			return fmt.Errorf("archive %s: %w", t, err)
		}
		data, err := os.ReadFile(archive) //nolint:gosec // archive was just written under the output directory
		if err != nil {
			return err
		}
		sums = append(sums, sumLine(data, filepath.Base(archive)))
		fmt.Println("wrote", archive)
	}
	sort.Strings(sums)
	return os.WriteFile(filepath.Join(*out, "SHA256SUMS"), []byte(strings.Join(sums, "\n")+"\n"), 0o640)
}

func isExecutable(path string) bool {
	base := filepath.Base(path)
	return base == "controlsnack" || base == "controlsnack.exe"
}

func writeZip(archive, dir string, files []string, mtime time.Time) (err error) {
	f, err := os.Create(archive) //nolint:gosec // path is under the output directory
	if err != nil {
		return err
	}
	defer func() { err = errJoinClose(err, f) }()
	zw := zip.NewWriter(f)
	for _, path := range files {
		if err := addZipFile(zw, dir, path, mtime); err != nil {
			return err
		}
	}
	return zw.Close()
}

func addZipFile(zw *zip.Writer, dir, path string, mtime time.Time) error {
	src, err := os.Open(path) //nolint:gosec // paths come from our own release staging
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	hdr := &zip.FileHeader{Name: dir + "/" + filepath.Base(path), Method: zip.Deflate, Modified: mtime}
	hdr.SetMode(0o644)
	if isExecutable(path) {
		hdr.SetMode(0o755)
	}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, src)
	return err
}

func writeTarGz(archive, dir string, files []string, mtime time.Time) (err error) {
	f, err := os.Create(archive) //nolint:gosec // path is under the output directory
	if err != nil {
		return err
	}
	defer func() { err = errJoinClose(err, f) }()
	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	for _, path := range files {
		if err := addTarFile(tw, dir, path, mtime); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addTarFile(tw *tar.Writer, dir, path string, mtime time.Time) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := int64(0o644)
	if isExecutable(path) {
		mode = 0o755
	}
	hdr := &tar.Header{Name: dir + "/" + filepath.Base(path), Mode: mode, Size: info.Size(), ModTime: mtime, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	src, err := os.Open(path) //nolint:gosec // paths come from our own release staging
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	_, err = io.Copy(tw, src)
	return err
}

// errJoinClose closes f and keeps the first error.
func errJoinClose(err error, f *os.File) error {
	if cerr := f.Close(); err == nil {
		return cerr
	}
	return err
}
