// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
)

// Store is what the `controlsnack vm` commands use to manage kept VMs (D-26); all output goes to w.
type Store interface {
	List(ctx context.Context, w io.Writer) error
	Remove(ctx context.Context, w io.Writer, name string) error
}

// FileStore is the Store over the kept VMs of the current user.
type FileStore struct {
	// Dir returns <state>/vms; the default is the platform's state directory.
	Dir func() (string, error)
	// Images returns the image store, to tell whether a VM's base image still exists (default: the user's).
	Images func() (image.Store, error)
}

// NewStore returns the store for the current user's kept VMs.
func NewStore() Store { return &FileStore{} }

// DefaultDir is <state dir>/vms.
func DefaultDir() (string, error) {
	d, err := platform.DefaultDirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.State, "vms"), nil
}

func (s *FileStore) library() (Library, error) {
	dir := s.Dir
	if dir == nil {
		dir = DefaultDir
	}
	d, err := dir()
	if err != nil {
		return Library{}, err
	}
	return Library{Dir: d}, nil
}

func (s *FileStore) images() (image.Store, error) {
	if s.Images != nil {
		return s.Images()
	}
	d, err := image.DefaultDir()
	return image.Store{Dir: d}, err
}

// List prints the kept VMs, most recently used first.
func (s *FileStore) List(_ context.Context, w io.Writer) error {
	lib, err := s.library()
	if err != nil {
		return err
	}
	vms, problems, err := lib.List()
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		fmt.Fprintln(w, "No kept VMs. Start one with: controlsnack run --keep-vm=NAME")
	} else {
		images, _ := s.images()
		known := map[string]bool{}
		if list, _, lerr := images.List(); lerr == nil {
			for _, im := range list {
				known[im.Manifest.ID] = true
			}
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tIMAGE\tCREATED\tLAST USED\tDISK\tSTATUS")
		for _, vm := range vms {
			status := "ok"
			switch {
			case !known[vm.BaseID]:
				status = "base image " + vm.BaseID + " is gone: this VM cannot start"
			case CheckBacking(vm.Disk, vm.BasePath) != nil:
				status = "disk does not match its base image"
			}
			size := int64(0)
			if fi, serr := os.Stat(vm.Disk); serr == nil {
				size = fi.Size()
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", vm.Name, vm.BaseID, vm.Created.Local().Format("2006-01-02 15:04"),
				vm.LastUsed.Local().Format("2006-01-02 15:04"), image.HumanSize(size), status)
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

// Remove deletes a kept VM.
func (s *FileStore) Remove(_ context.Context, w io.Writer, name string) error {
	lib, err := s.library()
	if err != nil {
		return err
	}
	vm, err := lib.Remove(name)
	if err != nil {
		if errors.Is(err, ErrNoVM) {
			return exitcode.New(exitcode.Failure, err)
		}
		return err
	}
	fmt.Fprintf(w, "removed kept VM %s\n", vm.Name)
	return nil
}
