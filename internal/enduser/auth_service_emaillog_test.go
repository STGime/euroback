package enduser

import "testing"

func TestRedirectForLog(t *testing.T) {
	for in, want := range map[string]string{
		"https://app.example.com/magic?token=secret&email=a@b.c#x": "https://app.example.com/magic",
		"http://localhost:3000": "http://localhost:3000",
		"/relative/path":        "(not a valid absolute URL)",
		"not a url at all":      "(not a valid absolute URL)",
	} {
		if got := redirectForLog(in); got != want {
			t.Errorf("redirectForLog(%q) = %q, want %q", in, got, want)
		}
	}
}
