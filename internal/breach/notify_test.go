package breach

import (
	"strings"
	"testing"
	"time"
)

func mkEntry() *Entry {
	occ := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	until := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	aware := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)
	recs := int64(1234)
	subs := int64(98)
	leadSA := "fr-cnil"
	return &Entry{
		IncidentID:         "11111111-2222-3333-4444-555555555555",
		Title:              "Misconfigured S3 ACL exposed thumbnails bucket",
		Description:        "A staging cron rotated bucket ACL to public-read for ~2h. Personal-data thumbnails were enumerable.",
		LikelyConsequences: "Loss of confidentiality of profile photos for affected users.",
		MeasuresTaken:      "ACL reverted, CDN purged, attacker IPs blocked, root-cause cron disabled.",
		DataCategories:     []string{"profile photo", "user id"},
		SubjectCategories:  []string{"end-users of project acme"},
		RecordsAffected:    &recs,
		SubjectsAffected:   &subs,
		OccurredAt:         &occ,
		OccurredUntil:      &until,
		AwarenessAt:        aware,
		LeadSA:             &leadSA,
		Status:             StatusOpen,
		ActorEmail:         "dpo@eurobase.app",
	}
}

func TestRenderCustomerEmail_PopulatesAllSections(t *testing.T) {
	subject, html, err := RenderCustomerEmail(mkEntry(), "dpo@eurobase.app")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(subject, "Personal-data breach notice") {
		t.Errorf("subject missing prefix: %q", subject)
	}
	// Subject must include a recognisable short id, not the full UUID.
	if !strings.Contains(subject, "11111111") {
		t.Errorf("subject missing short id: %q", subject)
	}
	if strings.Contains(subject, "555555555555") {
		t.Errorf("subject leaks full UUID: %q", subject)
	}

	for _, want := range []string{
		"staging cron rotated bucket ACL",                // nature (description)
		"profile photo, user id",                         // data categories joined
		"end-users of project acme",                      // subjects
		"1234",                                           // records
		"98",                                             // subjects count
		"Loss of confidentiality",                        // consequences
		"ACL reverted, CDN purged",                       // measures
		"<a href=\"mailto:dpo@eurobase.app\">",           // DPO link
		"2026-06-01T08:00:00Z — 2026-06-01T10:00:00Z",   // window UTC
		"2026-06-01T11:00:00Z",                           // awareness
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q", want)
		}
	}
}

func TestRenderCustomerEmail_FallbacksWhenFieldsAreEmpty(t *testing.T) {
	e := &Entry{
		IncidentID:  "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Title:       "Unknown",
		AwarenessAt: time.Now().UTC(),
	}
	_, html, err := RenderCustomerEmail(e, "dpo@eurobase.app")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"Details are still being established",
		"under investigation",
		"under assessment",
		"Stop-the-bleeding measures are in place",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing fallback %q", want)
		}
	}
}

func TestRenderAuthorityForm_StructuredArticle33Bullets(t *testing.T) {
	out, err := RenderAuthorityForm(mkEntry(), "dpo@eurobase.app")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"## 1. Nature of the breach",
		"## 2. Data and subjects",
		"## 3. Likely consequences",
		"## 4. Measures taken or proposed",
		"## 5. Contact point",
		"**Lead supervisory authority:** fr-cnil",
		"**Incident reference:** 11111111-2222-3333-4444-555555555555",
		"profile photo, user id",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("authority form missing %q", want)
		}
	}
}

func TestFormatWindow_Permutations(t *testing.T) {
	a := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	b := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		from, until *time.Time
		want        string
	}{
		{nil, nil, "under investigation"},
		{&a, nil, "2026-06-01T08:00:00Z — ongoing"},
		{nil, &b, "unknown — 2026-06-01T10:00:00Z"},
		{&a, &b, "2026-06-01T08:00:00Z — 2026-06-01T10:00:00Z"},
	}
	for _, c := range cases {
		got := formatWindow(c.from, c.until)
		if got != c.want {
			t.Errorf("formatWindow(%v,%v) = %q, want %q", c.from, c.until, got, c.want)
		}
	}
}
