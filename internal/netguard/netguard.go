// Package netguard decides whether an outbound connection that a customer
// configured (a host name or address they typed in) may be made: only to
// public internet addresses, never to the cluster, the node, the cloud
// metadata service or other non-routable ranges. Resolve and check at dial
// time, and connect to the checked address — a name that resolved to a
// public address when it was saved can point somewhere else later.
//
// A leaf package (stdlib only) so any package can use it.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ErrNotPublic: the host is not (or doesn't resolve to) a public address.
var ErrNotPublic = errors.New("not a public internet address")

// nonPublic are ranges that are never a customer's public server, beyond
// what netip.Addr's own classifiers cover (loopback, private, link-local,
// multicast, unspecified).
var nonPublic = mustPrefixes(
	"0.0.0.0/8",       // "this network"; 0.x can reach the local host
	"100.64.0.0/10",   // carrier-grade NAT (also the cluster's pod range)
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved, incl. broadcast
	"100::/64",        // discard
	"2001:db8::/32",   // documentation
)

// Prefixes that embed an IPv4 address, checked against the IPv4 rules.
var (
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4 = netip.MustParsePrefix("2002::/16")
)

// internalSuffixes are name suffixes that only resolve inside a network.
var internalSuffixes = []string{".local", ".localhost", ".internal", ".svc", ".cluster.local", ".lan", ".home.arpa"}

// CheckAddr returns nil for a public unicast address.
func CheckAddr(a netip.Addr) error {
	a = a.Unmap()
	if a.Is6() && nat64.Contains(a) {
		b := a.As16()
		return CheckAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	if a.Is6() && sixTo4.Contains(a) {
		b := a.As16()
		return CheckAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
	}
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return ErrNotPublic
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return ErrNotPublic
		}
	}
	return nil
}

// CheckHostName refuses names that can only mean something inside a
// network (localhost, single labels that the resolver's search domains
// would complete to cluster services, internal suffixes) and address
// literals that aren't public. It doesn't resolve: a public-looking name
// is checked again, resolved, at dial time.
func CheckHostName(host string) error {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "" {
		return fmt.Errorf("empty host: %w", ErrNotPublic)
	}
	if a, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return CheckAddr(a)
	}
	if h == "localhost" || !strings.Contains(h, ".") {
		return ErrNotPublic
	}
	for _, s := range internalSuffixes {
		if strings.HasSuffix(h, s) {
			return ErrNotPublic
		}
	}
	return nil
}

// DialPublic resolves host, keeps its public addresses and connects to
// the first that answers. It never connects to a non-public address, and
// its errors never name one.
func DialPublic(ctx context.Context, d *net.Dialer, host, port string) (net.Conn, error) {
	if err := CheckHostName(host); err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		addrs = []netip.Addr{a}
	} else {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		addrs = ips
	}
	var lastErr error = ErrNotPublic
	for _, a := range addrs {
		if CheckAddr(a) != nil {
			continue
		}
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}
