// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/platform"
)

const (
	// cacheTTL is how long a probe is believed: models are pulled again under the same name, servers are updated.
	cacheTTL = 14 * 24 * time.Hour
	// maxCacheFile bounds what is read back: the file is the user's, but a bug or a full disk can leave anything in it.
	maxCacheFile = 64 << 10
	cacheSubdir  = "probe"
)

// cachePath is the file of a probe result. The name is a hash of everything the result depends on, so a result is never
// used for another model, another server or another build of the model; the API key is not part of it, and is never
// written.
func (d *Detector) cachePath(t Target, digest string) string {
	if d.opts.CacheDir == "" {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{t.Backend, t.BaseURL, t.Model, digest} {
		_, _ = io.WriteString(h, part)
		_, _ = h.Write([]byte{0})
	}
	return filepath.Join(d.opts.CacheDir, cacheSubdir, hex.EncodeToString(h.Sum(nil))[:32]+".json")
}

// cached returns the result of an earlier probe of the same model, if there is one that is current.
func (d *Detector) cached(t Target, digest string) (Capabilities, bool) {
	path := d.cachePath(t, digest)
	if path == "" {
		return Capabilities{}, false
	}
	f, err := os.Open(path) //nolint:gosec // a file of our own cache directory, named by a hash
	if err != nil {
		return Capabilities{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCacheFile+1))
	if err != nil || len(data) > maxCacheFile {
		return Capabilities{}, false
	}
	var c Capabilities
	if json.Unmarshal(data, &c) != nil {
		return Capabilities{}, false
	}
	age := d.opts.Now().Sub(c.ProbedAt)
	switch {
	case c.Version != ProbeVersion,
		c.Backend != t.Backend, c.BaseURL != t.BaseURL, c.Model != t.Model, c.Digest != digest,
		age > cacheTTL, age < -time.Hour: // a result from the future is a clock that was wrong when it was written
		return Capabilities{}, false
	}
	c.Cached = true
	return c, true
}

// store keeps a result. It is best effort: a probe that cannot be cached is a probe that runs again next time.
func (d *Detector) store(t Target, c Capabilities) {
	path := d.cachePath(t, c.Digest)
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if platform.EnsureDir(dir) != nil {
		return
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".probe-*.tmp") // mode 0600
	if err != nil {
		return
	}
	name := tmp.Name()
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(name, path) != nil {
		_ = os.Remove(name)
	}
}
