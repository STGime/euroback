package cli

import (
	"strings"
	"testing"
	"time"
)

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.7.0", "0.7.1", true},
		{"0.7.1", "0.7.1", false},
		{"0.7.10", "0.7.9", false},
		{"0.9.0", "0.10.0", true},
		{"1.0.0-rc.1", "1.0.0", true},
		{"1.0.0", "1.0.0-rc.1", false},
		{"dev", "0.7.1", false},
		{"0.7.1", "garbage", false},
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestUpdateNotice(t *testing.T) {
	s := updateCheckState{CheckedAt: time.Now(), Latest: "0.8.0", Install: "brew upgrade stgime/tap/eurobase", URL: "https://example/releases/tag/cli-v0.8.0"}
	n := updateNotice("0.7.1", s)
	for _, want := range []string{"0.7.1 → 0.8.0", "brew upgrade stgime/tap/eurobase", "cli-v0.8.0", "EUROBASE_NO_UPDATE_CHECK=1"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice lacks %q:\n%s", want, n)
		}
	}
	if updateNotice("0.8.0", s) != "" || updateNotice("0.9.0", s) != "" {
		t.Error("no notice when the CLI is current or newer")
	}
	if updateNotice("0.7.1", updateCheckState{}) != "" {
		t.Error("no notice without a known latest version")
	}
}

func TestUpdateCheckDisabled(t *testing.T) {
	t.Setenv("EUROBASE_NO_UPDATE_CHECK", "1")
	if updateCheckEnabled("0.7.1") {
		t.Error("EUROBASE_NO_UPDATE_CHECK should turn the check off")
	}
	t.Setenv("EUROBASE_NO_UPDATE_CHECK", "")
	if updateCheckEnabled("dev") {
		t.Error("development builds never check")
	}
}
