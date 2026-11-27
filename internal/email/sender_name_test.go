package email

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestProjectSenderName(t *testing.T) {
	cases := []struct{ project, platform, want string }{
		{"Innebandy pilot test", "Eurobase", "Innebandy pilot test via Eurobase"},
		{"Café Ölstuga", "Eurobase", "Café Ölstuga via Eurobase"},
		{"  spaced\t\tout  ", "Eurobase", "spaced out via Eurobase"},
		{"Line\r\nBcc: x@y.z", "Eurobase", "Line Bcc x y.z via Eurobase"},
		{`Evil" <admin@eurobase.app>`, "Eurobase", "Evil admin eurobase.app via Eurobase"},
		{"bidi‮evil", "Eurobase", "bidievil via Eurobase"},
		{"zero​width", "Eurobase", "zerowidth via Eurobase"},
		{"App", "", "App via Eurobase"},
		{"App", "  My Platform ", "App via My Platform"},
		{"", "Eurobase", ""},
		{"  \t ", "Eurobase", ""},
		{"<>\"", "Eurobase", ""},
		{"‮​", "Eurobase", ""},
	}
	for _, c := range cases {
		if got := ProjectSenderName(c.project, c.platform); got != c.want {
			t.Errorf("ProjectSenderName(%q, %q) = %q, want %q", c.project, c.platform, got, c.want)
		}
	}
}

func TestProjectSenderName_Truncates(t *testing.T) {
	got := ProjectSenderName(strings.Repeat("å", 100), "Eurobase")
	if !strings.HasSuffix(got, "… via Eurobase") {
		t.Fatalf("got %q, want a cut name ending in \"… via Eurobase\"", got)
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "… via Eurobase")); n != maxProjectSenderName {
		t.Fatalf("project part has %d characters, want %d", n, maxProjectSenderName)
	}
	if !utf8.ValidString(got) {
		t.Fatal("cut name is not valid UTF-8")
	}
}
