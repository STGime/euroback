package email

import (
	"strings"
	"testing"
)

// TestRenderPlatformTemplate_PasskeysCleared pins the security notice a
// password reset sends when it removed the account's passkeys (#621).
func TestRenderPlatformTemplate_PasskeysCleared(t *testing.T) {
	subject, body, err := RenderPlatformTemplate("passkeys_cleared", TemplateData{
		UserEmail: "alice@example.eu",
		ActionURL: "https://console.eurobase.app/account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subject, "passkeys were removed") {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"alice@example.eu", "https://console.eurobase.app/account", "all passkeys on your account were removed", "did not reset your password"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

// Platform notices must never show up in a tenant's customisable
// email-template list.
func TestPasskeysClearedNotInTenantTemplates(t *testing.T) {
	if _, ok := DefaultTemplates()["passkeys_cleared"]; ok {
		t.Fatal("passkeys_cleared leaked into tenant DefaultTemplates")
	}
}
