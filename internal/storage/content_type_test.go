package storage

import "testing"

func TestResolveContentType(t *testing.T) {
	for _, tc := range []struct {
		key, s3, recorded, want string
	}{
		{"themes/nordic/hero.jpg", "", "", "image/jpeg"},
		{"themes/nordic/owl.m4a", "application/octet-stream", "audio/mp4a-latm", "audio/mp4a-latm"},
		{"themes/nordic/owl.m4a", "", "", "audio/mp4"},
		{"a/b.png", "image/png", "text/html", "image/png"},              // specific S3 type wins
		{"x/page.html", "", "", "application/octet-stream"},             // never infer active types
		{"x/logo.svg", "", "image/svg+xml", "application/octet-stream"}, // recorded active type ignored
		{"x/data", "", "", "application/octet-stream"},
		{"x/doc.pdf", "binary/octet-stream", "", "application/pdf"},
	} {
		if got := resolveContentType(tc.key, tc.s3, tc.recorded); got != tc.want {
			t.Errorf("resolveContentType(%q, %q, %q) = %q, want %q", tc.key, tc.s3, tc.recorded, got, tc.want)
		}
	}
	if !activeContentType("text/html; charset=utf-8") || !activeContentType("image/svg+xml") || activeContentType("image/png") {
		t.Error("activeContentType misclassifies")
	}
}
