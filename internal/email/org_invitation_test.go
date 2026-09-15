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
