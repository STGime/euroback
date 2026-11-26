package email

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/locale"
)

var placeholderRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

func placeholders(s string) []string {
	p := placeholderRe.FindAllString(s, -1)
	sort.Strings(p)
	return p
}

var tagRe = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

// Every language defines every key, with exactly the English placeholders
// and HTML tags: a translation can change words, never a placeholder, a
// link or the markup.
func TestLocaleFilesComplete(t *testing.T) {
	en := localeStrings[locale.Default]
	for _, l := range locale.Supported {
		s, ok := localeStrings[l]
		if !ok {
			t.Fatalf("%s: not loaded", l)
		}
		if len(s) != len(localeKeys) {
			t.Errorf("%s: %d keys, want %d (unknown or duplicate keys?)", l, len(s), len(localeKeys))
		}
		for _, k := range localeKeys {
			if got, want := strings.Join(placeholders(s[k]), " "), strings.Join(placeholders(en[k]), " "); got != want {
				t.Errorf("%s.%s: placeholders %q, want %q", l, k, got, want)
			}
			if got, want := strings.Join(tagRe.FindAllString(s[k], -1), ""), strings.Join(tagRe.FindAllString(en[k], -1), ""); got != want {
				t.Errorf("%s.%s: tags %q, want %q", l, k, got, want)
			}
			if strings.Contains(s[k], "{{") != strings.Contains(en[k], "{{") {
				t.Errorf("%s.%s: stray template braces", l, k)
			}
		}
	}
}

// Every template renders in every language with all placeholders filled.
func TestLocaleTemplatesRender(t *testing.T) {
	data := TemplateData{UserEmail: "a@example.com", ProjectName: "Klubbappen", ActionURL: "https://app.example/cb?token=abc&locale=xx"}
	for _, l := range locale.Supported {
		for _, typ := range authTemplateTypes {
			data.ExpiresIn = Duration(l, "1h")
			subj, body, err := RenderTemplateLocale(typ, l, "", "", data)
			if err != nil {
				t.Fatalf("%s/%s: %v", l, typ, err)
			}
			if subj == "" || strings.Contains(subj, "{{") || strings.Contains(body, "{{") {
				t.Errorf("%s/%s: unrendered placeholder", l, typ)
			}
			if !strings.Contains(body, `<html lang="`+l+`">`) {
				t.Errorf("%s/%s: missing lang attribute", l, typ)
			}
			if !strings.Contains(body, "Klubbappen") || !strings.Contains(body, "Eurobase") {
				t.Errorf("%s/%s: project name or footer missing", l, typ)
			}
			if typ != "welcome" && typ != "password_changed" {
				if !strings.Contains(body, `href="https://app.example/cb?token=abc&amp;locale=xx"`) {
					t.Errorf("%s/%s: action link missing or altered", l, typ)
				}
				if !strings.Contains(body, data.ExpiresIn) {
					t.Errorf("%s/%s: expiry %q missing", l, typ, data.ExpiresIn)
				}
			}
		}
	}
}

func TestLocaleTemplatesDiffer(t *testing.T) {
	en := DefaultTemplatesFor("en")["verification"]
	for _, l := range locale.Supported[1:] {
		if DefaultTemplatesFor(l)["verification"].Subject == en.Subject {
			t.Errorf("%s: verification subject is still English", l)
		}
	}
	if DefaultTemplatesFor("it")["verification"].Subject != en.Subject {
		t.Error("unsupported language should fall back to English")
	}
	if got := DefaultTemplatesFor("de-AT")["magic_link"].Subject; got != "Bei {{.ProjectName}} anmelden" {
		t.Errorf("de-AT: %q", got)
	}
}

// A project's custom template wins over every built-in translation.
func TestCustomTemplateWinsOverTranslation(t *testing.T) {
	data := TemplateData{ProjectName: "P", ActionURL: "https://x", ExpiresIn: "1"}
	subj, body, err := RenderTemplateLocale("verification", "de", "Custom {{.ProjectName}}", "<p>custom body</p>", data)
	if err != nil {
		t.Fatal(err)
	}
	if subj != "Custom P" || body != "<p>custom body</p>" {
		t.Errorf("custom template not used: %q / %q", subj, body)
	}
	// Only a custom subject: the body is the built-in in the language.
	_, body, _ = RenderTemplateLocale("verification", "de", "Custom", "", data)
	if !strings.Contains(body, "Bitte bestätigen Sie") {
		t.Error("built-in German body expected with a custom subject only")
	}
}

func TestDuration(t *testing.T) {
	if Duration("de", "24h") != "24 Stunden" || Duration("nb", "15m") != "15 minutter" || Duration("it", "1h") != "1 hour" {
		t.Error("durations")
	}
}

// The English built-ins must stay byte-identical to the templates sent
// before email languages existed (testdata, captured from the old
// templates.go), apart from the new lang attribute.
func TestEnglishTemplatesUnchanged(t *testing.T) {
	raw, err := os.ReadFile("testdata/en_templates_before_languages.json")
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]DefaultTemplate
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	now := DefaultTemplatesFor("en")
	for _, typ := range authTemplateTypes {
		b, ok := before[typ]
		if !ok {
			t.Fatalf("%s missing from testdata", typ)
		}
		want := strings.Replace(b.BodyHTML, "<html>", `<html lang="en">`, 1)
		if now[typ].Subject != b.Subject || now[typ].BodyHTML != want {
			t.Errorf("%s: English template changed", typ)
		}
		if DefaultTemplates()[typ] != now[typ] {
			t.Errorf("%s: DefaultTemplates() is not the English built-in", typ)
		}
	}
}

func TestExpiresInCustomBodyStaysEnglish(t *testing.T) {
	if expiresIn("de", "", "24h") != "24 Stunden" {
		t.Error("built-in template: want the translated duration")
	}
	if expiresIn("de", "<p>{{.ExpiresIn}}</p>", "24h") != "24 hours" {
		t.Error("custom body: want the English duration, as before languages existed")
	}
}
