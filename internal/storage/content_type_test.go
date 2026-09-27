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
	for ct, inline := range map[string]bool{
		"image/png": true, "IMAGE/JPEG": true, "audio/mp4": true, "video/mp4": true,
		"application/pdf": true, "text/plain; charset=utf-8": true,
		"text/html": false, "text/html; charset=utf-8": false, "TEXT/HTML ": false,
		"text/html,": false, "text/plain, text/html": false, "image/svg+xml": false,
		"application/xhtml+xml": false, "application/rss+xml": false, "application/x-javascript": false,
		"text/xsl": false, "multipart/x-mixed-replace": false, "": false, "garbage": false,
	} {
		if got := servedInline(ct); got != inline {
			t.Errorf("servedInline(%q) = %v, want %v", ct, got, inline)
		}
	}
}
