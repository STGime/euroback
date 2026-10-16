package gateway

import (
	"os"
	"regexp"
	"testing"
)

// Pins the minimum role of the platform edge-function routes: a
// function's code (via HandleGet's redaction), versions and invocation
// logs are for Developer and up. A route quietly moved back to viewer
// fails here.
func TestFunctionRouteRoles(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		`Get("/", functions.HandleList(`:                        "viewer",
		`Get("/{name}", functions.HandleGet(`:                   "viewer", // redacts code + env values below developer
		`Get("/{name}/logs", functions.HandleLogs(`:             "developer",
		`Get("/{name}/versions", functions.HandleListVersions(`: "developer",
		`Get("/{name}/triggers", functions.HandleListTriggers(`: "viewer",
		`Get("/{name}/metrics", functions.HandleMetrics(`:       "viewer",
		`Post("/", functions.HandleCreate(`:                     "developer",
		`Put("/{name}", functions.HandleUpdate(`:                "developer",
		`Delete("/{name}", functions.HandleDelete(`:             "developer",
		`Post("/{name}/rollback", functions.HandleRollback(`:    "developer",
	}
	for route, role := range want {
		re := regexp.MustCompile(`tenant\.RequireMinRole\("([a-z]+)"\)[^\n]*\)\.` + regexp.QuoteMeta(route))
		m := re.FindAllSubmatch(src, -1)
		if len(m) != 1 {
			t.Errorf("%s: found %d route registrations, want 1", route, len(m))
			continue
		}
		if got := string(m[0][1]); got != role {
			t.Errorf("%s: RequireMinRole(%q), want %q", route, got, role)
		}
	}
}
