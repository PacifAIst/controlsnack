// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package egress

import (
	"net/netip"
	"strconv"
	"strings"
)

// Non-public IPv4 ranges (PLAN.md section 6.11).
var nonPublicV4 = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", // 240/4 includes 255.255.255.255
)

// Non-public IPv6 ranges. Anything outside 2000::/3 is also refused (see IsPublic), which already covers fc00::/7,
// fe80::/10, ff00::/8, ::1, :: and the rest; the explicit entries document the table of the plan and cover the
// ranges inside 2000::/3.
var nonPublicV6 = mustPrefixes(
	"::/128", "::1/128", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
	"3fff::/20", "5f00::/16", "fec0::/10", "64:ff9b:1::/48",
)

var (
	globalUnicastV6 = netip.MustParsePrefix("2000::/3")
	nat64           = netip.MustParsePrefix("64:ff9b::/96")
	v4Compatible    = netip.MustParsePrefix("::/96")
)

func mustPrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// IsPublic reports whether addr is a globally routable unicast address, and if not, which range it belongs to.
// Addresses that embed an IPv4 address (IPv4-mapped ::ffff:a.b.c.d, the deprecated IPv4-compatible ::a.b.c.d and
// NAT64 64:ff9b::a.b.c.d) are judged by the IPv4 address inside them: otherwise "::ffff:127.0.0.1" would reach
// loopback.
func IsPublic(addr netip.Addr) (public bool, why string) {
	addr = addr.WithZone("")
	if !addr.IsValid() {
		return false, "invalid address"
	}
	if addr.Is4In6() {
		return IsPublic(addr.Unmap())
	}
	if addr.Is4() {
		for _, p := range nonPublicV4 {
			if p.Contains(addr) {
				return false, "IPv4 range " + p.String()
			}
		}
		return true, ""
	}
	// :: and ::1 lie inside ::/96 but are not IPv4-compatible addresses: say what they are.
	switch addr {
	case netip.IPv6Unspecified():
		return false, "IPv6 unspecified address ::/128"
	case netip.IPv6Loopback():
		return false, "IPv6 loopback ::1/128"
	}
	b := addr.As16()
	// IPv4-compatible (::a.b.c.d) and NAT64: judge the embedded IPv4 address.
	if v4Compatible.Contains(addr) {
		embedded := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		if ok, why := IsPublic(embedded); !ok {
			return false, "IPv4-compatible address of a non-public IPv4 (" + why + ")"
		}
		return false, "IPv4-compatible IPv6 range ::/96"
	}
	if nat64.Contains(addr) {
		embedded := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		if ok, why := IsPublic(embedded); !ok {
			return false, "NAT64 address of a non-public IPv4 (" + why + ")"
		}
		return true, ""
	}
	for _, p := range nonPublicV6 {
		if p.Contains(addr) {
			return false, "IPv6 range " + p.String()
		}
	}
	if !globalUnicastV6.Contains(addr) {
		return false, "IPv6 address outside global unicast 2000::/3"
	}
	return true, ""
}

// ParseIPLiteral recognizes everything the C library's inet_aton and inet_pton would treat as an address, not
// just dotted quads: "2130706433", "0x7f000001", "0177.0.0.1", "127.1" and "0x7f.0.0.1" all mean 127.0.0.1 to a
// resolver, so a policy that only checked "looks like an IP" would let them through as hostnames. IPv6 literals
// may be written with or without brackets.
func ParseIPLiteral(host string) (netip.Addr, bool) {
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		// Brackets belong around IPv6 literals only.
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") || !strings.Contains(host, ":") {
			return netip.Addr{}, false
		}
		host = host[1 : len(host)-1]
	}
	if strings.Contains(host, ":") {
		a, err := netip.ParseAddr(host)
		return a, err == nil
	}
	return parseInetAton(host)
}

// parseInetAton parses the 1 to 4 part forms of inet_aton(3): each part is decimal, octal (leading 0) or hex
// (leading 0x), and the last part fills all remaining bytes.
func parseInetAton(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := parseAtonNumber(p)
		if !ok {
			return netip.Addr{}, false
		}
		nums[i] = n
	}
	last := nums[len(nums)-1]
	remaining := 4 - (len(nums) - 1) // bytes the last part covers
	if last >= uint64(1)<<(8*uint(remaining)) {
		return netip.Addr{}, false
	}
	var v uint64
	for i := 0; i < len(nums)-1; i++ {
		if nums[i] > 255 {
			return netip.Addr{}, false
		}
		v |= nums[i] << (8 * uint(3-i))
	}
	v |= last
	return netip.AddrFrom4([4]byte{byte(v >> 24 & 0xff), byte(v >> 16 & 0xff), byte(v >> 8 & 0xff), byte(v & 0xff)}), true
}

func parseAtonNumber(p string) (uint64, bool) {
	if p == "" {
		return 0, false
	}
	base, digits := 10, p
	switch {
	case len(p) > 2 && (p[:2] == "0x" || p[:2] == "0X"):
		base, digits = 16, p[2:]
	case p == "0x" || p == "0X":
		return 0, false
	case len(p) > 1 && p[0] == '0':
		base, digits = 8, p[1:]
	}
	for _, r := range digits {
		isDigit := (r >= '0' && r <= '9') || (base == 16 && ((r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')))
		if !isDigit {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(digits, base, 64)
	return n, err == nil
}
