package tenant

import (
	"strings"
	"testing"
)

func TestWithBroadcastFooter(t *testing.T) {
	const url = "https://console.example.test"
	full := withBroadcastFooter("<html><body><p>Hi</p></BODY></html>", url, "")
	i, j := strings.Index(full, "Account &rarr; Mail"), strings.Index(full, "</BODY>")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("footer must sit before </body>: %s", full)
	}
	if !strings.Contains(full, `href="https://console.example.test/account#mail"`) {
		t.Fatalf("link to the console's mail settings missing: %s", full)
	}
	frag := withBroadcastFooter("<p>Hi</p>", url, "")
	if !strings.HasPrefix(frag, "<p>Hi</p>") || !strings.Contains(frag, "contact@eurobase.app") {
		t.Fatalf("fragment: %s", frag)
	}
}

func TestWithBroadcastFooter_PersonalLink(t *testing.T) {
	got := withBroadcastFooter("<p>Hi</p>", "https://console.example.test", "https://api.example.test/platform/mailing/unsubscribe?token=a&b")
	if !strings.Contains(got, `href="https://api.example.test/platform/mailing/unsubscribe?token=a&amp;b"`) {
		t.Fatalf("personal unsubscribe link missing or not escaped: %s", got)
	}
	if !strings.Contains(got, "Account &rarr; Mail") {
		t.Fatalf("console settings link missing: %s", got)
	}
}
