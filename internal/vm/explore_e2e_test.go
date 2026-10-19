// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// TestExploreGuest is a development harness, not a test: it boots the image once and then runs whatever it finds in the
// directory CS_EXPLORE_DIR (in-N.sh: a shell script, in-N.shot: a png screenshot, in-N.call: {"type":..,"req":{..}}),
// writing out-N.txt / out-N.png, until a file named "stop" appears. Skipped unless CS_EXPLORE_DIR is set.
func TestExploreGuest(t *testing.T) {
	dir := os.Getenv("CS_EXPLORE_DIR")
	if dir == "" {
		t.Skip("CS_EXPLORE_DIR is not set")
	}
	net := os.Getenv("CS_EXPLORE_NET") != ""
	m, _ := bootE2EWith(t, 1280, 800, func(o *PrepareOptions) { o.Net = net })
	ctx := context.Background()
	t.Logf("ready; watching %s", dir)
	done := map[string]bool{}
	deadline := time.Now().Add(110 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
			return
		}
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "in-") && !done[e.Name()] {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			done[name] = true
			base := strings.TrimSuffix(name, filepath.Ext(name))
			out := filepath.Join(dir, strings.Replace(base, "in-", "out-", 1))
			body, _ := os.ReadFile(filepath.Join(dir, name))
			switch filepath.Ext(name) {
			case ".sh":
				var res protocol.ExecRes
				_, err := m.Client.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: string(body), TimeoutMS: 300000}, nil, &res)
				text := res.Stdout
				if res.Stderr != "" {
					text += "\n--- stderr ---\n" + res.Stderr
				}
				if err != nil {
					text += "\n--- error ---\n" + err.Error()
				}
				_ = os.WriteFile(out+".txt", []byte(text), 0o600)
			case ".shot":
				var res protocol.ScreenshotRes
				blob, err := m.Client.Call(ctx, protocol.TypeScreenshot, protocol.ScreenshotReq{Format: "png"}, nil, &res)
				if err != nil {
					_ = os.WriteFile(out+".txt", []byte(err.Error()), 0o600)
				} else {
					_ = os.WriteFile(out+".png", blob, 0o600)
				}
			case ".call":
				var c struct {
					Type string          `json:"type"`
					Req  json.RawMessage `json:"req"`
				}
				if err := json.Unmarshal(body, &c); err != nil {
					_ = os.WriteFile(out+".txt", []byte(err.Error()), 0o600)
					continue
				}
				var res json.RawMessage
				blob, err := m.Client.Call(ctx, c.Type, c.Req, nil, &res)
				text := string(res)
				if err != nil {
					text += "\nerror: " + err.Error()
				}
				if len(blob) > 0 {
					_ = os.WriteFile(out+".bin", blob, 0o600)
				}
				_ = os.WriteFile(out+".txt", []byte(text), 0o600)
			}
		}
		time.Sleep(time.Second)
	}
}
