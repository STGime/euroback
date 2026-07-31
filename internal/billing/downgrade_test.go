package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeMailer records the last mail sent so tests can assert on
// subject + recipient + body substrings. Optionally returns an
// error to exercise the "mail failed but downgrade committed"
// path.
type fakeMailer struct {
	lastTo      string
	lastSubject string
	lastBody    string
	err         error
	calls       int
}

func (f *fakeMailer) SendRaw(ctx context.Context, to, subject, body string) error {
	f.calls++
	f.lastTo = to
	f.lastSubject = subject
	f.lastBody = body
	return f.err
}

// TestDowngradeConstants pins the two grace values that end up in
// user-visible outcomes so a copy-paste mistake shows up as a
// failing test rather than a "why did I get downgraded early?"
// support ticket.
func TestDowngradeConstants(t *testing.T) {
	if pastDueGracePeriod != 7*24*time.Hour {
		t.Errorf("pastDueGracePeriod = %v, want 7 days (matches CLAUDE.md + plan doc)", pastDueGracePeriod)
	}
	if downgradeInterval != 1*time.Hour {
		t.Errorf("downgradeInterval = %v, want 1h (matches existing audit_retention / dsar cleanup cadence)", downgradeInterval)
	}
}

// TestSendDowngradeMail_ReasonBranches verifies both reason
// labels produce a mail with the right explanation text —
// visible to the user, so a swap between the two would be a
// confusing bug.
func TestSendDowngradeMail_ReasonBranches(t *testing.T) {
	tests := []struct {
		reason      string
		wantSubstr  string
	}{
		{"past_due_grace_elapsed", "payment failed"},
		{"legacy_pro_grace_elapsed", "closed-beta Pro project"},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			m := &fakeMailer{}
			svc := &DowngradeService{mailer: m}
			c := downgradeCandidate{
				ProjectID:   "proj_x",
				ProjectName: "MyProject",
				OwnerEmail:  "owner@example.com",
			}
			svc.sendDowngradeMail(context.Background(), c, tt.reason)

			if m.calls != 1 {
				t.Fatalf("mail call count = %d, want 1", m.calls)
			}
			if m.lastTo != "owner@example.com" {
				t.Errorf("recipient = %q", m.lastTo)
			}
			if !contains(m.lastBody, tt.wantSubstr) {
				t.Errorf("body missing %q — got:\n%s", tt.wantSubstr, m.lastBody)
			}
			if !contains(m.lastBody, "MyProject") {
				t.Error("body missing project name")
			}
			// Estonian entity footer must appear on every
			// platform-sent mail (compliance requirement — see
			// legalStrings).
			if !contains(m.lastBody, "Eurobase O") {
				t.Error("body missing Estonian entity footer")
			}
			if !contains(m.lastBody, "Ahtri 12") {
				t.Error("body missing registered address")
			}
		})
	}
}

// TestSendDowngradeMail_NilMailerIsNoop guards against a
// crash-on-nil in dev environments booted without TEM creds.
// The downgrade must still complete (the DB flip happens before
// this method); the mail send is best-effort.
func TestSendDowngradeMail_NilMailerIsNoop(t *testing.T) {
	svc := &DowngradeService{mailer: nil}
	// Should not panic; should not do anything.
	svc.sendDowngradeMail(context.Background(), downgradeCandidate{
		ProjectID: "p", OwnerEmail: "x@example.com",
	}, "past_due_grace_elapsed")
}

// TestSendDowngradeMail_MailerErrorDoesNotPanic covers the
// documented "mail failure is best-effort" path — a TEM 500
// while sending must not crash the sweep loop.
func TestSendDowngradeMail_MailerErrorDoesNotPanic(t *testing.T) {
	m := &fakeMailer{err: errors.New("TEM 500")}
	svc := &DowngradeService{mailer: m}
	// Should not panic and should not propagate the error
	// (sendDowngradeMail has no return value by design).
	svc.sendDowngradeMail(context.Background(), downgradeCandidate{
		ProjectID: "p", ProjectName: "X", OwnerEmail: "x@example.com",
	}, "past_due_grace_elapsed")
	if m.calls != 1 {
		t.Fatal("mailer should still have been invoked")
	}
}

// TestSendDowngradeMail_EmptyOwnerEmailIsNoop guards against the
// edge case where a project has been orphaned (owner deleted).
// The mail is skipped; the downgrade still needs to happen.
func TestSendDowngradeMail_EmptyOwnerEmailIsNoop(t *testing.T) {
	m := &fakeMailer{}
	svc := &DowngradeService{mailer: m}
	svc.sendDowngradeMail(context.Background(), downgradeCandidate{
		ProjectID: "p", ProjectName: "X", OwnerEmail: "",
	}, "past_due_grace_elapsed")
	if m.calls != 0 {
		t.Errorf("mail was sent to empty recipient (%d calls)", m.calls)
	}
}

// contains is a case-sensitive substring helper. Kept package-
// local so tests don't have to import strings for one function.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
