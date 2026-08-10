package export

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// #355: severity mapping is a small allow-list; a change to the
// list is a behaviour change SIEM operators feel (their notice
// alerts change), so pin it here.
func TestSeverityFor(t *testing.T) {
	cases := []struct {
		action string
		want   int
	}{
		{"project.created", syslogSeverityInfo},
		{"user.login", syslogSeverityInfo},
		{"api_key.created", syslogSeverityNotice},
		{"api_key.regenerated", syslogSeverityNotice},
		{"retention_hold.placed", syslogSeverityNotice},
		{"retention_hold.expired", syslogSeverityNotice},
		{"staff_secrecy.declaration_added", syslogSeverityNotice},
		{"legal_team_beta.granted", syslogSeverityNotice},
		{"storage_retention_policy.set", syslogSeverityNotice},
		{"audit_export_destination.created", syslogSeverityNotice},
		{"project.deleted", syslogSeverityNotice},
		{"platform_user.deleted", syslogSeverityNotice},
		// Similar-but-not-prefix-matching → info.
		{"project.updated", syslogSeverityInfo},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			if got := severityFor(tc.action); got != tc.want {
				t.Errorf("severityFor(%q) = %d, want %d", tc.action, got, tc.want)
			}
		})
	}
}

// #355: SD-DATA value escaping per RFC 5424 §6.3.3. Backslash
// before ", \, ] — nothing else. UTF-8 passes through. Getting
// this wrong lets a crafted metadata value break the parser at
// the sink side.
func TestEscapeSDValue(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`plain`, `plain`},
		{`with "quote"`, `with \"quote\"`},
		{`back\slash`, `back\\slash`},
		{`right]bracket`, `right\]bracket`},
		{`all three "\]`, `all three \"\\\]`},
		{"unicode → arrow", "unicode → arrow"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := escapeSDValue(tc.in); got != tc.want {
				t.Errorf("escapeSDValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// #355: end-to-end format check. Verifies the wire shape a SIEM
// receiver actually sees — priority, timestamp, hostname, structured
// data — matches the docs. Pins EurobasePEN placeholder into the
// message so a real-PEN switch doesn't silently accept.
func TestFormatSyslog_ShapeMatchesDocs(t *testing.T) {
	// Freeze EurobasePEN to a known value; restore after so
	// SYSLOG_EURO_PEN env doesn't leak between test runs.
	prev := EurobasePEN
	EurobasePEN = "99999"
	defer func() { EurobasePEN = prev }()

	ev := SyslogEvent{
		ID:        "abc-123",
		ProjectID: "proj-1",
		ActorID:   "actor-1",
		Action:    "project.deleted",
		RowHash:   "deadbeef",
		Seq:       42,
		CreatedAt: time.Date(2026, 8, 10, 12, 34, 56, 0, time.UTC),
	}
	got := FormatSyslog(ev, "test-host")

	// Priority: local0(16)*8 + notice(5) = 133 (project.deleted
	// is in the notice allow-list).
	wantPri := strconv.Itoa(syslogFacilityLocal0*8 + syslogSeverityNotice)
	if !strings.HasPrefix(got, "<"+wantPri+">1 ") {
		t.Errorf("wrong PRI prefix; got %q, want <%s>1 …", got, wantPri)
	}
	for _, needle := range []string{
		"2026-08-10T12:34:56Z",
		" test-host ",
		" eurobase ",
		" 42 project.deleted ",
		`[eurobase@99999`,
		`project_id="proj-1"`,
		`actor_id="actor-1"`,
		`action="project.deleted"`,
		`row_hash="deadbeef"`,
		`seq="42"`,
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("missing %q in\n  %s", needle, got)
		}
	}
	// End-of-message MSG is empty ('-') — SIEMs that split on the
	// last space rely on this shape.
	if !strings.HasSuffix(got, "] -") {
		t.Errorf("expected trailing '] -' MSG separator; got %q", got)
	}
}

// #355: an info-severity event → PRI=134 = local0(16)*8 + info(6).
// Guards against a future edit that flips the default severity.
func TestFormatSyslog_InfoSeverity(t *testing.T) {
	ev := SyslogEvent{Action: "project.created", CreatedAt: time.Now().UTC().Truncate(time.Second)}
	got := FormatSyslog(ev, "")
	wantPri := strconv.Itoa(syslogFacilityLocal0*8 + syslogSeverityInfo)
	if !strings.HasPrefix(got, "<"+wantPri+">1 ") {
		t.Errorf("info-severity PRI wrong; got %q, want <%s>…", got, wantPri)
	}
}

// #355: octet-counting framer produces "<len> <msg>" — verify the
// length is BYTE-length of the message (not rune count), which
// matters for multibyte SD values.
func TestFrameOctetCounting(t *testing.T) {
	msg := "hello"
	got := string(FrameOctetCounting(msg))
	if got != "5 hello" {
		t.Errorf("simple frame: got %q, want %q", got, "5 hello")
	}

	// Multibyte UTF-8 → byte count > rune count.
	msgMB := "→arrow" // → is 3 bytes UTF-8
	frame := string(FrameOctetCounting(msgMB))
	wantLen := strconv.Itoa(len(msgMB)) // 3 + 5 = 8
	if !strings.HasPrefix(frame, wantLen+" ") {
		t.Errorf("multibyte frame: got %q, want prefix %q", frame, wantLen+" ")
	}
	if !strings.HasSuffix(frame, msgMB) {
		t.Errorf("frame body should be the original message; got %q", frame)
	}
}

// #355: empty batch is a no-op — no dial, no error, no cursor.
// This defends against a future refactor that adds a spurious dial
// when the query returned zero rows.
func TestSyslogDeliverer_EmptyBatchNoDial(t *testing.T) {
	d := NewSyslogDeliverer()
	// Endpoint intentionally bogus — if the empty-events guard
	// were removed, tls.Dial would fail against this and the test
	// would notice.
	cursor, err := d.DialAndSend(t.Context(), "localhost.invalid:6514", nil, nil)
	if err != nil {
		t.Errorf("empty batch should not error; got %v", err)
	}
	if cursor != 0 {
		t.Errorf("empty batch cursor should be 0; got %d", cursor)
	}
}

// #359 review blocker: partial-batch failure MUST return (0, err),
// not (maxSeqSuccessfullyWritten, err). Rationale is in DialAndSend's
// header: syslog over TLS has no application ACK, so a successful
// conn.Write only means bytes are in the local kernel send buffer,
// NOT that the peer received them. If the connection breaks (RST),
// the un-acked buffered bytes are discarded — advancing the cursor
// past them silently drops audit events.
//
// Test drives sendFrames directly against a net.Pipe whose far end
// closes after reading one frame's worth of bytes. The client's
// second Write then fails; we assert the returned cursor is 0. If
// the fix regressed to `return maxSeqSuccessfullyWritten, err`, the
// returned cursor would be events[0].Seq (100) and this test would
// fail — pinning the actual write-loop contract.
//
// Uses sendFrames (not DialAndSend) so the loopback SSRF rejection
// isn't the exit branch. The end-to-end SSRF check is exercised by
// the TestSSRFSafeClient_* suite; here we care about the (0, err)
// return on write failure specifically.
func TestSendFrames_PartialFailureHoldsCursor(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()

	// Server side: read a small chunk (one frame's bytes) then
	// slam the connection shut. That guarantees the client's
	// first Write succeeds (into net.Pipe's buffer, read by the
	// goroutine below) and its second Write fails with a broken-
	// pipe error.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 200)
		_, _ = serverEnd.Read(buf)
		_ = serverEnd.Close()
	}()

	d := &SyslogDeliverer{
		WriteTimeout: 500 * time.Millisecond,
	}
	events := []SyslogEvent{
		{ID: "e1", Action: "test.event", CreatedAt: time.Now().UTC().Truncate(time.Second), Seq: 100},
		{ID: "e2", Action: "test.event", CreatedAt: time.Now().UTC().Truncate(time.Second), Seq: 200},
	}
	cursor, err := d.sendFrames(t.Context(), clientEnd, events)
	<-done

	if err == nil {
		t.Fatal("expected write error when server closes mid-batch, got nil")
	}
	if cursor != 0 {
		t.Errorf("partial-batch failure MUST return cursor=0 (whole batch re-ships next attempt); got cursor=%d — this silently drops audit events",
			cursor)
	}
	_ = clientEnd.Close()
}

// Positive counterpart: sendFrames returns (batchMaxSeq, nil) when
// every event writes cleanly. Pins the success-case contract so a
// future revert can't turn the partial-failure test's (0, err)
// assertion into an "always returns 0" false-positive.
func TestSendFrames_FullSuccessReturnsMaxSeq(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Drain everything the client writes so no Write blocks.
		buf := make([]byte, 4096)
		for {
			if _, err := serverEnd.Read(buf); err != nil {
				return
			}
		}
	}()

	d := &SyslogDeliverer{WriteTimeout: 500 * time.Millisecond}
	events := []SyslogEvent{
		{ID: "e1", Action: "test.event", CreatedAt: time.Now().UTC().Truncate(time.Second), Seq: 100},
		{ID: "e2", Action: "test.event", CreatedAt: time.Now().UTC().Truncate(time.Second), Seq: 200},
		{ID: "e3", Action: "test.event", CreatedAt: time.Now().UTC().Truncate(time.Second), Seq: 300},
	}
	cursor, err := d.sendFrames(t.Context(), clientEnd, events)
	if err != nil {
		t.Fatalf("full-batch success expected, got err=%v", err)
	}
	if cursor != 300 {
		t.Errorf("full-batch success cursor should be max(seq)=300, got %d", cursor)
	}
	_ = clientEnd.Close()
	<-done
}
