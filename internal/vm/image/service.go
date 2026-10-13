// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// BuildOptions are the flags of `controlsnack image build`.
type BuildOptions struct {
	Yes   bool // do not ask before downloading
	Force bool // rebuild even if an image with the same recipe exists
}

// Manager is what the `controlsnack image` commands use; all output goes to w.
type Manager interface {
	Build(ctx context.Context, w io.Writer, cfg config.Config, opts BuildOptions) error
	List(ctx context.Context, w io.Writer) error
	Remove(ctx context.Context, w io.Writer, id string) error
	Verify(ctx context.Context, w io.Writer, id string) error
	Import(ctx context.Context, w io.Writer, qcow2, manifest string) error
	// Path prints the path of an image; an empty id means the newest one.
	Path(ctx context.Context, w io.Writer, id string) error
}

// FileManager is the Manager over a Store on disk.
type FileManager struct {
	// Dir returns the image directory; the default is <user cache dir>/controlsnack/images.
	Dir func() (string, error)
	// Arch is the guest architecture for "the newest image" (default: the host's).
	Arch string

	// What `image build` needs from the vm package, which this package must not import: the accelerator resolver
	// and the boot check (see Builder.Verify). Without them Build refuses to run.
	Accel     func(ctx context.Context, cfg config.Config, inst qemu.Install) (string, error)
	BootCheck func(ctx context.Context, inst qemu.Install, accel, disk string, out io.Writer) error
	// Version is recorded in the manifests it builds.
	Version string
	// In answers the confirmation question (default: standard input).
	In io.Reader
}

// NewManager returns the manager for the current user's image store.
func NewManager() Manager { return &FileManager{} }

// DefaultDir is <user cache dir>/controlsnack/images.
func DefaultDir() (string, error) {
	d, err := platform.DefaultDirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Cache, "images"), nil
}

func (m *FileManager) store() (Store, error) {
	dir := m.Dir
	if dir == nil {
		dir = DefaultDir
	}
	d, err := dir()
	if err != nil {
		return Store{}, err
	}
	return Store{Dir: d}, nil
}

func (m *FileManager) arch() string {
	if m.Arch != "" {
		return m.Arch
	}
	return hostArch()
}

// imageMissing marks "no such image" with exit code 4.
func imageMissing(err error) error {
	if errors.Is(err, ErrNoImage) {
		return exitcode.New(exitcode.ImageMissing, err)
	}
	return err
}

// Build builds a new base image (PLAN.md section 7.5).
func (m *FileManager) Build(ctx context.Context, w io.Writer, cfg config.Config, opts BuildOptions) error {
	s, err := m.store()
	if err != nil {
		return err
	}
	b := &Builder{Store: s, Version: m.Version, In: m.In, Out: w, Accel: m.Accel, Verify: m.BootCheck}
	return b.Run(ctx, cfg, opts)
}

// List prints the images, newest first.
func (m *FileManager) List(_ context.Context, w io.Writer) error {
	s, err := m.store()
	if err != nil {
		return err
	}
	images, problems, err := s.List()
	if err != nil {
		return err
	}
	if len(images) == 0 {
		fmt.Fprintln(w, "No base images. Build one with: controlsnack image build")
	} else {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tARCH\tCREATED\tSIZE\tUPSTREAM\tRECIPE")
		for _, im := range images {
			mf := im.Manifest
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", mf.ID, mf.Arch, mf.Created.Local().Format("2006-01-02 15:04"), HumanSize(mf.Size),
				upstreamLabel(mf.Upstream), mf.RecipeSHA256[:12])
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	for _, p := range problems {
		fmt.Fprintln(w, "problem:", p)
	}
	return nil
}

func upstreamLabel(u Upstream) string {
	name := u.URL[strings.LastIndex(u.URL, "/")+1:]
	return fmt.Sprintf("%s (sha512 %s…)", name, u.SHA512[:8])
}

// HumanSize formats a byte count for people: 1.5 KiB, 325.7 MiB.
func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Remove deletes an image by id or id prefix.
func (m *FileManager) Remove(_ context.Context, w io.Writer, id string) error {
	s, err := m.store()
	if err != nil {
		return err
	}
	im, err := s.Remove(id)
	if err != nil {
		return imageMissing(err)
	}
	fmt.Fprintf(w, "removed image %s (%s)\n", im.Manifest.ID, HumanSize(im.Manifest.Size))
	return nil
}

// Verify hashes one image, or all of them when id is empty.
func (m *FileManager) Verify(ctx context.Context, w io.Writer, id string) error {
	s, err := m.store()
	if err != nil {
		return err
	}
	var images []Image
	if id == "" {
		images, _, err = s.List()
		if err != nil {
			return err
		}
		if len(images) == 0 {
			return exitcode.New(exitcode.ImageMissing, fmt.Errorf("%w: build one with 'controlsnack image build'", ErrNoImage))
		}
	} else {
		im, ferr := s.Find(id)
		if ferr != nil {
			return imageMissing(ferr)
		}
		images = []Image{im}
	}
	failed := 0
	for _, im := range images {
		res, err := s.Verify(ctx, im)
		if err != nil {
			return err
		}
		if res.OK {
			fmt.Fprintf(w, "ok      %s  %s\n", im.Manifest.ID, im.Path)
			continue
		}
		failed++
		fmt.Fprintf(w, "FAILED  %s  %s\n", im.Manifest.ID, im.Path)
		for _, p := range res.Problems {
			fmt.Fprintf(w, "        %s\n", p)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d images do not match their manifests: remove them with 'controlsnack image rm' and build again", failed, len(images))
	}
	return nil
}

// Import copies an image and its manifest into the store.
func (m *FileManager) Import(ctx context.Context, w io.Writer, qcow2, manifest string) error {
	s, err := m.store()
	if err != nil {
		return err
	}
	im, err := s.Import(ctx, qcow2, manifest)
	if err != nil {
		if errors.Is(err, ErrBadImage) {
			return exitcode.New(exitcode.Failure, err)
		}
		return err
	}
	fmt.Fprintf(w, "imported image %s (%s, %s)\n", im.Manifest.ID, im.Manifest.Arch, HumanSize(im.Manifest.Size))
	return nil
}

// Path prints the path of an image, one line, nothing else.
func (m *FileManager) Path(_ context.Context, w io.Writer, id string) error {
	s, err := m.store()
	if err != nil {
		return err
	}
	im, err := s.Resolve(id, m.arch())
	if err != nil {
		return imageMissing(err)
	}
	fmt.Fprintln(w, im.Path)
	return nil
}
