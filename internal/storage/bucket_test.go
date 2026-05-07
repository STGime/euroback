package storage

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// Closes advisory GHSA-gvrg-vq6j-j647 — storage bucket name must come
// from the authenticated ProjectContext, never from the X-Project-Slug
// request header. The previous version trusted the header verbatim and
// let any authenticated SDK caller upload/list/signed-url-mint against
// any tenant's bucket.

func TestBucketForRequest_UsesProjectContextSlug(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/storage/upload", nil)
	ctx := auth.ContextWithProject(r.Context(), &auth.ProjectContext{
		ProjectID:  "p1",
		SchemaName: "tenant_p1",
		Slug:       "alpha",
	})
	r = r.WithContext(ctx)

	got, err := bucketForRequest(r)
	if err != nil {
		t.Fatalf("bucketForRequest: unexpected error %v", err)
	}
	if got != "eurobase-alpha" {
		t.Errorf("bucketForRequest = %q, want %q", got, "eurobase-alpha")
	}
}

func TestBucketForRequest_IgnoresXProjectSlugHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/storage/upload", nil)
	r.Header.Set("X-Project-Slug", "victim")
	ctx := auth.ContextWithProject(r.Context(), &auth.ProjectContext{
		ProjectID:  "p1",
		SchemaName: "tenant_p1",
		Slug:       "alpha",
	})
	r = r.WithContext(ctx)

	got, err := bucketForRequest(r)
	if err != nil {
		t.Fatalf("bucketForRequest: unexpected error %v", err)
	}
	if got == "eurobase-victim" {
		t.Fatalf("bucketForRequest honored X-Project-Slug header — that's the GHSA-gvrg-vq6j-j647 vector")
	}
	if got != "eurobase-alpha" {
		t.Errorf("bucketForRequest = %q, want %q", got, "eurobase-alpha")
	}
}

func TestBucketForRequest_RejectsMissingProjectContext(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/storage/upload", nil)
	// No ProjectContext on r.Context() — this should fail closed.
	r.Header.Set("X-Project-Slug", "victim")

	_, err := bucketForRequest(r)
	if err == nil {
		t.Fatal("bucketForRequest succeeded with no ProjectContext — must reject")
	}
	if !strings.Contains(err.Error(), "project context") {
		t.Errorf("error = %v, want a project-context-related error", err)
	}
}

func TestBucketForRequest_RejectsEmptySlugInContext(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/storage/upload", nil)
	ctx := auth.ContextWithProject(r.Context(), &auth.ProjectContext{
		ProjectID:  "p1",
		SchemaName: "tenant_p1",
		// Slug intentionally empty — represents a misconfigured upstream
		// middleware that didn't populate it.
	})
	r = r.WithContext(ctx)
	r.Header.Set("X-Project-Slug", "victim") // header is irrelevant

	_, err := bucketForRequest(r)
	if err == nil {
		t.Fatal("bucketForRequest succeeded with empty Slug — must reject (cannot fall back to header)")
	}
}
