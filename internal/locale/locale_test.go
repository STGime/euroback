package locale

import (
	"context"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"de": "de", "de-AT": "de", "de_CH": "de", "DE": "de", " fr-CA ": "fr",
		"nb": "nb", "nb-NO": "nb", "no": "nb", "nn-NO": "nb",
		"sv-SE": "sv", "es-419": "es", "nl-BE": "nl", "en-GB": "en",
		"it": "", "pl-PL": "", "": "", "x": "", "1234": "", "de;q=1": "",
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz": "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromAcceptLanguage(t *testing.T) {
	cases := map[string]string{
		"":                                 "",
		"de-DE,de;q=0.9,en;q=0.8":          "de",
		"it-IT,it;q=0.9,fr;q=0.5,en;q=0.4": "fr",
		"en;q=0.5, nb;q=0.9":               "nb",
		"pl, it":                           "",
		"fr;q=0, de":                       "de",
		"sv;q=abc, nl":                     "nl",
		"*":                                "",
		"es, de":                           "es", // equal q: first wins
	}
	for in, want := range cases {
		if got := FromAcceptLanguage(in); got != want {
			t.Errorf("FromAcceptLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveOrder(t *testing.T) {
	cases := []struct {
		stored, requested, accept, def, want string
	}{
		{"de", "fr", "nl", "sv", "de"}, // stored always wins
		{"", "fr", "nl", "sv", "fr"},   // then the app's request
		{"", "", "nl", "sv", "sv"},     // an explicit project default beats the browser
		{"", "", "nl", "en", "en"},     // also an explicit English
		{"", "", "nl", "", "nl"},       // automatic: the browser
		{"", "", "", "sv", "sv"},
		{"", "", "", "", "en"},
		{"it", "pl", "pt", "xx", "en"},     // nothing supported
		{"garbage!", "es", "", "", "es"},   // bad stored value falls through
		{"no", "", "", "", "nb"},           // alias
		{"", "", "it, de;q=0.3", "", "de"}, // header skips unsupported
	}
	for _, c := range cases {
		if got := Resolve(c.stored, c.requested, c.accept, c.def); got != c.want {
			t.Errorf("Resolve(%q,%q,%q,%q) = %q, want %q", c.stored, c.requested, c.accept, c.def, got, c.want)
		}
	}
}

func TestHintsAndForNewUser(t *testing.T) {
	ctx := context.Background()
	if ForNewUser(ctx, "") != "" {
		t.Fatal("no hints: want empty")
	}
	ctx = WithAcceptLanguage(ctx, "sv-SE,sv;q=0.9")
	if got := ForNewUser(ctx, ""); got != "sv" {
		t.Fatalf("header only, automatic project: got %q", got)
	}
	if got := ForNewUser(ctx, "en"); got != "" {
		t.Fatalf("explicit project default: the browser language must not be stored, got %q", got)
	}
	ctx = WithRequested(ctx, "")
	if got := ForNewUser(ctx, ""); got != "sv" {
		t.Fatalf("empty requested must not clear: got %q", got)
	}
	ctx = WithRequested(ctx, "de-AT")
	if got := ForNewUser(ctx, "en"); got != "de" {
		t.Fatalf("requested wins, also over an explicit default: got %q", got)
	}
	ctx = WithRequested(ctx, "it")
	if got := ForNewUser(ctx, ""); got != "sv" {
		t.Fatalf("unsupported requested falls back to header: got %q", got)
	}
	h := HintsFrom(ctx)
	if h.Requested != "it" || h.AcceptLanguage != "sv-SE,sv;q=0.9" {
		t.Fatalf("hints: %+v", h)
	}
}

func TestSupportedAreNormalForms(t *testing.T) {
	for _, l := range Supported {
		if Normalize(l) != l || !IsSupported(l) {
			t.Errorf("%q is not its own normal form", l)
		}
	}
}
