package email

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseFailedEmails(t *testing.T) {
	long := strings.Repeat("é", 400) // multi-byte: the cut must stay valid UTF-8
	body := []byte(`{"total_count":6,"emails":[
		{"mail_rcpt":" User@Dead.example.org ","status":"failed","status_details":"dropped","created_at":"2026-10-30T00:58:59Z","last_tries":[{"rank":1,"code":-1,"message":"send email to dead.example.org: non-existing domain or no MX found"}]},
		{"mail_rcpt":"gone@live.org","status":"failed","status_details":"dropped","flags":["mailbox_not_found","hard_bounce"],"created_at":"2026-10-30T01:00:00Z","last_tries":[{"message":"550 mailbox not found"}]},
		{"mail_rcpt":"long@live.org","status":"failed","status_details":"dropped","flags":["hard_bounce"],"created_at":"2026-10-30T01:00:00Z","last_tries":[{"message":"` + long + `"}]},
		{"mail_rcpt":"flaky@live.org","status":"failed","status_details":"dropped","created_at":"2026-10-30T01:00:00Z","last_tries":[{"message":"no reachable MX found: giving up after trying 1 hosts"}]},
		{"mail_rcpt":"ok@live.org","status":"sent","created_at":"2026-10-30T01:00:00Z"},
		{"mail_rcpt":"","status":"failed","flags":["hard_bounce"],"created_at":"2026-10-30T01:00:00Z"}
	]}`)
	got, n, err := parseFailedEmailsPage(body)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("page length = %d, want 6 (before filtering, for paging)", n)
	}
	byRcpt := map[string]FailedEmail{}
	for _, f := range got {
		byRcpt[f.Recipient] = f
	}
	if len(got) != 3 {
		t.Fatalf("kept %d, want 3 (non-existing domain, two flagged): %+v", len(got), got)
	}
	if _, ok := byRcpt["flaky@live.org"]; ok {
		t.Fatal("an unflagged, possibly temporary failure must not be kept")
	}
	if f := byRcpt["gone@live.org"]; f.Reason != "dropped [mailbox_not_found,hard_bounce]: 550 mailbox not found" {
		t.Fatalf("reason = %q", f.Reason)
	}
	if f := byRcpt["long@live.org"]; utf8.RuneCountInString(f.Reason) > 300 || !utf8.ValidString(f.Reason) {
		t.Fatalf("long reason not cut cleanly: %d runes, valid=%v", utf8.RuneCountInString(f.Reason), utf8.ValidString(f.Reason))
	}
	if _, ok := byRcpt["user@dead.example.org"]; !ok {
		t.Fatal("a non-existing domain must be kept (recipient lowercased and trimmed)")
	}
	if _, err := parseFailedEmails([]byte(`not json`)); err == nil {
		t.Fatal("bad JSON must error")
	}
}
