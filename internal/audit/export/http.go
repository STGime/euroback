// Package export implements the audit-log SIEM deliverers.
//
// SSRF discipline (per #356 review + #354 comment): the CRUD layer
// (internal/compliance/audit_destinations.go) rejects internal
// literals at registration time. That's necessary but not sufficient
// — a tenant can register attacker.com whose DNS resolves to
// 10.0.0.1 at delivery time (rebinding). Every dial from this
// package MUST go through NewSSRFSafeClient below, which hooks the
// resolved IP via a custom DialContext and re-applies
// compliance.ValidateHostNotInternal before opening the socket.
package export

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/compliance"
)

// ClientConfig tunes the SSRF-safe HTTP client. Zero-value fields
// fall back to safe defaults inside NewSSRFSafeClient.
type ClientConfig struct {
	// Timeout is the whole-request budget (dial + TLS + body write +
	// response headers + body read). Default 15s — long enough for
	// a slow SIEM sink, short enough that a dead one doesn't wedge
	// the worker.
	Timeout time.Duration

	// DialTimeout is a stricter cap on the connect+TLS handshake
	// alone. Default 5s.
	DialTimeout time.Duration
}

// NewSSRFSafeClient returns an *http.Client that refuses to connect
// to internal targets — regardless of what DNS returned for the
// hostname. Redirects are DISALLOWED (a 302 to http://10.0.0.1
// would sidestep every check).
func NewSSRFSafeClient(cfg ClientConfig) *http.Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}

	baseDialer := &net.Dialer{
		Timeout:   cfg.DialTimeout,
		KeepAlive: 30 * time.Second,
	}

	// Custom DialContext: after Go resolves the hostname, but BEFORE
	// the TCP connect, re-check the resolved IP against the SSRF
	// deny-list. If Go's resolver returns multiple IPs, we walk them
	// in order and only connect to the first non-internal one — if
	// they're all internal, we refuse.
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IPs for %s", host)
		}
		var lastErr error
		for _, ip := range ips {
			if err := compliance.ValidateHostNotInternal(ip.IP.String()); err != nil {
				lastErr = fmt.Errorf("resolved %s → %s: %w", host, ip.IP, err)
				continue
			}
			conn, err := baseDialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}

	transport := &http.Transport{
		DialContext:           dial,
		DialTLSContext:        nil, // let http fall back to DialContext + StartTLS
		TLSHandshakeTimeout:   cfg.DialTimeout,
		ResponseHeaderTimeout: cfg.Timeout,
		DisableKeepAlives:     true, // fresh dial per POST → resolves DNS every time
		ForceAttemptHTTP2:     false,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// A 302 to http://10.0.0.1 would bypass every check
			// because CheckRedirect fires AFTER the resolver would
			// have decided the redirect was fine. Refuse.
			return errors.New("redirects disallowed for SIEM deliverer")
		},
	}
}
