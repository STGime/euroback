package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckAddr(t *testing.T) {
	refused := []string{
		"127.0.0.1", "10.32.0.10", "172.16.5.4", "192.168.1.1", "169.254.42.42", "169.254.169.254",
		"100.64.0.1", "100.127.255.254", "0.0.0.0", "0.1.2.3", "224.0.0.1", "255.255.255.255", "240.1.1.1",
		"192.0.2.10", "198.18.0.1", "::1", "::", "fe80::1", "fd00::1", "ff02::1", "2001:db8::1",
		"::ffff:10.0.0.1", // IPv4-mapped
		"64:ff9b::a00:1",  // NAT64 → 10.0.0.1
		"2002:a00:1::1",   // 6to4 → 10.0.0.1
		"64:ff9b::7f00:1", // NAT64 → 127.0.0.1
	}
	for _, s := range refused {
		if CheckAddr(netip.MustParseAddr(s)) == nil {
			t.Errorf("%s: accepted, want refused", s)
		}
	}
	allowed := []string{"51.15.217.235", "167.235.125.60", "2a01:4f8::1", "64:ff9b::33f:d9eb", "100.128.0.1"}
	for _, s := range allowed {
		if err := CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: refused (%v), want accepted", s, err)
		}
	}
}

func TestCheckHostName(t *testing.T) {
	for _, h := range []string{"", "localhost", "LOCALHOST.", "pgbouncer", "gateway.eurobase.svc.cluster.local",
		"db.eurobase.svc", "printer.local", "x.internal", "127.0.0.1", "[::1]", "10.0.0.1"} {
		if CheckHostName(h) == nil {
			t.Errorf("%q: accepted, want refused", h)
		}
	}
	for _, h := range []string{"smtp.example.com", "www783.your-server.de", "mail.perweg.de.", "51.15.217.235"} {
		if err := CheckHostName(h); err != nil {
			t.Errorf("%q: refused (%v), want accepted", h, err)
		}
	}
}

// A local listener is never connected to, by literal or by name.
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
	d := &net.Dialer{Timeout: time.Second}
	for _, host := range []string{"127.0.0.1", "localhost", "localhost.", "[::1]"} {
		if _, err := DialPublic(context.Background(), d, host, port); !errors.Is(err, ErrNotPublic) {
			t.Errorf("%s: want ErrNotPublic, got %v", host, err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Fatalf("connected to the local listener %d times", n)
	}
}
