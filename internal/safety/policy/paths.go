// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"path"
	"regexp"
	"strings"
)

// pathSetNames lists the named path sets a rule may refer to in writes_to.
var pathSetNames = map[string]struct{}{
	"block_devices": {}, "account_files": {}, "kernel_paths": {}, "persist_files": {},
}

// pathSets answers membership for the named sets. Paths passed in must be normalized (absolute, cleaned).
type pathSets struct {
	home          string
	blockDevices  []*regexp.Regexp
	accountFiles  []*regexp.Regexp
	kernelPaths   []*regexp.Regexp
	persistFiles  []*regexp.Regexp
	protectedRoot map[string]bool
}

func newPathSets(home string) *pathSets {
	q := regexp.QuoteMeta(home)
	re := func(patterns ...string) []*regexp.Regexp {
		out := make([]*regexp.Regexp, len(patterns))
		for i, p := range patterns {
			out[i] = regexp.MustCompile(p)
		}
		return out
	}
	roots := map[string]bool{"/": true}
	for _, r := range []string{"/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib32", "/lib64", "/libx32", "/opt", "/proc",
		"/root", "/run", "/sbin", "/srv", "/sys", "/usr", "/var", path.Clean(home)} {
		roots[r] = true
	}
	return &pathSets{
		home: home,
		// Exact device-node shapes: a looser prefix match would catch /dev/stderr as a SCSI tape (/dev/st0).
		blockDevices: re(
			`^/dev/(sd|vd|hd|xvd)[a-z]+[0-9]*$`,
			`^/dev/nvme[0-9]+(n[0-9]+(p[0-9]+)?)?$`,
			`^/dev/mmcblk[0-9]+(p[0-9]+)?$`,
			`^/dev/(loop|ram|zram|nbd|md|sr|fd|st|sg|mtdblock|mtd|dm-)[0-9]+(p[0-9]+)?$`,
			`^/dev/(mapper|disk|block)/.+`,
			`^/dev/(mem|kmem|port)$`,
		),
		accountFiles: re(
			`^/etc/(passwd|shadow|group|gshadow|subuid|subgid)-?$`,
			`^/etc/sudoers(\.d(/.*)?)?$`,
			`^/etc/(pam\.d|security)(/.*)?$`,
		),
		kernelPaths: re(
			`^/proc/sys(/.*)?$`,
			`^/sys(/.*)?$`,
			`^/proc/sysrq-trigger$`,
			`^/proc/kcore$`,
			`^/proc/(self|[0-9]+)/mem$`,
		),
		persistFiles: re(
			`^`+q+`/\.(bashrc|bash_profile|bash_login|bash_logout|profile|zshrc|zprofile|zlogin|xprofile|xsessionrc|xinitrc|pam_environment)$`,
			`^`+q+`/\.config/(autostart|systemd/user)(/.*)?$`,
			`^`+q+`/\.config/xfce4/xinitrc$`,
		),
		protectedRoot: roots,
	}
}

// in reports whether the normalized path p belongs to the named set.
func (ps *pathSets) in(set, p string) bool {
	var res []*regexp.Regexp
	switch set {
	case "block_devices":
		res = ps.blockDevices
	case "account_files":
		res = ps.accountFiles
	case "kernel_paths":
		res = ps.kernelPaths
	case "persist_files":
		res = ps.persistFiles
	}
	for _, re := range res {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

// normalizePath turns p into an absolute, cleaned path: "~" is the home directory, relative paths are resolved
// against cwd, "." and ".." are removed. It returns "" when that is impossible (relative path, unknown cwd).
// A trailing slash is dropped.
func normalizePath(p, cwd, home string) string {
	if p == "" {
		return ""
	}
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = home + p[1:]
	}
	if !strings.HasPrefix(p, "/") {
		if cwd == "" {
			return ""
		}
		p = cwd + "/" + p
	}
	return path.Clean(p)
}

// isProtectedRoot reports whether deleting p recursively would wipe the system or the home directory. p is
// normalized first; a trailing "/*" or "/." means "everything in", which is as destructive as the directory
// itself. A glob in the first path component of an absolute path (/e*, /?tc) can match any top-level directory.
func (ps *pathSets) isProtectedRoot(raw, cwd string) bool {
	p := raw
	for {
		if p == "/" {
			return true
		}
		switch {
		case strings.HasSuffix(p, "/*"):
			p = strings.TrimSuffix(p, "/*")
			if p == "" {
				return true // "/*"
			}
			continue
		case strings.HasSuffix(p, "/."), strings.HasSuffix(p, "/"):
			p = strings.TrimRight(strings.TrimSuffix(p, "."), "/")
			if p == "" {
				p = "/"
			}
			continue
		}
		break
	}
	if p == "*" || p == "." || p == ".." || p == "./*" {
		// Relative "everything here": protected only if the working directory itself is.
		if cwd == "" {
			return false
		}
		return ps.isProtectedRoot(cwd, "")
	}
	norm := normalizePath(p, cwd, ps.home)
	if norm == "" {
		return false
	}
	if strings.HasPrefix(norm, "/") && norm != "/" {
		first := strings.SplitN(strings.TrimPrefix(norm, "/"), "/", 2)[0]
		if strings.ContainsAny(first, "*?[{") {
			return true
		}
	}
	return ps.protectedRoot[norm]
}
