package export

// Syslog deliverer (#355). Formats audit_log events as RFC 5424
// messages and writes them over a TLS TCP connection using an
// octet-counting framer (RFC 6587 §3.4.1) — safer than newline
// framing because audit metadata can legitimately contain '\n'.
//
// Connection lifecycle: short-lived per River job invocation. Job
// dials, writes the batch, closes. River's per-job cadence is 30s,
// which is fine for a compliance stream; keeping a persistent
// connection alive across jobs would fight River's stateless-worker
// model. If throughput ever demands it we can pool at the process
// level, but the current shape trades a small handshake tax for
// simpler failure semantics: any error during a batch fails the
// job (no cursor advance), River retries with backoff, and the
// next attempt starts with a fresh connection.
//
// TLS is mandatory — plaintext syslog over TCP is trivially
// eavesdroppable and audit events name subjects + actions we can't
// leak. #353 destination validation already refuses non-TLS
// endpoints via kind=="syslog" + host:port validation; the dialer
// here re-enforces via tls.Dial only.
//
// SSRF discipline: same as the webhook deliverer. Registration-
// time rejection of literal internal targets is not enough — the
// dial-time IP MUST be re-checked via
// compliance.ValidateHostNotInternal because a hostname can pass
// registration and later resolve to 10.0.0.1 (DNS rebinding).

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/compliance"
)

// SyslogFacility is fixed to local0 by design — an operator can
// route by SD-ID ([eurobase@PEN]) in their SIEM rules rather than
// per-facility. Kept as a named constant so a future PR that adds
// per-tenant configuration has a single place to touch.
const syslogFacilityLocal0 = 16

// Severity codes per RFC 5424 §6.2.1. We only ever emit info (6)
// and notice (5) — audit events are not warnings/errors from the
// platform's POV, they're records.
const (
	syslogSeverityInfo   = 6
	syslogSeverityNotice = 5
)

// EurobasePEN is our IANA Private Enterprise Number placeholder
// slot for SD-ID naming. Real PEN assignment is a paperwork task
// (§ ops runbook). Until then, the placeholder is stable so
// downstream SIEM rules keyed on the SD-ID don't break at
// re-configuration time.
//
// Overrideable via SYSLOG_EURO_PEN so ops can flip to the real
// number without a code push once assigned.
var EurobasePEN = envOr("SYSLOG_EURO_PEN", "99999")

// syslogNoticeActions is the small allow-list of action prefixes
// promoted to notice severity. These are the events a SIEM
// operator would want to alert on individually — the rest go to
// info and their sheer volume is expected. Prefix match, not
// exact, so a future retention_hold.expired lands in notice
// automatically without a code change.
var syslogNoticeActions = []string{
	"retention_hold.",
	"api_key.",
	"staff_secrecy.",
	"legal_team_beta.",
	"audit_export_destination.",
	"storage_retention_policy.",
	"project.deleted",
	"platform_user.deleted",
}

// SyslogEvent is the subset of audit_log data the syslog formatter
// needs. Kept separate from EventRow so the syslog wire format
// isn't coupled to the JSON envelope shape.
type SyslogEvent struct {
	ID        string
	ProjectID string
	ActorID   string
	Action    string
	RowHash   string
	Seq       int64
	CreatedAt time.Time
}

// FormatSyslog renders one event as a complete RFC 5424 message
// (without the octet-counting frame). Hostname is the platform's
// self-identifier (os.Hostname), app-name is fixed "eurobase",
// procid is our seq (unique within the tenant chain), msgid is
// the audit action.
//
// Structured data:
//
//	[eurobase@<PEN> project_id="<uuid>" actor_id="<uuid>"
//	                action="<action>" row_hash="<hex>" seq="<n>"]
//
// Values inside SD-DATA are escaped per RFC 5424 §6.3.3
// (backslash before ", \, ]).
func FormatSyslog(ev SyslogEvent, hostname string) string {
	pri := syslogFacilityLocal0*8 + severityFor(ev.Action)
	ts := ev.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	if ev.CreatedAt.Nanosecond() != 0 {
		// RFC 5424 permits fractional seconds; keep milli precision
		// only if source data actually carried it, so audit_log
		// rows with second-precision created_at don't gain a
		// spurious .000.
		ts = ev.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if hostname == "" {
		hostname = "-"
	}
	if ev.ProjectID == "" {
		ev.ProjectID = "-"
	}
	actor := ev.ActorID
	if actor == "" {
		actor = "-"
	}
	sd := fmt.Sprintf(
		`[eurobase@%s project_id=%q actor_id=%q action=%q row_hash=%q seq=%q]`,
		EurobasePEN,
		escapeSDValue(ev.ProjectID),
		escapeSDValue(actor),
		escapeSDValue(ev.Action),
		escapeSDValue(ev.RowHash),
		strconv.FormatInt(ev.Seq, 10),
	)
	// PRI VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID SD MSG
	//   MSG is empty (we put the semantic content into SD).
	return fmt.Sprintf("<%d>1 %s %s eurobase %d %s %s -", pri, ts, hostname, ev.Seq, ev.Action, sd)
}

// severityFor maps an action to syslog severity by prefix
// allow-list. Everything else stays at info — the whole point of
// notice is to be a sparse, meaningful signal.
func severityFor(action string) int {
	for _, prefix := range syslogNoticeActions {
		if strings.HasPrefix(action, prefix) {
			return syslogSeverityNotice
		}
	}
	return syslogSeverityInfo
}

// escapeSDValue applies RFC 5424 §6.3.3 escaping: '"', '\', ']'
// each get a leading backslash. Nothing else; UTF-8 passes
// through.
func escapeSDValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' || c == ']' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// FrameOctetCounting wraps one RFC 5424 message with the
// RFC 6587 §3.4.1 octet-counting frame: "<len> <msg>". Safer
// than newline framing because audit metadata may legitimately
// contain '\n' inside SD values.
func FrameOctetCounting(msg string) []byte {
	// FormatSyslog + framer share one allocation.
	prefix := strconv.Itoa(len(msg)) + " "
	out := make([]byte, 0, len(prefix)+len(msg))
	out = append(out, prefix...)
	out = append(out, msg...)
	return out
}

// SyslogDeliverer is the reusable transport shim. Stateless — one
// instance per worker process is fine.
type SyslogDeliverer struct {
	// TLSConfig is a *shared* base config. Per-destination the
	// worker clones and sets ServerName + certificate. Nil is
	// legal — Dial derives a config from the hostname.
	TLSConfig *tls.Config

	// DialTimeout caps the TCP+TLS handshake. Default 5s.
	DialTimeout time.Duration
	// WriteTimeout caps each Write call — a hung sink shouldn't
	// wedge the worker. Default 10s per write.
	WriteTimeout time.Duration
}

// NewSyslogDeliverer constructs a deliverer with safe defaults.
// clientCert is optional — if non-nil, the TLS handshake presents
// it (mutual TLS to the sink). The cert/key material is resolved
// from the tenant vault by the caller; this constructor only
// wires it into the config.
func NewSyslogDeliverer() *SyslogDeliverer {
	return &SyslogDeliverer{
		DialTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
}

// DialAndSend opens a TLS connection to endpoint (host:port),
// writes each event as an octet-counted RFC 5424 frame, and
// closes. Returns the max seq successfully written — the caller
// uses this to CAS-advance the cursor.
//
// SSRF: net.SplitHostPort → LookupIPAddr → ValidateHostNotInternal
// on each resolved IP → tls.Dial to the first non-internal one.
// If all resolved IPs are internal, refuses with an SSRF error.
//
// Failure inside the batch loop: the returned cursor is the LAST
// SUCCESSFULLY-WRITTEN seq (may be 0 if the first write failed),
// paired with the error. Caller advances cursor to that value and
// returns the error so River retries — the next attempt picks up
// where this one stopped, no re-delivery.
func (d *SyslogDeliverer) DialAndSend(ctx context.Context, endpoint string, clientCert *tls.Certificate, events []SyslogEvent) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return 0, fmt.Errorf("split host:port: %w", err)
	}

	// Resolve once, validate each result before considering the
	// connect. Same shape as the webhook deliverer's DialContext.
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return 0, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return 0, fmt.Errorf("no IPs for %s", host)
	}

	tlsCfg := d.tlsConfigFor(host, clientCert)

	var (
		conn    *tls.Conn
		lastErr error
	)
	for _, ip := range ips {
		if err := compliance.ValidateHostNotInternal(ip.IP.String()); err != nil {
			lastErr = fmt.Errorf("resolved %s → %s: %w", host, ip.IP, err)
			continue
		}
		dialCtx, cancel := context.WithTimeout(ctx, d.DialTimeout)
		dialer := &net.Dialer{}
		rawConn, dErr := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(ip.IP.String(), port))
		cancel()
		if dErr != nil {
			lastErr = dErr
			continue
		}
		tlsConn := tls.Client(rawConn, tlsCfg)
		hsCtx, hsCancel := context.WithTimeout(ctx, d.DialTimeout)
		if err := tlsConn.HandshakeContext(hsCtx); err != nil {
			hsCancel()
			_ = tlsConn.Close()
			lastErr = err
			continue
		}
		hsCancel()
		conn = tlsConn
		break
	}
	if conn == nil {
		if lastErr == nil {
			lastErr = errors.New("no usable IP for endpoint (all resolved addresses rejected by SSRF policy)")
		}
		return 0, lastErr
	}
	defer conn.Close()

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "eurobase-worker"
	}

	var maxSeqDelivered int64
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return maxSeqDelivered, err
		}
		frame := FrameOctetCounting(FormatSyslog(ev, hostname))
		if err := conn.SetWriteDeadline(time.Now().Add(d.WriteTimeout)); err != nil {
			return maxSeqDelivered, fmt.Errorf("set write deadline: %w", err)
		}
		if _, err := conn.Write(frame); err != nil {
			// Partial-batch failure: the seq we already wrote is
			// safely on the sink (TCP + TLS = ordered, so any
			// bytes we successfully wrote reached the peer's
			// syslog receiver). Return the max we got through
			// so the caller can advance the cursor to it.
			return maxSeqDelivered, fmt.Errorf("write event seq=%d: %w", ev.Seq, err)
		}
		if ev.Seq > maxSeqDelivered {
			maxSeqDelivered = ev.Seq
		}
	}
	return maxSeqDelivered, nil
}

// tlsConfigFor clones the base TLS config (never mutates the
// shared one) and pins ServerName + client cert per call.
func (d *SyslogDeliverer) tlsConfigFor(host string, clientCert *tls.Certificate) *tls.Config {
	var cfg *tls.Config
	if d.TLSConfig != nil {
		cfg = d.TLSConfig.Clone()
	} else {
		cfg = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
	cfg.ServerName = host
	if clientCert != nil {
		cfg.Certificates = []tls.Certificate{*clientCert}
	}
	return cfg
}

// envOr returns os.Getenv(k) or fallback if unset/empty.
func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
