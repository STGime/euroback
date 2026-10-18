package email

import (
	"strings"
	"testing"
)

// #822: the end-user "password changed" notice renders from the default
// template with the project's name, has no link, and a custom template
// replaces it.
func TestRenderTemplate_PasswordChanged(t *testing.T) {
	subject, body, err := RenderTemplate("password_changed", "", "", TemplateData{UserEmail: "ann@example.com", ProjectName: "Innebandy <Club>"})
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Your password has been changed" {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(body, "Innebandy &lt;Club&gt;") || strings.Contains(body, "<Club>") {
		t.Errorf("project name must be in the body, HTML-escaped: %s", body)
	}
	if strings.Contains(body, "href=") {
		t.Error("the notice carries no link")
	}

	subject, body, err = RenderTemplate("password_changed", "Password changed for {{.ProjectName}}", "<p>Hi {{.UserEmail}}</p>", TemplateData{UserEmail: "ann@example.com", ProjectName: "Innebandy"})
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Password changed for Innebandy" || !strings.Contains(body, "Hi ann@example.com") {
		t.Errorf("custom template: %q %q", subject, body)
	}
}
