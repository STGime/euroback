package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The {kind} segment picks the table from a fixed list and {id} must be a
// UUID; anything else is refused before the database is touched (the
// handlers get a nil pool here and would panic if they reached it).
func TestInboxTargetRefusesUnknownKindAndBadID(t *testing.T) {
	for _, tc := range []struct{ kind, id string }{
		{"orgs", "00000000-0000-0000-0000-000000000001"},
		{"support; DROP TABLE x", "00000000-0000-0000-0000-000000000001"},
		{"support", "not-a-uuid"},
		{"contact", ""},
	} {
		for _, h := range []http.HandlerFunc{AdminResolveInboxItem(nil), AdminReopenInboxItem(nil)} {
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("kind", tc.kind)
			rctx.URLParams.Add("id", tc.id)
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("kind=%q id=%q: status %d, want 404", tc.kind, tc.id, rec.Code)
			}
		}
	}
}

func TestInboxTable(t *testing.T) {
	if tbl, ok := inboxTable("support"); !ok || tbl != "public.support_requests" {
		t.Errorf("support -> %q %v", tbl, ok)
	}
	if tbl, ok := inboxTable("contact"); !ok || tbl != "public.contact_requests" {
		t.Errorf("contact -> %q %v", tbl, ok)
	}
	if _, ok := inboxTable("platform_users"); ok {
		t.Error("unknown kinds must be refused")
	}
}

func TestAdminListInboxRefusesBadStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	AdminListInbox(nil)(rec, httptest.NewRequest(http.MethodGet, "/?status=deleted", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	AdminListInbox(nil)(rec, httptest.NewRequest(http.MethodGet, "/?limit=-1", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("limit: status %d, want 400", rec.Code)
	}
}
