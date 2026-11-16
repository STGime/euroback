package storage

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A sized upload URL signs Content-Length, so the storage refuses a PUT
// of any other size; an unsized one doesn't. Presigning is local, no
// network needed.
func TestPresignedUploadURL_SignsContentLength(t *testing.T) {
	c, err := NewS3Client("https://s3.example.invalid", "fr-par", "AK", "SK")
	if err != nil {
		t.Fatal(err)
	}
	signed := func(size int64) string {
		p, err := c.GeneratePresignedUploadURLSized(context.Background(), "b", "k.bin", "application/octet-stream", time.Minute, Retention{}, size)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(p.URL)
		if err != nil {
			t.Fatal(err)
		}
		return u.Query().Get("X-Amz-SignedHeaders")
	}
	if h := signed(12345); !strings.Contains(h, "content-length") {
		t.Errorf("sized URL signed headers = %q, want content-length", h)
	}
	if h := signed(0); strings.Contains(h, "content-length") {
		t.Errorf("unsized URL signed headers = %q, want no content-length", h)
	}
}
