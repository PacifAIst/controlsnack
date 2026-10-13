// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/guest"
	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
	"github.com/PacifAIst/controlsnack-local/internal/vm/seed"
)

// buildRig is a Builder whose environment is entirely fake: a Debian mirror served from memory, no QEMU.
type buildRig struct {
	t    *testing.T
	b    *Builder
	out  *bytes.Buffer
	srv  *httptest.Server
	disk []byte // the "Debian image" the mirror serves
	sums func() string
	hits *int

	provisioned []ProvisionRun
	overlayArgs [][4]string
	convertArgs [][3]string
	verified    []string
	builds      int
}

func newBuildRig(t *testing.T) *buildRig {
	t.Helper()
	r := &buildRig{t: t, out: &bytes.Buffer{}}
	r.disk = append(append([]byte(nil), qcow2Magic...), bytes.Repeat([]byte("debian"), 20_000)...)
	hits := 0
	r.hits = &hits
	r.sums = func() string {
		s := sha512.Sum512(r.disk)
		return hex.EncodeToString(s[:]) + "  debian-13-generic-amd64.qcow2\n"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/dir/SHA512SUMS", func(w http.ResponseWriter, req *http.Request) { _, _ = io.WriteString(w, r.sums()) })
	mux.HandleFunc("/dir/debian-13-generic-amd64.qcow2", func(w http.ResponseWriter, req *http.Request) {
		hits++
		http.ServeContent(w, req, "x", time.Time{}, bytes.NewReader(r.disk))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	r.srv = srv
	client := srv.Client()
	client.CheckRedirect = checkRedirect

	r.b = &Builder{
		Store:    Store{Dir: filepath.Join(t.TempDir(), "images")},
		Down:     Downloader{Client: client},
		Upstream: UpstreamSpec{BaseURL: srv.URL + "/dir/", Version: "13", SignatureNote: "none published"},
		Version:  "v9.9.9",
		In:       strings.NewReader("y\n"),
		Out:      r.out,
		Discover: func(context.Context, string) (qemu.Install, error) {
			return qemu.Install{Arch: qemu.ArchX86_64, Dir: "/usr/bin", System: "/usr/bin/qemu-system-x86_64", Img: "/usr/bin/qemu-img",
				Version: qemu.Version{Major: 9}}, nil
		},
		Accel: func(context.Context, config.Config, qemu.Install) (string, error) { return "kvm", nil },
		Provision: func(ctx context.Context, p ProvisionRun) error {
			r.provisioned = append(r.provisioned, p)
			// The seed must be a real NoCloud ISO carrying the recipe.
			label, files, err := seed.Read(p.Seed)
			if err != nil || strings.TrimSpace(label) != "cidata" || !bytes.Contains(files.UserData, []byte("#cloud-config")) {
				return errors.Join(errors.New("fake provisioner got a bad seed"), err, fmt.Errorf("label %q", label))
			}
			p.Progress("start")
			p.Progress("installing packages")
			return nil
		},
		Overlay: func(ctx context.Context, img, backing, dest, size string) error {
			r.overlayArgs = append(r.overlayArgs, [4]string{img, backing, dest, size})
			return os.WriteFile(dest, append(append([]byte(nil), qcow2Magic...), []byte("overlay")...), 0o600)
		},
		Convert: func(ctx context.Context, img, src, dest string) error {
			r.convertArgs = append(r.convertArgs, [3]string{img, src, dest})
			r.builds++
			// Every build yields different bytes, like the real thing (timestamps), so ids differ.
			return os.WriteFile(dest, append(append([]byte(nil), qcow2Magic...), []byte(fmt.Sprintf("sealed image number %d-%d", r.builds, serial.Add(1)))...), 0o600)
		},
		Verify: func(ctx context.Context, inst qemu.Install, accel, disk string, out io.Writer) error {
			r.verified = append(r.verified, disk+"|"+accel)
			if _, err := os.Stat(disk); err != nil {
				return err
			}
			// The disk is still in the build directory: nothing is stored before the boot check passed.
			if filepath.Dir(disk) == r.b.Store.Dir {
				return fmt.Errorf("the image was committed before it was verified: %s", disk)
			}
			return nil
		},
		FreeSpace: func(string) (uint64, error) { return 100 << 30, nil },
	}
	return r
}

func (r *buildRig) run(opts BuildOptions) error {
	return r.b.Run(context.Background(), config.Defaults(), opts)
}

func TestBuildHappyPath(t *testing.T) {
	r := newBuildRig(t)
	if err := r.run(BuildOptions{Yes: true}); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	out := r.out.String()
	for i := 1; i <= totalSteps; i++ {
		if !strings.Contains(out, fmt.Sprintf("[%d/%d]", i, totalSteps)) {
			t.Errorf("step %d is not announced:\n%s", i, out)
		}
	}
	for _, want := range []string{"accelerator: kvm", "checksum verified", "guest: start", "guest: installing packages", "Built image "} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "WARNING: no hardware acceleration") {
		t.Error("no warning about software emulation with kvm")
	}

	images, problems, err := r.b.Store.List()
	if err != nil || len(problems) != 0 || len(images) != 1 {
		t.Fatalf("%v %v %v", images, problems, err)
	}
	m := images[0].Manifest
	if m.Arch != "x86_64" || m.Version != "v9.9.9" || m.RecipeSHA256 != guest.RecipeHash() || len(m.Packages) != len(guest.Packages) ||
		m.Upstream.SHA512 != strings.Fields(r.sums())[0] || m.Upstream.SignatureVerified || m.Upstream.SignatureNote != "none published" ||
		!strings.HasSuffix(m.Upstream.URL, "/dir/debian-13-generic-amd64.qcow2") || time.Since(m.Created) > time.Minute {
		t.Errorf("manifest %+v", m)
	}
	if res, err := r.b.Store.Verify(context.Background(), images[0]); err != nil || !res.OK {
		t.Errorf("the stored image matches its manifest: %+v %v", res, err)
	}
	if !strings.Contains(out, m.ID) {
		t.Error("the summary names the image")
	}

	// The environment received what the plan says.
	if len(r.provisioned) != 1 {
		t.Fatalf("%d provisioning boots", len(r.provisioned))
	}
	p := r.provisioned[0]
	if p.Accel != "kvm" || p.CPUs < 2 || p.MemoryMiB < 2048 || p.Install.Arch != "x86_64" || filepath.Base(p.Disk) != "work.qcow2" || filepath.Base(p.Seed) != "seed.iso" {
		t.Errorf("%+v", p)
	}
	ov := r.overlayArgs[0]
	if ov[0] != "/usr/bin/qemu-img" || !filepath.IsAbs(ov[1]) || filepath.Base(ov[1]) != "debian-13-generic-amd64.qcow2" || ov[3] != "12G" {
		t.Errorf("overlay %v: the backing file is the verified download, by absolute path, 12G", ov)
	}
	cv := r.convertArgs[0]
	if cv[1] != p.Disk || filepath.Base(cv[2]) != "sealed.qcow2" {
		t.Errorf("convert %v", cv)
	}
	if len(r.verified) != 1 || !strings.HasSuffix(r.verified[0], "sealed.qcow2|kvm") {
		t.Errorf("the sealed disk is what gets booted: %v", r.verified)
	}

	// Housekeeping: no build directory, no lock, the download kept for the next build.
	entries, _ := os.ReadDir(r.b.Store.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "build-") || e.Name() == ".build.lock" {
			t.Errorf("%s was left behind", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(r.b.Store.Dir, "downloads", "debian-13-generic-amd64.qcow2")); err != nil {
		t.Errorf("the verified download stays: %v", err)
	}
}

func TestBuildWarnsAboutSoftwareEmulation(t *testing.T) {
	r := newBuildRig(t)
	r.b.Accel = func(context.Context, config.Config, qemu.Install) (string, error) { return "tcg", nil }
	if err := r.run(BuildOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.out.String(), "WARNING: no hardware acceleration") || !strings.Contains(r.out.String(), "30 minutes to two hours") {
		t.Errorf("%s", r.out)
	}
	if r.provisioned[0].Accel != "tcg" {
		t.Error("the provisioning boot uses the resolved accelerator")
	}
}

func TestBuildIsSkippedWhenNothingChanged(t *testing.T) {
	r := newBuildRig(t)
	if err := r.run(BuildOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	r.out.Reset()
	if err := r.run(BuildOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.out.String(), "is up to date") || len(r.provisioned) != 1 {
		t.Errorf("same recipe and same upstream: nothing to do.\n%s", r.out)
	}
	// --force builds anyway.
	r.out.Reset()
	if err := r.run(BuildOptions{Yes: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	if images, _, _ := r.b.Store.List(); len(images) != 2 || len(r.provisioned) != 2 {
		t.Errorf("%d images, %d provisioning boots", len(images), len(r.provisioned))
	}
	// A new Debian image (another checksum) is a reason to build.
	r.disk = append(r.disk, []byte("newer")...)
	if err := os.Remove(filepath.Join(r.b.Store.Dir, "downloads", "debian-13-generic-amd64.qcow2")); err != nil {
		t.Fatal(err)
	}
	if err := r.run(BuildOptions{Yes: true}); err != nil || len(r.provisioned) != 3 {
		t.Errorf("a new upstream image triggers a build: %v (%d boots)", err, len(r.provisioned))
	}
}

func TestBuildDownloadIsReused(t *testing.T) {
	r := newBuildRig(t)
	if err := r.run(BuildOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if *r.hits != 1 {
		t.Fatalf("%d downloads", *r.hits)
	}
	if err := r.run(BuildOptions{Yes: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	if *r.hits != 1 || !strings.Contains(r.out.String(), "already downloaded and verified") {
		t.Errorf("a rebuild uses the verified download: %d downloads", *r.hits)
	}
}

func TestBuildAsksBeforeDownloading(t *testing.T) {
	for answer, wantErr := range map[string]string{"n\n": "canceled", "\n": "canceled", "maybe\n": "canceled", "": "no answer"} {
		r := newBuildRig(t)
		r.b.In = strings.NewReader(answer)
		err := r.run(BuildOptions{})
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("answer %q: %v", answer, err)
		}
		if *r.hits != 0 || len(r.provisioned) != 0 {
			t.Errorf("answer %q: nothing may be downloaded or built before a yes", answer)
		}
		if !strings.Contains(r.out.String(), "Continue? [y/N]") {
			t.Errorf("the question is shown: %s", r.out)
		}
	}
	for _, answer := range []string{"y\n", "YES\n", " yes \n"} {
		r := newBuildRig(t)
		r.b.In = strings.NewReader(answer)
		if err := r.run(BuildOptions{}); err != nil {
			t.Errorf("answer %q: %v", answer, err)
		}
	}
}

func TestBuildPreflightFailures(t *testing.T) {
	t.Run("QEMU missing", func(t *testing.T) {
		r := newBuildRig(t)
		r.b.Discover = func(context.Context, string) (qemu.Install, error) { return qemu.Install{}, qemu.ErrNotFound }
		err := r.run(BuildOptions{Yes: true})
		if exitcode.Of(err) != exitcode.Environment || !strings.Contains(err.Error(), "controlsnack doctor") {
			t.Errorf("%v (%d)", err, exitcode.Of(err))
		}
	})
	t.Run("--qemu is passed on", func(t *testing.T) {
		r := newBuildRig(t)
		var got string
		r.b.Discover = func(_ context.Context, dir string) (qemu.Install, error) {
			got = dir
			return qemu.Install{}, qemu.ErrNotFound
		}
		cfg := config.Defaults()
		cfg.VM.QemuDir = "/opt/qemu/bin"
		_ = r.b.Run(context.Background(), cfg, BuildOptions{Yes: true})
		if got != "/opt/qemu/bin" {
			t.Errorf("%q", got)
		}
	})
	t.Run("accelerator error", func(t *testing.T) {
		r := newBuildRig(t)
		r.b.Accel = func(context.Context, config.Config, qemu.Install) (string, error) {
			return "", exitcode.Usagef("--accel=kvm is not available here")
		}
		if err := r.run(BuildOptions{Yes: true}); exitcode.Of(err) != exitcode.Usage {
			t.Errorf("%v", err)
		}
	})
	t.Run("not enough disk", func(t *testing.T) {
		r := newBuildRig(t)
		r.b.FreeSpace = func(string) (uint64, error) { return 3 << 30, nil }
		err := r.run(BuildOptions{Yes: true})
		if err == nil || !strings.Contains(err.Error(), "3.0 GiB free") || !strings.Contains(err.Error(), "needs 8.0 GiB") || *r.hits != 0 {
			t.Errorf("%v (%d downloads)", err, *r.hits)
		}
	})
	t.Run("disk space unreadable", func(t *testing.T) {
		r := newBuildRig(t)
		r.b.FreeSpace = func(string) (uint64, error) { return 0, errors.New("statfs failed") }
		if err := r.run(BuildOptions{Yes: true}); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("accelerator resolver and boot check are required", func(t *testing.T) {
		r := newBuildRig(t)
		r.b.Verify = nil
		if err := r.run(BuildOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("%v", err)
		}
		r = newBuildRig(t)
		r.b.Accel = nil
		if err := r.run(BuildOptions{Yes: true}); err == nil {
			t.Error("accepted")
		}
	})
}

func TestBuildLock(t *testing.T) {
	r := newBuildRig(t)
	if err := os.MkdirAll(r.b.Store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A live builder holds the lock: this process, identified by pid and start time.
	pi := platform.OSProcesses{}
	start, err := pi.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(r.b.Store.Dir, ".build.lock")
	if err := os.WriteFile(lock, []byte(fmt.Sprintf(`{"pid":%d,"start":%q}`, os.Getpid(), start)), 0o600); err != nil {
		t.Fatal(err)
	}
	err = r.run(BuildOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "another image build is running") {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(lock); serr != nil {
		t.Error("a held lock is not removed")
	}
	// A lock left by a dead process (or one whose pid was reused, so the start time differs) is taken over.
	for _, stale := range []string{fmt.Sprintf(`{"pid":%d,"start":"not-this-process"}`, os.Getpid()), `{"pid":999999,"start":"x"}`, `garbage`} {
		if err := os.WriteFile(lock, []byte(stale), 0o600); err != nil {
			t.Fatal(err)
		}
		r2 := newBuildRig(t)
		r2.b.Store = r.b.Store
		r2.b.Down = r.b.Down
		r2.b.Upstream = r.b.Upstream
		if err := r2.run(BuildOptions{Yes: true, Force: true}); err != nil {
			t.Errorf("stale lock %q: %v", stale, err)
		}
	}
	if _, serr := os.Stat(lock); !os.IsNotExist(serr) {
		t.Error("the lock is released at the end")
	}
}

func TestBuildDownloadFailures(t *testing.T) {
	t.Run("wrong bytes", func(t *testing.T) {
		r := newBuildRig(t)
		good := r.sums
		r.sums = func() string { return strings.Repeat("a", 128) + "  debian-13-generic-amd64.qcow2\n" }
		_ = good
		err := r.run(BuildOptions{Yes: true})
		if err == nil || !errors.Is(err, ErrChecksum) || len(r.provisioned) != 0 {
			t.Errorf("%v", err)
		}
		if _, serr := os.Stat(filepath.Join(r.b.Store.Dir, "downloads", "debian-13-generic-amd64.qcow2")); serr == nil {
			t.Error("a file that failed its checksum is not kept")
		}
	})
	t.Run("checksum list without the image", func(t *testing.T) {
		r := newBuildRig(t)
		r.sums = func() string { return strings.Repeat("a", 128) + "  something-else.qcow2\n" }
		if err := r.run(BuildOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "does not list") {
			t.Errorf("%v", err)
		}
	})
	t.Run("mirror unreachable", func(t *testing.T) {
		r := newBuildRig(t)
		r.srv.Close()
		if err := r.run(BuildOptions{Yes: true}); err == nil {
			t.Error("accepted")
		}
	})
}

func TestBuildStageFailuresCleanUp(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(*buildRig)
		want   string
	}{
		{"overlay", func(r *buildRig) {
			r.b.Overlay = func(context.Context, string, string, string, string) error {
				return errors.New("qemu-img: cannot create")
			}
		}, "qemu-img: cannot create"},
		{"provisioning", func(r *buildRig) {
			r.b.Provision = func(_ context.Context, p ProvisionRun) error {
				_ = os.WriteFile(filepath.Join(p.Dir, "serial.log"), []byte("kernel noise\nCS-PROVISION: FAILED at line 3: apt-get update\n"), 0o600)
				return errors.New("a provisioning step failed")
			}
		}, "provisioning failed: a provisioning step failed"},
		{"convert", func(r *buildRig) {
			r.b.Convert = func(context.Context, string, string, string) error { return errors.New("no space left on device") }
		}, "no space left"},
		{"boot check", func(r *buildRig) {
			r.b.Verify = func(context.Context, qemu.Install, string, string, io.Writer) error {
				return errors.New("the guest did not open the agent port")
			}
		}, "does not boot to a working desktop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newBuildRig(t)
			tt.break_(r)
			err := r.run(BuildOptions{Yes: true})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("%v", err)
			}
			if images, _, _ := r.b.Store.List(); len(images) != 0 {
				t.Errorf("a failed build stores nothing: %v", images)
			}
			entries, _ := os.ReadDir(r.b.Store.Dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "build-") || e.Name() == ".build.lock" {
					t.Errorf("%s was left behind", e.Name())
				}
			}
			if tt.name == "boot check" {
				kept := filepath.Join(r.b.Store.Dir, failedImageName)
				if _, serr := os.Stat(kept); serr != nil || !strings.Contains(err.Error(), kept) {
					t.Errorf("the sealed disk of an image that does not boot is kept and named: %v", serr)
				}
				r.b.Verify = func(context.Context, qemu.Install, string, string, io.Writer) error { return nil }
				if err := r.run(BuildOptions{Yes: true, Force: true}); err != nil {
					t.Fatal(err)
				}
				if _, serr := os.Stat(kept); serr == nil {
					t.Error("the next build removes the kept disk")
				}
			}
			if tt.name == "provisioning" {
				saved, rerr := os.ReadFile(filepath.Join(r.b.Store.Dir, "last-build-failure.log"))
				if rerr != nil || !strings.Contains(string(saved), "FAILED at line 3") || !strings.Contains(err.Error(), "last-build-failure.log") {
					t.Errorf("the console log of a failed provisioning is kept: %v %q", rerr, saved)
				}
			}
		})
	}
}

func TestBuildCanceled(t *testing.T) {
	r := newBuildRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.b.Provision = func(ctx context.Context, p ProvisionRun) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	err := r.b.Run(ctx, config.Defaults(), BuildOptions{Yes: true})
	if !errors.Is(err, context.Canceled) || exitcode.Of(err) != exitcode.Killed {
		t.Errorf("%v (%d)", err, exitcode.Of(err))
	}
	entries, _ := os.ReadDir(r.b.Store.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "build-") || e.Name() == ".build.lock" {
			t.Errorf("%s was left behind", e.Name())
		}
	}
}

// serial makes every fake build different, as real ones are.
var serial atomic.Int64
