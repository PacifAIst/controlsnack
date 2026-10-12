// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpstreamNames(t *testing.T) {
	u := DebianStable
	for arch, want := range map[string]string{ArchX86_64: "debian-13-genericcloud-amd64.qcow2", ArchAarch64: "debian-13-genericcloud-arm64.qcow2"} {
		name, err := u.FileName(arch)
		url, uerr := u.ImageURL(arch)
		if err != nil || uerr != nil || name != want || url != "https://cloud.debian.org/images/cloud/trixie/latest/"+want {
			t.Errorf("%s: %q %q %v %v", arch, name, url, err, uerr)
		}
	}
	if _, err := u.FileName("riscv64"); err == nil {
		t.Error("no riscv64 image")
	}
	if _, err := u.ImageURL(""); err == nil {
		t.Error("empty arch")
	}
	if u.ChecksumsURL() != "https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS" {
		t.Error(u.ChecksumsURL())
	}
}

func TestUpstreamResolveAndRecord(t *testing.T) {
	sums := strings.Repeat("a", 128) + "  debian-13-genericcloud-amd64.qcow2\n" + strings.Repeat("b", 128) + "  debian-13-genericcloud-arm64.qcow2\n"
	srv, d := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dir/SHA512SUMS" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sums))
	}))
	u := UpstreamSpec{BaseURL: srv.URL + "/dir/", Version: "13", SignatureNote: "no signature"}
	tg, err := u.Resolve(context.Background(), d, ArchAarch64)
	if err != nil || tg.SHA512 != strings.Repeat("b", 128) || tg.URL != srv.URL+"/dir/debian-13-genericcloud-arm64.qcow2" || tg.FileName != "debian-13-genericcloud-arm64.qcow2" {
		t.Fatalf("%+v %v", tg, err)
	}
	rec := u.Record(tg)
	if rec.SignatureVerified || rec.SignatureNote != "no signature" || rec.URL != tg.URL || rec.SHA512 != tg.SHA512 {
		t.Errorf("%+v", rec)
	}
	if _, err := (UpstreamSpec{BaseURL: srv.URL + "/other/", Version: "13"}).Resolve(context.Background(), d, ArchX86_64); err == nil {
		t.Error("a directory without a checksum list")
	}
	if _, err := (UpstreamSpec{BaseURL: srv.URL + "/dir/", Version: "99"}).Resolve(context.Background(), d, ArchX86_64); err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Errorf("an image the list does not contain: %v", err)
	}
}

// TestRealDebian checks the parsing, the redirect and the size limit against the real Debian server. It needs the
// network, so it only runs when CONTROLSNACK_NET_TESTS=1.
func TestRealDebian(t *testing.T) {
	if os.Getenv("CONTROLSNACK_NET_TESTS") != "1" {
		t.Skip("set CONTROLSNACK_NET_TESTS=1 to test against cloud.debian.org")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := Downloader{}
	for _, arch := range []string{ArchX86_64, ArchAarch64} {
		tg, err := DebianStable.Resolve(ctx, d, arch)
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		if !reHex128.MatchString(tg.SHA512) {
			t.Errorf("%s: %+v", arch, tg)
		}
		t.Logf("%s: %s sha512 %s…", arch, tg.FileName, tg.SHA512[:16])
	}
	// The image URL redirects to a mirror; the announced size comes through the redirect and is checked against the
	// limit before a single byte of the body is read.
	tg, _ := DebianStable.Resolve(ctx, d, ArchX86_64)
	small := Downloader{MaxSize: 1 << 20}
	dest := t.TempDir() + "/img"
	_, err := small.Download(ctx, tg.URL, dest, tg.SHA512)
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("a 1 MiB limit on a 300 MB image: %v", err)
	}
	if fi, serr := os.Stat(dest + ".part"); serr == nil && fi.Size() != 0 {
		t.Errorf("nothing was downloaded: %d bytes", fi.Size())
	}
	// A real range request: the mirror honors it.
	total, body, status, err := Downloader{}.open(ctx, tg.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	t.Logf("range request at offset 1000: status %d, total %d bytes", status, total)
	if status != http.StatusPartialContent || total < 100<<20 {
		t.Errorf("status %d total %d", status, total)
	}
}
