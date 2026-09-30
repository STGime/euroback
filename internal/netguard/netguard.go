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
	"net"
	"net/netip"
	"strings"
	"time"
)

// ErrNotPublic: the host doesn't exist in public DNS, or is (or resolves
// to) an address that isn't public. One error for both on purpose: telling
// them apart would reveal which internal names exist.
var ErrNotPublic = errors.New("host not found, or not a public internet address")

// IPv4 ranges that are never a customer's public server, beyond what
// netip.Addr's own classifiers cover (loopback, private, link-local,
// multicast, unspecified).
var nonPublic4 = mustPrefixes(
	"0.0.0.0/8",       // "this network"; 0.x can reach the local host
	"100.64.0.0/10",   // carrier-grade NAT (also the cluster's pod range)
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved, incl. broadcast
)

// IPv6: only global unicast (2000::/3) is public, minus the special-purpose
// and documentation blocks inside it. 6to4 and the well-known NAT64 prefix
// embed an IPv4 address, checked by the IPv4 rules.
var (
	global6    = netip.MustParsePrefix("2000::/3")
	nonPublic6 = mustPrefixes(
		"2001::/23",     // IETF special purpose (incl. Teredo 2001::/32)
		"2001:db8::/32", // documentation
		"3fff::/20",     // documentation
	)
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4 = netip.MustParsePrefix("2002::/16")
)

// internalSuffixes are name suffixes that only resolve inside a network.
var internalSuffixes = []string{".local", ".localhost", ".internal", ".svc", ".cluster.local", ".lan", ".home.arpa", ".arpa"}

// CheckAddr returns nil for a public unicast address.
func CheckAddr(a netip.Addr) error {
	if !a.IsValid() || a.Zone() != "" {
		return ErrNotPublic
	}
	a = a.Unmap()
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64.Contains(a):
			return CheckAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case sixTo4.Contains(a):
			return CheckAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		case !global6.Contains(a):
			return ErrNotPublic
		}
		for _, p := range nonPublic6 {
			if p.Contains(a) {
				return ErrNotPublic
			}
		}
		return nil
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return ErrNotPublic
	}
	for _, p := range nonPublic4 {
		if p.Contains(a) {
			return ErrNotPublic
		}
	}
	return nil
}

// Normalize lowercases and trims a host; "" if it carries anything but a
// name or an address (brackets, a port, a zone, spaces inside).
func Normalize(host string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if strings.ContainsAny(h, "[]/ \t@%") {
		return ""
	}
	return h
}

// CheckHostName refuses names that can only mean something inside a
// network (localhost, single labels, internal suffixes) and address
// literals that aren't public. It doesn't resolve: a public-looking name
// is resolved and checked again at dial time.
func CheckHostName(host string) error {
	h := Normalize(host)
	if h == "" {
		return ErrNotPublic
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return CheckAddr(a)
	}
	if strings.Contains(h, ":") || h == "localhost" || !strings.Contains(h, ".") {
		return ErrNotPublic
	}
	for _, s := range internalSuffixes {
		if strings.HasSuffix(h, s) {
			return ErrNotPublic
		}
	}
	return nil
}

// Dialer connects to customer-configured hosts. The zero value uses the
// system resolver and a plain TCP dialer.
type Dialer struct {
	// Lookup resolves a fully qualified (rooted) name.
	Lookup func(ctx context.Context, fqdn string) ([]netip.Addr, error)
	// Dial connects to an address literal "ip:port".
	Dial func(ctx context.Context, addr string) (net.Conn, error)
	// Timeout covers the lookup and all connection attempts; each attempt
	// gets an equal share of what's left, so one silent address can't
	// use it all. Default 10 s.
	Timeout time.Duration
}

// DialPublic is (&Dialer{}).DialContext.
func DialPublic(ctx context.Context, host, port string) (net.Conn, error) {
	return (&Dialer{}).DialContext(ctx, host, port)
}

// DialContext resolves host, keeps its public addresses and connects to
// the first that answers. It never connects to a non-public address, and
// a lookup failure and a non-public answer look the same to the caller.
func (d *Dialer) DialContext(ctx context.Context, host, port string) (net.Conn, error) {
	if err := CheckHostName(host); err != nil {
		return nil, err
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	h := Normalize(host)
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(h); err == nil {
		addrs = []netip.Addr{a}
	} else {
		lookup := d.Lookup
		if lookup == nil {
			lookup = func(ctx context.Context, fqdn string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", fqdn)
			}
		}
		// Rooted: the resolver's search list (which would complete
		// "svc.namespace" to a cluster service) is never used.
		ips, err := lookup(ctx, h+".")
		if err != nil {
			return nil, ErrNotPublic
		}
		addrs = ips
	}
	var public []netip.Addr
	for _, a := range addrs {
		if CheckAddr(a) == nil {
			public = append(public, a.Unmap())
		}
	}
	if len(public) == 0 {
		return nil, ErrNotPublic
	}
	dial := d.Dial
	if dial == nil {
		var nd net.Dialer
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return nd.DialContext(ctx, "tcp", addr) }
	}
	var lastErr error
	for i, a := range public {
		end, _ := ctx.Deadline()
		actx, acancel := context.WithTimeout(ctx, time.Until(end)/time.Duration(len(public)-i))
		conn, err := dial(actx, net.JoinHostPort(a.String(), port))
		acancel()
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
