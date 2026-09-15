package email

import (
	"strings"
	"testing"
)

// TestRenderTemplate_OrgInvitation pins the org_invitation template
// output shape so a future edit to the HTML body / subject doesn't
// silently drop the org name or inviter email that customers rely
// on to recognise the invitation. Cheap unit-level (no DB, no SMTP).
func TestRenderTemplate_OrgInvitation(t *testing.T) {
	subject, body, err := RenderTemplate("org_invitation", "", "", TemplateData{
		UserEmail:    "invitee@example.com",
		ProjectName:  "Eurobase Console",
		ActionURL:    "https://console.eurobase.app/login",
		OrgName:      "Acme Corp",
		InviterEmail: "admin@acme.com",
	})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}

	if want := "You've been added to Acme Corp on Eurobase"; subject != want {
		t.Errorf("subject: got %q, want %q", subject, want)
	}

	// Body must name the inviter, the org, and the action URL —
	// missing any of them = a broken invitation from the invitee's
	// perspective.
	for _, want := range []string{
		"admin@acme.com",
		"Acme Corp",
		"https://console.eurobase.app/login",
		"Sign in to Eurobase",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nfull body:\n%s", want, body)
		}
	}
}

// TestRenderTemplate_OrgInvitation_SubjectNotHTMLEscaped pins the
// #584 review 🟡 fix: an org name with ampersand / apostrophe / angle
// brackets must appear verbatim in the plain-text Subject: header,
// not as HTML entities. Before splitting subject rendering onto
// text/template, "Ben & Jerry's Bakery" landed in the inbox as
// "Ben &amp; Jerry&#39;s Bakery".
func TestRenderTemplate_OrgInvitation_SubjectNotHTMLEscaped(t *testing.T) {
	orgName := "Ben & Jerry's Bakery"
	subject, body, err := RenderTemplate("org_invitation", "", "", TemplateData{
		UserEmail:    "invitee@example.com",
		ProjectName:  "Eurobase Console",
		ActionURL:    "https://console.eurobase.app/login",
		OrgName:      orgName,
		InviterEmail: "admin@bnj.com",
	})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}

	if want := "You've been added to Ben & Jerry's Bakery on Eurobase"; subject != want {
		t.Errorf("subject: got %q, want %q", subject, want)
	}
	// Belt-and-braces — enumerate the entities that should NOT
	// appear in a plain-text subject.
	for _, entity := range []string{"&amp;", "&#39;", "&#34;", "&lt;", "&gt;"} {
		if strings.Contains(subject, entity) {
			t.Errorf("subject contains HTML entity %q: %q", entity, subject)
		}
	}

	// Body is HTML and MUST still escape (injection safety) — the
	// literal `&` from "Ben & Jerry's" must render as `&amp;` in
	// the HTML source so the browser shows one ampersand and no
	// XSS surface opens if OrgName ever contained "<script>…".
	if !strings.Contains(body, "Ben &amp; Jerry&#39;s Bakery") {
		t.Errorf("body should HTML-escape the org name; body:\n%s", body)
	}
}

// TestRenderTemplate_OrgInvitation_BodyEscapesInjection guards the
// design property called out in the review: the HTML body renders
// through html/template so a malicious org name can't inject script.
func TestRenderTemplate_OrgInvitation_BodyEscapesInjection(t *testing.T) {
	_, body, err := RenderTemplate("org_invitation", "", "", TemplateData{
		UserEmail:    "invitee@example.com",
		ProjectName:  "Eurobase Console",
		ActionURL:    "https://console.eurobase.app/login",
		OrgName:      `<script>alert(1)</script>`,
		InviterEmail: `admin@x.com`,
	})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("body must NOT contain raw <script> — html/template escaping regressed. body:\n%s", body)
	}
}
