// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/protocol"
)

func peek(t *testing.T, body any) (protocol.FilePeekRes, []byte, error) {
	t.Helper()
	res, blob, err := FilePeek{}.Handle(context.Background(), frame(t, protocol.TypeFilePeek, body))
	if err != nil {
		return protocol.FilePeekRes{}, nil, err
	}
	return res.(protocol.FilePeekRes), blob, nil
}

func TestFilePeekReadsTheHead(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	content := "#!/bin/bash\necho hello\n" + strings.Repeat("# padding\n", 100)
	if err := os.WriteFile(script, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, blob, err := peek(t, protocol.FilePeekReq{Path: script, MaxBytes: 20})
	if err != nil || !res.Exists || res.Size != int64(len(content)) || string(blob) != content[:20] {
		t.Errorf("%+v %q %v", res, blob, err)
	}
	// Without max_bytes the head is up to 64 KiB, here the whole small file.
	res, blob, err = peek(t, protocol.FilePeekReq{Path: script})
	if err != nil || string(blob) != content || res.Size != int64(len(content)) {
		t.Errorf("%+v %d bytes %v", res, len(blob), err)
	}
	// max_bytes larger than the file is the whole file.
	_, blob, _ = peek(t, protocol.FilePeekReq{Path: script, MaxBytes: 60000})
	if string(blob) != content {
		t.Errorf("%d bytes", len(blob))
	}
}

func TestFilePeekReadsOnlyTheHeadOfABigFile(t *testing.T) {
	big := filepath.Join(t.TempDir(), "big")
	data := bytes.Repeat([]byte("0123456789"), 300000) // 3 MB
	if err := os.WriteFile(big, data, 0o600); err != nil {
		t.Fatal(err)
	}
	res, blob, err := peek(t, protocol.FilePeekReq{Path: big, MaxBytes: 1000})
	if err != nil || res.Size != 3000000 || len(blob) != 1000 || !bytes.Equal(blob, data[:1000]) {
		t.Errorf("%+v %d bytes %v", res, len(blob), err)
	}
	_, blob, _ = peek(t, protocol.FilePeekReq{Path: big})
	if len(blob) != 64<<10 {
		t.Errorf("the default head is 64 KiB: %d", len(blob))
	}
}

func TestFilePeekMissingAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	res, blob, err := peek(t, protocol.FilePeekReq{Path: filepath.Join(dir, "nope")})
	if err != nil || res.Exists || res.Size != 0 || blob != nil {
		t.Errorf("a missing file is a fact: %+v %v %v", res, blob, err)
	}
	res, blob, err = peek(t, protocol.FilePeekReq{Path: dir})
	if err != nil || !res.Exists || res.Size != 0 || blob != nil {
		t.Errorf("a directory exists and has no head: %+v %v %v", res, blob, err)
	}
	if runtime.GOOS == "linux" {
		res, blob, err = peek(t, protocol.FilePeekReq{Path: "/dev/zero", MaxBytes: 100})
		if err != nil || !res.Exists || blob != nil {
			t.Errorf("a device is not read: %+v %d bytes %v", res, len(blob), err)
		}
	}
	empty := filepath.Join(dir, "empty")
	_ = os.WriteFile(empty, nil, 0o600)
	res, blob, err = peek(t, protocol.FilePeekReq{Path: empty})
	if err != nil || !res.Exists || res.Size != 0 || len(blob) != 0 {
		t.Errorf("%+v %v %v", res, blob, err)
	}
}

func TestFilePeekFollowsALinkToAFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	_ = os.WriteFile(target, []byte("through the link"), 0o600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks here")
	}
	res, blob, err := peek(t, protocol.FilePeekReq{Path: link})
	if err != nil || !res.Exists || string(blob) != "through the link" {
		t.Errorf("%+v %q %v", res, blob, err)
	}
}

func TestFilePeekRefusals(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "f")
	for name, req := range map[string]any{
		"empty path":       protocol.FilePeekReq{},
		"relative path":    protocol.FilePeekReq{Path: "run.sh"},
		"dot path":         protocol.FilePeekReq{Path: "./run.sh"},
		"a NUL":            protocol.FilePeekReq{Path: abs + "\x00x"},
		"a long path":      protocol.FilePeekReq{Path: abs + strings.Repeat("a", 5000)},
		"negative bytes":   protocol.FilePeekReq{Path: abs, MaxBytes: -1},
		"too many bytes":   protocol.FilePeekReq{Path: abs, MaxBytes: 64<<10 + 1},
		"an unknown field": map[string]any{"path": abs, "follow": true},
	} {
		if _, _, err := peek(t, req); code(err) != protocol.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFilePeekStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := FilePeek{}.Handle(ctx, frame(t, protocol.TypeFilePeek, protocol.FilePeekReq{Path: filepath.Join(t.TempDir(), "f")}))
	if err == nil || ctx.Err() == nil {
		t.Errorf("%v", err)
	}
}
