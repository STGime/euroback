package query

import "testing"

func TestIndexColumns(t *testing.T) {
	for def, want := range map[string]string{
		`CREATE INDEX a_idx ON s.t USING btree (title)`:                        "title",
		`CREATE INDEX a_idx ON s.t USING btree ("Weird Name")`:                 "Weird Name",
		`CREATE INDEX a_idx ON s.t USING btree (lower(name))`:                  "lower(name)",
		`CREATE INDEX a_idx ON s.t USING btree (a, b) WHERE (deleted IS NULL)`: "a, b",
		`CREATE UNIQUE INDEX a_idx ON s.t USING btree (lower((email)::text))`:  "lower((email)::text)",
		`garbage`: "",
	} {
		if got := indexColumns(def); got != want {
			t.Errorf("indexColumns(%q) = %q, want %q", def, got, want)
		}
	}
}
