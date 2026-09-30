package email

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/plans"
	"github.com/go-chi/chi/v5"
)

type fakeGate struct{ err error }

func (g fakeGate) CheckBYOSMTP(context.Context, string) error { return g.err }

// Off-plan projects can't save or test a custom SMTP sender (403); a plan
// lookup failure is a 503, never "upgrade". Both refuse before the sender
// service is touched (nil here).
func TestSenderHandlers_PlanGate(t *testing.T) {
	for _, c := range []struct {
		gate   fakeGate
		status int
		body   string
	}{
		{fakeGate{fmt.Errorf("%w: BYO SMTP is not available on the free plan", plans.ErrNotOnPlan)}, http.StatusForbidden, "plan_required"},
		{fakeGate{errors.New("failed to look up project plan")}, http.StatusServiceUnavailable, "couldn't check"},
	} {
		r := chi.NewRouter()
		r.Put("/p/{id}/email-sender", HandlePutSender(nil, c.gate))
		r.Post("/p/{id}/email-sender/test", HandleTestSender(nil, c.gate))
		for _, req := range []*http.Request{
			httptest.NewRequest(http.MethodPut, "/p/abc/email-sender", strings.NewReader(`{}`)),
			httptest.NewRequest(http.MethodPost, "/p/abc/email-sender/test", strings.NewReader(`{"to":"a@b.c"}`)),
		} {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.body) {
				t.Errorf("%s %s: got %d %s, want %d %q", req.Method, req.URL.Path, rec.Code, rec.Body.String(), c.status, c.body)
			}
		}
	}
}
