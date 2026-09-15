package email

import (
	"bytes"
	"fmt"
	"html/template"
)

// TemplateData holds the variables available in email templates.
type TemplateData struct {
	UserEmail   string
	ProjectName string
	ActionURL   string
	ExpiresIn   string
	// OrgName / InviterEmail are only populated by the org_invitation
	// template today. Other templates leave them empty and don't
	// reference them; keeping them on the shared struct avoids a
	// per-template data-shape sprawl.
	OrgName      string
	InviterEmail string
}

// DefaultTemplate holds the default subject and HTML body for a template type.
type DefaultTemplate struct {
	Subject  string `json:"subject"`
	BodyHTML string `json:"body_html"`
}

const baseLayout = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;padding:0;background:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif">
<table width="100%%" cellpadding="0" cellspacing="0" style="background:#f4f4f5;padding:40px 0">
<tr><td align="center">
<table width="560" cellpadding="0" cellspacing="0" style="background:#ffffff;border-radius:8px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1)">
<tr><td style="background:#1e3a5f;padding:24px 32px">
<h1 style="margin:0;color:#ffffff;font-size:20px;font-weight:600">%s</h1>
</td></tr>
<tr><td style="padding:32px">%s</td></tr>
<tr><td style="padding:16px 32px 24px;border-top:1px solid #e4e4e7;color:#71717a;font-size:12px;text-align:center">
Sent by Eurobase &mdash; EU-Sovereign Backend-as-a-Service
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

var defaultTemplates = map[string]DefaultTemplate{
	"verification": {
		Subject: "Verify your email address",
		BodyHTML: fmt.Sprintf(baseLayout,
			"{{.ProjectName}}",
			`<p style="margin:0 0 16px;color:#18181b;font-size:16px">Hi,</p>
<p style="margin:0 0 24px;color:#3f3f46;font-size:14px;line-height:1.6">Please verify your email address to complete your registration with <strong>{{.ProjectName}}</strong>.</p>
<p style="margin:0 0 24px;text-align:center">
<a href="{{.ActionURL}}" style="display:inline-block;background:#1e3a5f;color:#ffffff;text-decoration:none;padding:12px 32px;border-radius:6px;font-size:14px;font-weight:600">Verify Email</a>
</p>
<p style="margin:0;color:#71717a;font-size:12px">This link expires in {{.ExpiresIn}}. If you didn't create this account, you can safely ignore this email.</p>`),
	},
	"password_reset": {
		Subject: "Reset your password",
		BodyHTML: fmt.Sprintf(baseLayout,
			"{{.ProjectName}}",
			`<p style="margin:0 0 16px;color:#18181b;font-size:16px">Hi,</p>
<p style="margin:0 0 24px;color:#3f3f46;font-size:14px;line-height:1.6">We received a request to reset your password for <strong>{{.ProjectName}}</strong>.</p>
<p style="margin:0 0 24px;text-align:center">
<a href="{{.ActionURL}}" style="display:inline-block;background:#1e3a5f;color:#ffffff;text-decoration:none;padding:12px 32px;border-radius:6px;font-size:14px;font-weight:600">Reset Password</a>
</p>
<p style="margin:0;color:#71717a;font-size:12px">This link expires in {{.ExpiresIn}}. If you didn't request this, you can safely ignore this email.</p>`),
	},
	"welcome": {
		Subject: "Welcome to {{.ProjectName}}",
		BodyHTML: fmt.Sprintf(baseLayout,
			"{{.ProjectName}}",
			`<p style="margin:0 0 16px;color:#18181b;font-size:16px">Welcome!</p>
<p style="margin:0 0 24px;color:#3f3f46;font-size:14px;line-height:1.6">Your account with <strong>{{.ProjectName}}</strong> has been verified. You're all set to get started.</p>`),
	},
	"password_changed": {
		Subject: "Your password has been changed",
		BodyHTML: fmt.Sprintf(baseLayout,
			"{{.ProjectName}}",
			`<p style="margin:0 0 16px;color:#18181b;font-size:16px">Hi,</p>
<p style="margin:0 0 24px;color:#3f3f46;font-size:14px;line-height:1.6">Your password for <strong>{{.ProjectName}}</strong> was successfully changed. If you didn't make this change, please contact support immediately.</p>`),
	},
	"magic_link": {
		Subject: "Sign in to {{.ProjectName}}",
		BodyHTML: fmt.Sprintf(baseLayout,
			"{{.ProjectName}}",
			`<p style="margin:0 0 16px;color:#18181b;font-size:16px">Hi,</p>
<p style="margin:0 0 24px;color:#3f3f46;font-size:14px;line-height:1.6">Click the button below to sign in to <strong>{{.ProjectName}}</strong>. No password needed.</p>
<p style="margin:0 0 24px;text-align:center">
<a href="{{.ActionURL}}" style="display:inline-block;background:#1e3a5f;color:#ffffff;text-decoration:none;padding:12px 32px;border-radius:6px;font-size:14px;font-weight:600">Sign In</a>
</p>
<p style="margin:0;color:#71717a;font-size:12px">This link expires in {{.ExpiresIn}}. If you didn't request this, you can safely ignore this email.</p>`),
	},
	// org_invitation is a platform-level notification: an org admin
	// added the recipient to their organization on Eurobase Console.
	// No token — the DB row already grants membership; the mail is
	// purely informational + a directional pointer to the SSO login.
	"org_invitation": {
		Subject: "You've been added to {{.OrgName}} on Eurobase",
		BodyHTML: fmt.Sprintf(baseLayout,
			"Eurobase Console",
			`<p style="margin:0 0 16px;color:#18181b;font-size:16px">Hi,</p>
<p style="margin:0 0 16px;color:#3f3f46;font-size:14px;line-height:1.6"><strong>{{.InviterEmail}}</strong> has added you to <strong>{{.OrgName}}</strong> on Eurobase.</p>
<p style="margin:0 0 24px;color:#3f3f46;font-size:14px;line-height:1.6">Sign in with SSO using this email address to access org-owned projects. If you don't already have an Eurobase account, sign up first — the invitation waits for you.</p>
<p style="margin:0 0 24px;text-align:center">
<a href="{{.ActionURL}}" style="display:inline-block;background:#1e3a5f;color:#ffffff;text-decoration:none;padding:12px 32px;border-radius:6px;font-size:14px;font-weight:600">Sign in to Eurobase</a>
</p>
<p style="margin:0;color:#71717a;font-size:12px">If you weren't expecting this, you can safely ignore this email — the person listed above added you and can remove you at any time.</p>`),
	},
}

// DefaultTemplates returns the built-in default templates.
func DefaultTemplates() map[string]DefaultTemplate {
	return defaultTemplates
}

// RenderTemplate renders a template with the given data.
// If customSubject/customHTML are empty, defaults are used.
func RenderTemplate(templateType, customSubject, customHTML string, data TemplateData) (string, string, error) {
	def, ok := defaultTemplates[templateType]
	if !ok {
		return "", "", fmt.Errorf("unknown template type: %s", templateType)
	}

	subjectTpl := def.Subject
	if customSubject != "" {
		subjectTpl = customSubject
	}
	bodyTpl := def.BodyHTML
	if customHTML != "" {
		bodyTpl = customHTML
	}

	subject, err := renderString(subjectTpl, data)
	if err != nil {
		return "", "", fmt.Errorf("render subject: %w", err)
	}

	body, err := renderString(bodyTpl, data)
	if err != nil {
		return "", "", fmt.Errorf("render body: %w", err)
	}

	return subject, body, nil
}

func renderString(tpl string, data TemplateData) (string, error) {
	t, err := template.New("email").Parse(tpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
