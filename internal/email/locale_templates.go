package email

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eurobase/euroback/internal/locale"
)

// Built-in auth email templates in every supported language.
//
// Each language is one JSON file in locales/ with the text pieces below.
// The HTML frame is shared, so a translation can only change words, not
// markup or links. Placeholders ({{.ProjectName}}, {{.ExpiresIn}}) must
// match the English file exactly (TestLocaleFilesComplete).
//
//go:embed locales/*.json
var localeFiles embed.FS

// localeKeys are the text pieces every language file must define.
var localeKeys = []string{
	"footer", "greeting", "duration_24h", "duration_1h", "duration_15m",
	"verification_subject", "verification_intro", "verification_button", "verification_note",
	"password_reset_subject", "password_reset_intro", "password_reset_button", "password_reset_note",
	"welcome_subject", "welcome_heading", "welcome_intro",
	"password_changed_subject", "password_changed_intro",
	"magic_link_subject", "magic_link_intro", "magic_link_button", "magic_link_note",
}

// authTemplateTypes are the template types built per language. They are
// the types a project can customise (handler.go's allow-list).
var authTemplateTypes = []string{"verification", "password_reset", "welcome", "password_changed", "magic_link"}

const localeLayout = `<!DOCTYPE html>
<html lang="%s">
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
%s
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

const (
	pGreeting = `<p style="margin:0 0 16px;color:#18181b;font-size:16px">%s</p>`
	pIntro    = `<p style="margin:0 0 24px;color:#3f3f46;font-size:14px;line-height:1.6">%s</p>`
	pButton   = `<p style="margin:0 0 24px;text-align:center">
<a href="{{.ActionURL}}" style="display:inline-block;background:#1e3a5f;color:#ffffff;text-decoration:none;padding:12px 32px;border-radius:6px;font-size:14px;font-weight:600">%s</a>
</p>`
	pNote = `<p style="margin:0;color:#71717a;font-size:12px">%s</p>`
)

// localeStrings holds each language's text pieces; localeTemplates the
// built templates, by language and type.
var (
	localeStrings   = map[string]map[string]string{}
	localeTemplates = map[string]map[string]DefaultTemplate{}
)

func init() {
	for _, l := range locale.Supported {
		raw, err := localeFiles.ReadFile("locales/" + l + ".json")
		if err != nil {
			panic(fmt.Sprintf("email: missing locales/%s.json: %v", l, err))
		}
		var s map[string]string
		if err := json.Unmarshal(raw, &s); err != nil {
			panic(fmt.Sprintf("email: locales/%s.json: %v", l, err))
		}
		for _, k := range localeKeys {
			if strings.TrimSpace(s[k]) == "" {
				panic(fmt.Sprintf("email: locales/%s.json: missing %q", l, k))
			}
		}
		localeStrings[l] = s
		localeTemplates[l] = buildLocaleTemplates(l, s)
	}
	// The English built-ins are also the project-facing defaults (the
	// console's template editor starts from them).
	for t, tpl := range localeTemplates[locale.Default] {
		defaultTemplates[t] = tpl
	}
}

func buildLocaleTemplates(lang string, s map[string]string) map[string]DefaultTemplate {
	page := func(parts ...string) string {
		return fmt.Sprintf(localeLayout, lang, "{{.ProjectName}}", strings.Join(parts, "\n"), s["footer"])
	}
	greet := fmt.Sprintf(pGreeting, s["greeting"])
	linkMail := func(prefix string) DefaultTemplate {
		return DefaultTemplate{
			Subject: s[prefix+"_subject"],
			BodyHTML: page(greet,
				fmt.Sprintf(pIntro, s[prefix+"_intro"]),
				fmt.Sprintf(pButton, s[prefix+"_button"]),
				fmt.Sprintf(pNote, s[prefix+"_note"])),
		}
	}
	return map[string]DefaultTemplate{
		"verification":   linkMail("verification"),
		"password_reset": linkMail("password_reset"),
		"magic_link":     linkMail("magic_link"),
		"welcome": {
			Subject: s["welcome_subject"],
			BodyHTML: page(fmt.Sprintf(pGreeting, s["welcome_heading"]),
				fmt.Sprintf(pIntro, s["welcome_intro"])),
		},
		"password_changed": {
			Subject:  s["password_changed_subject"],
			BodyHTML: page(greet, fmt.Sprintf(pIntro, s["password_changed_intro"])),
		},
	}
}

// DefaultTemplatesFor returns the built-in auth templates in lang (English
// for an unsupported language).
func DefaultTemplatesFor(lang string) map[string]DefaultTemplate {
	if t, ok := localeTemplates[locale.Normalize(lang)]; ok {
		return t
	}
	return localeTemplates[locale.Default]
}

// Duration returns a link lifetime ("24h", "1h", "15m") in lang, for the
// {{.ExpiresIn}} placeholder.
func Duration(lang, key string) string {
	s, ok := localeStrings[locale.Normalize(lang)]
	if !ok {
		s = localeStrings[locale.Default]
	}
	if v := s["duration_"+key]; v != "" {
		return v
	}
	return localeStrings[locale.Default]["duration_"+key]
}

// RenderTemplateLocale renders templateType in lang. A project's custom
// subject or body (one per type, any language) always wins over the
// built-in translation; otherwise the built-in in lang is used.
func RenderTemplateLocale(templateType, lang, customSubject, customHTML string, data TemplateData) (string, string, error) {
	def, ok := DefaultTemplatesFor(lang)[templateType]
	if !ok {
		return RenderTemplate(templateType, customSubject, customHTML, data)
	}
	subjectTpl := def.Subject
	if customSubject != "" {
		subjectTpl = customSubject
	}
	bodyTpl := def.BodyHTML
	if customHTML != "" {
		bodyTpl = customHTML
	}
	subject, err := renderSubject(subjectTpl, data)
	if err != nil {
		return "", "", fmt.Errorf("render subject: %w", err)
	}
	body, err := renderBody(bodyTpl, data)
	if err != nil {
		return "", "", fmt.Errorf("render body: %w", err)
	}
	return subject, body, nil
}
