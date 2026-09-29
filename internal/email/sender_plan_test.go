package email

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeGate struct{ err error }

func (g fakeGate) CheckBYOSMTP(context.Context, string) error { return g.err }

// Off-plan projects can't save or test a custom SMTP sender; the refusal
// comes before the sender service is touched (nil here).
func TestSenderHandlers_PlanGate(t *testing.T) {
	off := fakeGate{err: errors.New("BYO SMTP is not available on the free plan, upgrade to pro")}
	for name, h := range map[string]http.HandlerFunc{
		"put":  HandlePutSender(nil, off),
		"test": HandleTestSender(nil, off),
	} {
		r := chi.NewRouter()
		r.Handle("/p/{id}", h)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/p/abc", strings.NewReader(`{"to":"a@b.c"}`)))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "plan_required") {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body.String())
		}
	}
}
