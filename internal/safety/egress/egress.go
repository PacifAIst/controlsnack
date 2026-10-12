// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/idna"
)

// Resolver turns a host name into addresses. The default uses the host's resolver; tests inject fakes (including
// ones that change their answer, to show that a rebinding attempt cannot work).
type Resolver interface {
	LookupAddrs(ctx context.Context, host string) ([]netip.Addr, error)
}

type systemResolver struct{}

func (systemResolver) LookupAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// Decision is the outcome of checking one destination. It is what the audit log records for the `net` kind.
type Decision struct {
	Allowed bool `json:"allowed"`
	// Rule names what decided: ok, port, host-syntax, localhost, deny-list, allow-list, private-address,
	// resolve-failed, no-address.
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
	// Host is the normalized host name (or the textual IP) and Port the destination port.
	Host string `json:"host"`
	Port int    `json:"port"`
	// Addrs are the vetted addresses to dial, in resolver order. Only these may be connected to.
	Addrs []netip.Addr `json:"addrs,omitempty"`
}

// Policy decides which destinations the guest may reach through the --allow-net bridge. The zero value denies
// everything that is not on port 80 or 443 and has no domain restrictions; use New.
type Policy struct {
	ports        map[int]bool
	allowDomains []string
	denyDomains  []string
	resolver     Resolver
	// dial is replaceable in tests; production dials with the checking net.Dialer.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// Config configures a Policy.
type Config struct {
	// ExtraPorts are allowed in addition to 80 and 443.
	ExtraPorts []int
	// AllowDomains, when non-empty, restricts the guest to these domains and their subdomains.
	AllowDomains []string
	// DenyDomains are refused even if AllowDomains would admit them.
	DenyDomains []string
	// Resolver defaults to the system resolver.
	Resolver Resolver
}

// New builds a Policy. Domain lists are normalized (lower case, no trailing dot, IDNA).
func New(c Config) (*Policy, error) {
	p := &Policy{ports: map[int]bool{80: true, 443: true}, resolver: c.Resolver}
	if p.resolver == nil {
		p.resolver = systemResolver{}
	}
	for _, port := range c.ExtraPorts {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("port %d is not valid", port)
		}
		p.ports[port] = true
	}
	var err error
	if p.allowDomains, err = normalizeDomains(c.AllowDomains); err != nil {
		return nil, fmt.Errorf("allowed domains: %w", err)
	}
	if p.denyDomains, err = normalizeDomains(c.DenyDomains); err != nil {
		return nil, fmt.Errorf("denied domains: %w", err)
	}
	return p, nil
}

func normalizeDomains(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, d := range list {
		n, err := normalizeHost(d)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", d, err)
		}
		if _, isIP := ParseIPLiteral(n); isIP {
			return nil, fmt.Errorf("%q is an IP address, not a domain", d)
		}
		out = append(out, n)
	}
	return out, nil
}

// normalizeHost lower-cases a host name, removes a trailing dot and converts it to its ASCII (punycode) form.
// IP literals are returned as written, apart from case and brackets.
func normalizeHost(raw string) (string, error) {
	h := strings.TrimSpace(raw)
	if h == "" || h != raw {
		return "", errors.New("empty host or surrounding whitespace")
	}
	if strings.ContainsAny(h, "%/\\@?# \t\r\n\x00") {
		return "", errors.New("the host contains characters that do not belong in a host name")
	}
	if _, ok := ParseIPLiteral(h); ok {
		return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")), nil
	}
	if strings.Contains(h, ":") {
		return "", errors.New("not a valid IP address or host name")
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "" || strings.Contains(h, "..") || strings.HasPrefix(h, ".") {
		return "", errors.New("empty label in the host name")
	}
	ascii, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return "", fmt.Errorf("not a valid host name: %w", err)
	}
	if len(ascii) > 253 {
		return "", errors.New("host name longer than 253 characters")
	}
	labels := strings.Split(ascii, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return "", errors.New("host name label is empty or longer than 63 characters")
		}
	}
	return ascii, nil
}

func matchesDomain(host string, list []string) (string, bool) {
	for _, d := range list {
		if host == d || strings.HasSuffix(host, "."+d) {
			return d, true
		}
	}
	return "", false
}

// Check decides whether the guest may connect to host:port. For a host name it resolves the name once, here, and
// refuses if any answer is not a public address; the returned Decision carries the vetted addresses and Dial
// connects to exactly those, so the name is never looked up a second time. That closes the DNS-rebinding gap
// between check and use.
func (p *Policy) Check(ctx context.Context, host string, port int) Decision {
	d := Decision{Host: host, Port: port}
	deny := func(rule, format string, args ...any) Decision {
		d.Rule, d.Reason = rule, fmt.Sprintf(format, args...)
		return d
	}

	if !p.ports[port] {
		return deny("port", "port %d is not allowed (80 and 443 are; add others with --allow-port)", port)
	}
	norm, err := normalizeHost(host)
	if err != nil {
		return deny("host-syntax", "%v", err)
	}
	d.Host = norm

	if ip, isIP := ParseIPLiteral(norm); isIP {
		if len(p.allowDomains) > 0 {
			return deny("allow-list", "only the allowed domains can be reached, not the address %s", ip)
		}
		if ok, why := IsPublic(ip); !ok {
			return deny("private-address", "%s is not a public address (%s)", ip, why)
		}
		d.Allowed, d.Rule, d.Reason, d.Addrs = true, "ok", "public address", []netip.Addr{ip.Unmap()}
		return d
	}

	// A host name.
	labels := strings.Split(norm, ".")
	switch {
	case norm == "localhost" || strings.HasSuffix(norm, ".localhost"):
		return deny("localhost", "localhost is the VM itself, and the host's loopback is never reachable")
	case len(labels) < 2:
		return deny("host-syntax", "%q is not a public domain name (no dot)", norm)
	case isAllDigits(labels[len(labels)-1]):
		return deny("host-syntax", "%q does not end in a valid top-level domain", norm)
	}
	if dom, ok := matchesDomain(norm, p.denyDomains); ok {
		return deny("deny-list", "the domain %s is on the deny list", dom)
	}
	if len(p.allowDomains) > 0 {
		if _, ok := matchesDomain(norm, p.allowDomains); !ok {
			return deny("allow-list", "%s is not on the allowed domain list", norm)
		}
	}

	addrs, err := p.resolver.LookupAddrs(ctx, norm)
	if err != nil {
		return deny("resolve-failed", "cannot resolve %s: %v", norm, err)
	}
	if len(addrs) == 0 {
		return deny("no-address", "%s has no addresses", norm)
	}
	vetted := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		// One bad answer poisons the lot: a name that resolves to both a public and a private address is how
		// rebinding and split-horizon tricks work.
		if ok, why := IsPublic(a); !ok {
			return deny("private-address", "%s resolves to %s, which is not a public address (%s)", norm, a, why)
		}
		vetted = append(vetted, a.Unmap())
	}
	d.Allowed, d.Rule, d.Reason, d.Addrs = true, "ok", "allowed", vetted
	return d
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// DialTimeout bounds one connection attempt to one address.
const DialTimeout = 10 * time.Second

// Dial checks host:port and connects. It dials the vetted addresses directly, never the name, and a net.Dialer
// Control hook looks at each address again right before the connection is made, so even a bug between check and
// connect could not reach a non-public address.
func (p *Policy) Dial(ctx context.Context, host string, port int) (net.Conn, Decision, error) {
	d := p.Check(ctx, host, port)
	if !d.Allowed {
		return nil, d, fmt.Errorf("connection refused by the egress policy: %s", d.Reason)
	}
	dial := p.dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: DialTimeout, Control: p.control}
		dial = dialer.DialContext
	}
	var errs []error
	for _, a := range d.Addrs {
		network := "tcp4"
		if a.Is6() {
			network = "tcp6"
		}
		conn, err := dial(ctx, network, net.JoinHostPort(a.String(), strconv.Itoa(port)))
		if err == nil {
			return conn, d, nil
		}
		errs = append(errs, err)
	}
	return nil, d, fmt.Errorf("could not connect to %s:%d: %w", d.Host, port, errors.Join(errs...))
}

// control re-checks the address the operating system is about to connect to.
func (p *Policy) control(_, address string, _ syscall.RawConn) error {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("egress policy: bad address %q", address)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || !p.ports[port] {
		return fmt.Errorf("egress policy: port %q is not allowed", portStr)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("egress policy: %q is not an IP address", host)
	}
	if ok, why := IsPublic(ip); !ok {
		return fmt.Errorf("egress policy: %s is not a public address (%s)", ip, why)
	}
	return nil
}
