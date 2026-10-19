// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack/guest"
	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/platform"
	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
	"github.com/PacifAIst/controlsnack/internal/vm/seed"
)

// MinFreeSpace is what a build needs on the disk that holds the image store (PLAN.md section 7.5, step 1): the
// download, the overlay it grows, the compressed result and room to work.
const MinFreeSpace = 8 << 30

const totalSteps = 7

// failedImageName is where a sealed image that failed its boot check is kept, so that someone can boot it and look.
// The next build removes it.
const failedImageName = "last-build-failed.qcow2"

// The boot check leaves these in the build directory when the guest does not come up (a screendump of what it shows
// and its whole console); a failed build keeps them beside the disk under the names in failedEvidence.
const (
	BootCheckScreenshot = "boot-check.png"
	BootCheckConsole    = "boot-check-serial.log"
)

var failedEvidence = map[string]string{BootCheckScreenshot: "last-build-failed.png", BootCheckConsole: "last-build-failed-console.log"}

// Builder carries out `controlsnack image build` (PLAN.md section 7.5). Everything that touches the machine is a
// field with a real default or, for what needs the `vm` package (which this package must not import), a function
// the caller provides; tests substitute fakes for all of them.
type Builder struct {
	Store    Store
	Down     Downloader
	Upstream UpstreamSpec
	Version  string // controlsnack_version recorded in the manifest
	In       io.Reader
	Out      io.Writer

	// Discover finds QEMU (default: qemu.Discover on this machine, honoring --qemu).
	Discover func(ctx context.Context, qemuDir string) (qemu.Install, error)
	// Accel resolves the accelerator the same way `run` does. Required.
	Accel func(ctx context.Context, cfg config.Config, inst qemu.Install) (string, error)
	// Provision boots the provisioning VM (default RunProvision).
	Provision func(ctx context.Context, r ProvisionRun) error
	// Overlay and Convert are the qemu-img steps (defaults CreateOverlay, ConvertCompressed).
	Overlay func(ctx context.Context, img, backing, dest, size string) error
	Convert func(ctx context.Context, img, src, dest string) error
	// Verify boots the sealed disk with the runtime profile and waits for the agent port to open. Required.
	Verify func(ctx context.Context, inst qemu.Install, accel, disk string, out io.Writer) error

	FreeSpace func(dir string) (uint64, error)
	Locker    platform.ProcessInspector
	MinFree   uint64
	Now       func() time.Time
	// Timeouts of the provisioning boot (defaults in RunProvision) and the poll of its console.
	ProvisionTimeout, ProvisionIdle, ProvisionPoll time.Duration
}

func (b *Builder) defaults() {
	if b.Discover == nil {
		b.Discover = func(ctx context.Context, dir string) (qemu.Install, error) {
			return qemu.Discover(ctx, qemu.OSHost{}, qemu.Options{Dir: dir})
		}
	}
	if b.Provision == nil {
		b.Provision = RunProvision
	}
	if b.Overlay == nil {
		b.Overlay = CreateOverlay
	}
	if b.Convert == nil {
		b.Convert = ConvertCompressed
	}
	if b.FreeSpace == nil {
		b.FreeSpace = platform.FreeSpace
	}
	if b.Locker == nil {
		b.Locker = platform.OSProcesses{}
	}
	if b.MinFree == 0 {
		b.MinFree = MinFreeSpace
	}
	if b.Now == nil {
		b.Now = time.Now
	}
	if b.In == nil {
		b.In = os.Stdin
	}
	if b.Out == nil {
		b.Out = io.Discard
	}
	if b.Upstream.BaseURL == "" {
		b.Upstream = DebianStable
	}
}

type buildRun struct {
	b     *Builder
	n     int
	began time.Time
}

func (r *buildRun) step(format string, args ...any) {
	r.n++
	fmt.Fprintf(r.b.Out, "[%d/%d] %s\n", r.n, totalSteps, fmt.Sprintf(format, args...))
}

func (r *buildRun) say(format string, args ...any) {
	fmt.Fprintf(r.b.Out, "      %s\n", fmt.Sprintf(format, args...))
}

// Run builds a base image. It returns nil without doing anything if an image made from the same recipe and the
// same upstream already exists and opts.Force is not set.
func (b *Builder) Run(ctx context.Context, cfg config.Config, opts BuildOptions) error {
	b.defaults()
	if b.Accel == nil || b.Verify == nil {
		return errors.New("image build: the accelerator resolver and the boot verifier are required")
	}
	run := &buildRun{b: b, began: b.Now()}

	// 1. Preflight.
	run.step("Checking QEMU, the accelerator and the disk")
	inst, err := b.Discover(ctx, cfg.VM.QemuDir)
	if err != nil {
		return exitcode.New(exitcode.Environment, fmt.Errorf("%w\nrun 'controlsnack doctor' to see what is missing", err))
	}
	accel, err := b.Accel(ctx, cfg, inst)
	if err != nil {
		return err
	}
	run.say("QEMU %s at %s; accelerator: %s", inst.Version, inst.Dir, accel)
	if accel == qemu.AccelTCG {
		run.say("WARNING: no hardware acceleration, so the build uses software emulation and can take 30 minutes to two hours")
		run.say("(see 'controlsnack doctor' for why, and what would make it faster)")
	}
	if err := os.MkdirAll(b.Store.Dir, 0o700); err != nil {
		return err
	}
	free, err := b.FreeSpace(b.Store.Dir)
	if err != nil {
		return err
	}
	if free < b.MinFree {
		return fmt.Errorf("the image store at %s has %s free; a build needs %s", b.Store.Dir, HumanSize(int64(free)), HumanSize(int64(b.MinFree))) //nolint:gosec // sizes fit in int64
	}
	release, err := acquireBuildLock(b.Store.Dir, b.Locker)
	if err != nil {
		return err
	}
	defer release()

	// 2. Find out what upstream offers, and whether there is anything to do.
	run.step("Looking up the Debian cloud image")
	target, err := b.Upstream.Resolve(ctx, b.Down, inst.Arch)
	if err != nil {
		return err
	}
	recipe := guest.RecipeHash()
	run.say("%s (sha512 %s…), recipe %s", target.FileName, target.SHA512[:12], recipe[:12])
	if !opts.Force {
		if images, _, lerr := b.Store.List(); lerr == nil {
			for _, im := range images {
				if im.Manifest.Arch == inst.Arch && im.Manifest.RecipeSHA256 == recipe && im.Manifest.Upstream.SHA512 == target.SHA512 {
					fmt.Fprintf(b.Out, "Image %s is up to date: same recipe, same Debian image. Use --force to build again.\n", im.Manifest.ID)
					return nil
				}
			}
		}
	}
	if !opts.Yes {
		if err := b.confirm(inst.Arch, accel); err != nil {
			return err
		}
	}

	downloads := filepath.Join(b.Store.Dir, "downloads")
	if err := os.MkdirAll(downloads, 0o700); err != nil {
		return err
	}
	upstream := filepath.Join(downloads, target.FileName)
	run.say("downloading %s", target.URL)
	d := b.Down
	var lastShown time.Time
	d.Progress = func(done, total int64) {
		if now := time.Now(); now.Sub(lastShown) > 5*time.Second || (total > 0 && done == total) {
			lastShown = now
			if total > 0 {
				run.say("  %s of %s (%d%%)", HumanSize(done), HumanSize(total), done*100/total)
			} else {
				run.say("  %s", HumanSize(done))
			}
		}
	}
	res, err := d.Download(ctx, target.URL, upstream, target.SHA512)
	if err != nil {
		return fmt.Errorf("downloading the Debian image: %w", err)
	}
	switch {
	case res.Cached:
		run.say("already downloaded and verified")
	case res.Resumed:
		run.say("continued a partial download; checksum verified")
	default:
		run.say("checksum verified")
	}

	// 3. The build directory: provisioning overlay and seed.
	run.step("Preparing the provisioning disk and the cloud-init seed")
	_ = os.Remove(filepath.Join(b.Store.Dir, failedImageName)) // what a previous failed build kept
	for _, name := range failedEvidence {
		_ = os.Remove(filepath.Join(b.Store.Dir, name))
	}
	work, err := os.MkdirTemp(b.Store.Dir, "build-")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(work)
		}
	}()
	overlay := filepath.Join(work, "work.qcow2")
	absUpstream, err := filepath.Abs(upstream)
	if err != nil {
		return err
	}
	if err := b.Overlay(ctx, inst.Img, absUpstream, overlay, OverlaySize); err != nil {
		return err
	}
	user, err := guest.UserData()
	if err != nil {
		return err
	}
	seedISO := filepath.Join(work, "seed.iso")
	if err := seed.Build(seedISO, seed.Files{UserData: user, MetaData: guest.MetaData(), NetworkConfig: guest.NetworkConfig()}); err != nil {
		return err
	}
	run.say("overlay %s, seed %s (%d packages requested)", OverlaySize, HumanSize(fileSize(seedISO)), len(guest.Packages))

	// 4. Provisioning boot: the one profile with a network device.
	run.step("Provisioning inside a temporary VM (installing the desktop; this is the long part)")
	cpus, mem := cfg.VM.CPUs, cfg.VM.MemoryMiB
	if cpus < 2 {
		cpus = 2
	}
	if mem < 2048 {
		mem = 2048
	}
	began := b.Now()
	err = b.Provision(ctx, ProvisionRun{
		Install: inst, Accel: accel, CPUs: cpus, MemoryMiB: mem, Disk: overlay, Seed: seedISO, Dir: work,
		Timeout: b.ProvisionTimeout, Idle: b.ProvisionIdle, Poll: b.ProvisionPoll,
		Progress: func(text string) { run.say("guest: %s", text) },
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		saved := filepath.Join(b.Store.Dir, "last-build-failure.log")
		data, rerr := os.ReadFile(filepath.Join(work, "serial.log")) //nolint:gosec // inside our own build directory
		if rerr == nil && os.WriteFile(saved, data, 0o600) == nil {  //nolint:gosec // saved is inside the image store this process owns
			err = fmt.Errorf("%w\nthe whole console log is in %s", err, saved)
		}
		return fmt.Errorf("provisioning failed: %w", err)
	}
	run.say("provisioning took %s", b.Now().Sub(began).Round(time.Second))

	// 5. Seal: flatten and compress.
	run.step("Flattening and compressing the disk")
	sealed := filepath.Join(work, "sealed.qcow2")
	if err := b.Convert(ctx, inst.Img, overlay, sealed); err != nil {
		return err
	}
	_ = os.Remove(overlay)
	run.say("%s", HumanSize(fileSize(sealed)))

	// 6. Verify: boot the result the way a session will, with no network, and wait for the agent port.
	run.step("Booting the result with the runtime profile (no network) to check that the desktop and the bootstrap start")
	if err := b.Verify(ctx, inst, accel, sealed, b.Out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Keep the sealed disk: what is wrong with it can only be seen by booting it.
		kept := filepath.Join(b.Store.Dir, failedImageName)
		if merr := moveFile(sealed, kept); merr == nil {
			err = fmt.Errorf("%w\nthe image was kept for inspection as %s; the next build removes it", err, kept)
		}
		for from, to := range failedEvidence {
			if data, rerr := os.ReadFile(filepath.Join(work, from)); rerr == nil { //nolint:gosec // our build directory
				dest := filepath.Join(b.Store.Dir, to)
				if os.WriteFile(dest, data, 0o600) == nil { //nolint:gosec // inside the image store this process owns
					err = fmt.Errorf("%w\n  also kept: %s", err, dest)
				}
			}
		}
		return fmt.Errorf("the finished image does not boot to a working desktop: %w", err)
	}

	// 7. Commit.
	run.step("Storing the image")
	img, err := b.Store.Commit(ctx, sealed, Manifest{
		Created: b.Now().UTC(), Version: b.Version, RecipeSHA256: recipe, Upstream: b.Upstream.Record(target),
		Arch: inst.Arch, Packages: guest.Packages,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(b.Out, "Built image %s (%s, %s) in %s\n  %s\n", img.Manifest.ID, img.Manifest.Arch, HumanSize(img.Manifest.Size),
		b.Now().Sub(run.began).Round(time.Second), img.Path)
	// Older downloads are not needed any more.
	if entries, rerr := os.ReadDir(downloads); rerr == nil {
		for _, e := range entries {
			if e.Name() != target.FileName {
				_ = os.Remove(filepath.Join(downloads, e.Name()))
			}
		}
	}
	return nil
}

func (b *Builder) confirm(arch, accel string) error {
	fmt.Fprintf(b.Out, "\nThis will download the Debian 13 cloud image (about 430 MB), start a temporary VM with network access and\n"+
		"install about 400 packages in it (about 1.5 GB more), then seal the result as the base image (about 2 GB, %s).\n", arch)
	if accel == qemu.AccelTCG {
		fmt.Fprintln(b.Out, "Without hardware acceleration this can take 30 minutes to two hours.")
	} else {
		fmt.Fprintf(b.Out, "With %s this usually takes 10 to 20 minutes.\n", accel)
	}
	fmt.Fprint(b.Out, "Continue? [y/N] ")
	line, err := bufio.NewReader(b.In).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("no answer; run with --yes to build without being asked")
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errors.New("canceled")
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
