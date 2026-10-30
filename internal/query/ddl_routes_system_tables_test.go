package query

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Every DDL route that changes a table refuses the platform-managed ones
// with 403 before touching the database (on Team, platform SQL runs as the
// database owner, so the database alone wouldn't refuse). Walks the real
// router, so a new unguarded route fails here.
func TestDDLRoutesRefuseSystemTables(t *testing.T) {
	router := HandleDDL(nil)
	body := `{"name":"p","command":"ALL","using":"true","enabled":false,"preset":"full_access","new_name":"x","column":"id","columns":["id"]}`

	var checked int
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet || !strings.Contains(route, "{table}") {
			return nil
		}
		for _, table := range tenantSystemTables {
			path := strings.NewReplacer("{table}", table, "{column}", "id", "{constraint}", "c",
				"{index}", "i", "{trigger}", "t", "{policy}", "p").Replace(route)
			rec := httptest.NewRecorder()
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("%s %s reached the database for %s (unguarded): %v", method, route, table, p)
					}
				}()
				router.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
			}()
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s on %s: status %d, want 403", method, route, table, rec.Code)
			}
			checked++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := 16 * len(tenantSystemTables); checked < want {
		t.Errorf("checked %d route/table pairs, want at least %d", checked, want)
	}

	for _, table := range tenantSystemTables {
		if !reservedTableNames[table] {
			t.Errorf("%s is missing from reservedTableNames", table)
		}
	}
	if err := refuseSystemTableChange("todos"); err != nil {
		t.Errorf("application table refused: %v", err)
	}
}
