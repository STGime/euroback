package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckAddr(t *testing.T) {
	refused := []string{
		"127.0.0.1", "10.32.0.10", "172.16.5.4", "192.168.1.1", "169.254.42.42", "169.254.169.254",
		"100.64.0.1", "100.127.255.254", "0.0.0.0", "0.1.2.3", "224.0.0.1", "255.255.255.255", "240.1.1.1",
		"192.0.2.10", "198.18.0.1", "::1", "::", "fe80::1", "fe80::1%eth0", "fd00::1", "fd00:42::42", "ff02::1",
		"2001:db8::1", "fec0::1", "100::1:2:3:4",
		"::ffff:10.0.0.1",    // IPv4-mapped
		"::7f00:1",           // IPv4-compatible
		"::ffff:0:7f00:1",    // SIIT
		"64:ff9b::a00:1",     // NAT64 → 10.0.0.1
		"64:ff9b::7f00:1",    // NAT64 → 127.0.0.1
		"64:ff9b:1::a00:1",   // local-use NAT64
		"2002:a00:1::1",      // 6to4 → 10.0.0.1
		"2001:0:4136:e378::", // Teredo
	}
	for _, s := range refused {
		if CheckAddr(netip.MustParseAddr(s)) == nil {
			t.Errorf("%s: accepted, want refused", s)
		}
	}
	allowed := []string{"51.15.217.235", "167.235.125.60", "2a01:4f8::1", "64:ff9b::33f:d9eb", "2002:330f:d9eb::1", "100.128.0.1"}
	for _, s := range allowed {
		if err := CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: refused (%v), want accepted", s, err)
		}
	}
}

func TestCheckHostName(t *testing.T) {
	for _, h := range []string{"", "localhost", "LOCALHOST.", "pgbouncer", "gateway.eurobase.svc.cluster.local",
		"db.eurobase.svc", "printer.local", "x.internal", "127.0.0.1", "[::1]", "[2a01:4f8::1]", "10.0.0.1",
		"smtp.example.com:25", "a b.example.com", "fe80::1%eth0", "::7f00:1"} {
		if CheckHostName(h) == nil {
			t.Errorf("%q: accepted, want refused", h)
		}
	}
	for _, h := range []string{"smtp.example.com", " Smtp.Example.com ", "www783.your-server.de", "mail.perweg.de.", "51.15.217.235", "2a01:4f8::1"} {
		if err := CheckHostName(h); err != nil {
			t.Errorf("%q: refused (%v), want accepted", h, err)
		}
	}
}

// fakeDialer records what would have been dialed.
func fakeDialer(answers []string, lookupErr error) (*Dialer, *[]string, *[]string) {
	var looked, dialed []string
	d := &Dialer{
		Timeout: time.Second,
		Lookup: func(_ context.Context, fqdn string) ([]netip.Addr, error) {
			looked = append(looked, fqdn)
			if lookupErr != nil {
				return nil, lookupErr
			}
			var out []netip.Addr
			for _, a := range answers {
				out = append(out, netip.MustParseAddr(a))
			}
			return out, nil
		},
		Dial: func(_ context.Context, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			c1, c2 := net.Pipe()
			c2.Close()
			return c1, nil
		},
	}
	return d, &looked, &dialed
}

func TestDialContext_OnlyPublicAnswersAreDialed(t *testing.T) {
	// Mixed answer: the private one is skipped, the public one dialed.
	d, looked, dialed := fakeDialer([]string{"10.32.0.10", "::ffff:127.0.0.1", "51.15.217.235"}, nil)
	conn, err := d.DialContext(context.Background(), "Smtp.Example.com", "587")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if len(*dialed) != 1 || (*dialed)[0] != "51.15.217.235:587" {
		t.Fatalf("dialed %v", *dialed)
	}
	// Rooted lookup: the resolver's search list is never used.
	if len(*looked) != 1 || (*looked)[0] != "smtp.example.com." {
		t.Fatalf("looked up %v", *looked)
	}

	// Only private answers: nothing is dialed.
	d, _, dialed = fakeDialer([]string{"10.32.0.10", "100.64.3.4", "fd00::1"}, nil)
	if _, err := d.DialContext(context.Background(), "rebind.example.com", "587"); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("want ErrNotPublic, got %v", err)
	}
	if len(*dialed) != 0 {
		t.Fatalf("dialed %v", *dialed)
	}

	// A lookup failure looks exactly like a non-public answer, and never
	// carries the resolver's own address.
	d, _, _ = fakeDialer(nil, &net.DNSError{Err: "no such host", Name: "nx.example.com.", Server: "10.32.0.10:53", IsNotFound: true})
	_, err = d.DialContext(context.Background(), "nx.example.com", "587")
	if !errors.Is(err, ErrNotPublic) || strings.Contains(err.Error(), "10.32.0.10") {
		t.Fatalf("lookup failure: %v", err)
	}
}

// With the real dialer, a local listener is never connected to.
func TestDialPublicNeverConnectsToLocal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	for _, host := range []string{"127.0.0.1", "localhost", "localhost.", "[::1]", "::1"} {
		if _, err := DialPublic(context.Background(), host, port); !errors.Is(err, ErrNotPublic) {
			t.Errorf("%s: want ErrNotPublic, got %v", host, err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Fatalf("connected to the local listener %d times", n)
	}
}
