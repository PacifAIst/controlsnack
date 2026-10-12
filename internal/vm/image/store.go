// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// ErrNoImage means there is no base image to use; `controlsnack image build` makes one.
	ErrNoImage = errors.New("no base image")
	// ErrAmbiguous means an id prefix matches more than one image.
	ErrAmbiguous = errors.New("image id is ambiguous")
	// ErrBadImage means a file is not the image its manifest says it is.
	ErrBadImage = errors.New("image does not match its manifest")
)

var reManifestName = regexp.MustCompile(`^base-([0-9a-f]{12})\.json$`)

// qcow2Magic starts every qcow2 file.
var qcow2Magic = []byte{'Q', 'F', 'I', 0xfb}

// Image is a sealed base image in the store.
type Image struct {
	Manifest     Manifest
	Path         string // the qcow2 file
	ManifestPath string
	// Unmanaged is set for an image given by path that is not in the store (no manifest of ours).
	Unmanaged bool
}

// Store is the directory of base images, <cache>/images. Images are flat files: base-<id>.qcow2 (read-only) and
// base-<id>.json (the manifest).
type Store struct {
	Dir string
}

// DiskName and ManifestName are the file names of an image id.
func DiskName(id string) string     { return "base-" + id + ".qcow2" }
func ManifestName(id string) string { return "base-" + id + ".json" }

func (s Store) paths(id string) (disk, manifest string) {
	return filepath.Join(s.Dir, DiskName(id)), filepath.Join(s.Dir, ManifestName(id))
}

// List returns the images, newest first, and a description of every entry that could not be used.
func (s Store) List() (images []Image, problems []string, err error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		m := reManifestName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		disk, manifest := s.paths(m[1])
		man, merr := ReadManifest(manifest)
		switch {
		case merr != nil:
			problems = append(problems, fmt.Sprintf("%s: %v", e.Name(), merr))
		case man.ID != m[1]:
			problems = append(problems, fmt.Sprintf("%s: the manifest is for image %s", e.Name(), man.ID))
		default:
			if _, serr := os.Stat(disk); serr != nil {
				problems = append(problems, fmt.Sprintf("%s: the image file is missing", DiskName(m[1])))
				continue
			}
			images = append(images, Image{Manifest: man, Path: disk, ManifestPath: manifest})
		}
	}
	sort.Slice(images, func(i, j int) bool {
		if !images[i].Manifest.Created.Equal(images[j].Manifest.Created) {
			return images[i].Manifest.Created.After(images[j].Manifest.Created)
		}
		return images[i].Manifest.ID < images[j].Manifest.ID
	})
	return images, problems, nil
}

// Find returns the image whose id starts with prefix (at least 4 hex digits).
func (s Store) Find(prefix string) (Image, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if len(prefix) < 4 || len(prefix) > 12 || !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(prefix) {
		return Image{}, fmt.Errorf("%q is not an image id (4 to 12 hex digits)", prefix)
	}
	images, _, err := s.List()
	if err != nil {
		return Image{}, err
	}
	var found []Image
	for _, im := range images {
		if strings.HasPrefix(im.Manifest.ID, prefix) {
			found = append(found, im)
		}
	}
	switch len(found) {
	case 0:
		return Image{}, fmt.Errorf("%w with id %s", ErrNoImage, prefix)
	case 1:
		return found[0], nil
	}
	return Image{}, fmt.Errorf("%w: %q matches %d images", ErrAmbiguous, prefix, len(found))
}

// Newest returns the newest image for the architecture.
func (s Store) Newest(arch string) (Image, error) {
	images, _, err := s.List()
	if err != nil {
		return Image{}, err
	}
	for _, im := range images {
		if im.Manifest.Arch == arch {
			return im, nil
		}
	}
	return Image{}, fmt.Errorf("%w for %s: build one with 'controlsnack image build'", ErrNoImage, arch)
}

// Resolve turns the vm.image setting into an image: empty means the newest one for arch, a path to a qcow2 file is
// used as it is (with its sidecar manifest if it has one), anything else is an id or id prefix.
func (s Store) Resolve(spec, arch string) (Image, error) {
	var im Image
	var err error
	switch {
	case spec == "":
		im, err = s.Newest(arch)
	case strings.ContainsAny(spec, `/\`) || strings.HasSuffix(strings.ToLower(spec), ".qcow2"):
		im, err = externalImage(spec)
	default:
		im, err = s.Find(spec)
	}
	if err != nil {
		return Image{}, err
	}
	if !im.Unmanaged && im.Manifest.Arch != arch {
		return Image{}, fmt.Errorf("image %s is for %s but this host runs %s guests", im.Manifest.ID, im.Manifest.Arch, arch)
	}
	return im, nil
}

func externalImage(path string) (Image, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Image{}, err
	}
	if err := checkQCOW2(abs); err != nil {
		return Image{}, err
	}
	im := Image{Path: abs, Unmanaged: true}
	if man, err := ReadManifest(strings.TrimSuffix(abs, filepath.Ext(abs)) + ".json"); err == nil {
		im.Manifest, im.ManifestPath, im.Unmanaged = man, strings.TrimSuffix(abs, filepath.Ext(abs))+".json", false
	}
	return im, nil
}

// checkQCOW2 requires a regular file that starts with the qcow2 magic.
func checkQCOW2(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path) //nolint:gosec // a path the user named
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, len(qcow2Magic))
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, qcow2Magic) {
		return fmt.Errorf("%s is not a qcow2 image", path)
	}
	return nil
}

// QuickCheck is the check `run` makes on every start: the file is there, is a regular file and has the size the
// manifest records. It does not hash the file (that is `image verify`); an unmanaged image has nothing to compare.
func (s Store) QuickCheck(im Image) error {
	fi, err := os.Stat(im.Path)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoImage, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrBadImage, im.Path)
	}
	if !im.Unmanaged && fi.Size() != im.Manifest.Size {
		return fmt.Errorf("%w: %s is %d bytes, the manifest says %d", ErrBadImage, im.Path, fi.Size(), im.Manifest.Size)
	}
	return nil
}

// VerifyResult is the outcome of hashing an image.
type VerifyResult struct {
	Image    Image
	OK       bool
	Problems []string
}

// Verify hashes the image file and compares it with the manifest.
func (s Store) Verify(ctx context.Context, im Image) (VerifyResult, error) {
	res := VerifyResult{Image: im}
	if im.Unmanaged {
		return res, errors.New("an image outside the store has no manifest to verify against")
	}
	fi, err := os.Stat(im.Path)
	if err != nil {
		res.Problems = append(res.Problems, err.Error())
		return res, nil
	}
	if fi.Size() != im.Manifest.Size {
		res.Problems = append(res.Problems, fmt.Sprintf("size is %d bytes, the manifest says %d", fi.Size(), im.Manifest.Size))
	}
	sum, err := hashFile(ctx, im.Path)
	if err != nil {
		return res, err
	}
	if sum != im.Manifest.SHA256 {
		res.Problems = append(res.Problems, fmt.Sprintf("sha256 is %s, the manifest says %s", sum, im.Manifest.SHA256))
	}
	res.OK = len(res.Problems) == 0
	return res, nil
}

func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a store path
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := copyCtx(ctx, h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyCtx copies in 1 MiB steps and stops between them when ctx ends.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		r, rerr := src.Read(buf)
		if r > 0 {
			w, werr := dst.Write(buf[:r])
			n += int64(w)
			if werr != nil {
				return n, werr
			}
		}
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			return n, rerr
		}
	}
}

// Remove deletes the image and its manifest.
func (s Store) Remove(id string) (Image, error) {
	im, err := s.Find(id)
	if err != nil {
		return Image{}, err
	}
	// The disk is read-only (and on Windows that attribute blocks deletion).
	_ = os.Chmod(im.Path, 0o600)
	if err := os.Remove(im.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return im, err
	}
	if err := os.Remove(im.ManifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return im, err
	}
	return im, nil
}

// Import copies a qcow2 file and its manifest into the store after checking that they belong together: the
// manifest is valid, the file is a qcow2 image of the recorded size and its SHA-256 is the recorded one.
func (s Store) Import(ctx context.Context, qcow2, manifestPath string) (Image, error) {
	man, err := ReadManifest(manifestPath)
	if err != nil {
		return Image{}, err
	}
	if err := checkQCOW2(qcow2); err != nil {
		return Image{}, err
	}
	if fi, _ := os.Stat(qcow2); fi.Size() != man.Size {
		return Image{}, fmt.Errorf("%w: %s is %d bytes, the manifest says %d", ErrBadImage, qcow2, fi.Size(), man.Size)
	}
	disk, mpath := s.paths(man.ID)
	if _, err := os.Stat(disk); err == nil {
		return Image{}, fmt.Errorf("image %s is already in the store", man.ID)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Image{}, err
	}
	tmp, err := os.CreateTemp(s.Dir, ".import-*.tmp")
	if err != nil {
		return Image{}, err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	src, err := os.Open(qcow2)  //nolint:gosec // a path the user named
	if err != nil {
		_ = tmp.Close()
		return Image{}, err
	}
	defer src.Close()
	h := sha256.New()
	if _, err := copyCtx(ctx, io.MultiWriter(tmp, h), src); err != nil {
		_ = tmp.Close()
		return Image{}, err
	}
	if err := tmp.Close(); err != nil {
		return Image{}, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != man.SHA256 {
		return Image{}, fmt.Errorf("%w: sha256 is %s, the manifest says %s", ErrBadImage, got, man.SHA256)
	}
	return s.place(tmp.Name(), man, disk, mpath)
}

// Commit seals a freshly built image into the store: it hashes the file, fills in the id, size and checksum of the
// manifest, moves the file into place read-only and writes the manifest.
func (s Store) Commit(ctx context.Context, built string, man Manifest) (Image, error) {
	if err := checkQCOW2(built); err != nil {
		return Image{}, err
	}
	sum, err := hashFile(ctx, built)
	if err != nil {
		return Image{}, err
	}
	fi, err := os.Stat(built)
	if err != nil {
		return Image{}, err
	}
	man.SHA256, man.ID, man.Size = sum, IDOf(sum), fi.Size()
	if err := man.Validate(); err != nil {
		return Image{}, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Image{}, err
	}
	disk, mpath := s.paths(man.ID)
	if _, err := os.Stat(disk); err == nil {
		return Image{}, fmt.Errorf("image %s is already in the store", man.ID)
	}
	return s.place(built, man, disk, mpath)
}

// place moves a verified file into the store, writes the manifest and makes the disk read-only.
func (s Store) place(src string, man Manifest, disk, mpath string) (Image, error) {
	data, err := man.Encode()
	if err != nil {
		return Image{}, err
	}
	if err := moveFile(src, disk); err != nil {
		return Image{}, err
	}
	// Read-only and for this user only: the image is never written, and QEMU runs as this user.
	if err := os.Chmod(disk, 0o400); err != nil {
		return Image{}, err
	}
	tmp := mpath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil { //nolint:gosec // a manifest is public information
		return Image{}, err
	}
	if err := os.Rename(tmp, mpath); err != nil {
		return Image{}, err
	}
	return Image{Manifest: man, Path: disk, ManifestPath: mpath}, nil
}

// moveFile renames, or copies and removes when the two are on different devices.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src) //nolint:gosec // a path this package produced
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the store
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Remove(src)
}
