package email

import "testing"

func TestParseFailedEmails(t *testing.T) {
	body := []byte(`{"total_count":3,"emails":[
		{"mail_rcpt":" User@Dead.example.org ","status":"failed","status_details":"dropped","created_at":"2026-10-30T00:58:59Z","last_tries":[{"rank":1,"code":-1,"message":"non-existing domain or no MX found"}]},
		{"mail_rcpt":"ok@live.org","status":"sent","status_details":"","created_at":"2026-10-30T01:00:00Z"},
		{"mail_rcpt":"","status":"failed","status_details":"dropped","created_at":"2026-10-30T01:00:00Z"}
	]}`)
	got, err := parseFailedEmails(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Recipient != "user@dead.example.org" || got[0].Reason != "dropped: non-existing domain or no MX found" || got[0].At.IsZero() {
		t.Fatalf("unexpected entry: %+v", got[0])
	}
	if _, err := parseFailedEmails([]byte(`not json`)); err == nil {
		t.Fatal("bad JSON must error")
	}
}
