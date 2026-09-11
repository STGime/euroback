package sovereignty

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRatings_JSONCasing pins the snake_case JSON marshaling of
// the Ratings struct. Without explicit json tags Go emits field
// names verbatim (PascalCase). That shipped for a hot minute on
// the first Sovereignty Check deploy — the ratings block on
// /report responses came out as "EntityControl" while every
// other key was snake_case. Fixed with json tags on vendor.go;
// this test locks the shape in so a future rename can't silently
// regress it.
func TestRatings_JSONCasing(t *testing.T) {
	r := Ratings{
		EntityControl: "red", DataLocation: "amber",
		OperationalAccess: "amber", SubprocessorChain: "amber",
		TransferMechanism: "amber", Overall: "red",
	}
	blob, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(blob)
	// Must contain every snake_case key.
	for _, key := range []string{
		`"entity_control":`, `"data_location":`,
		`"operational_access":`, `"subprocessor_chain":`,
		`"transfer_mechanism":`, `"overall":`,
	} {
		if !strings.Contains(got, key) {
			t.Errorf("expected key %q in JSON; got %s", key, got)
		}
	}
	// Must NOT contain the PascalCase forms.
	for _, key := range []string{
		`"EntityControl":`, `"DataLocation":`,
		`"OperationalAccess":`, `"SubprocessorChain":`,
		`"TransferMechanism":`, `"Overall":`,
	} {
		if strings.Contains(got, key) {
			t.Errorf("expected NO PascalCase key %q; got %s", key, got)
		}
	}
}

// helper: build a minimal in-memory registry so we don't depend on
// the git submodule being checked out during CI.
func testRegistry(t *testing.T) *Registry {
	t.Helper()
	vendors := []*Vendor{
		{
			Slug: "acme-us", Name: "Acme US", Category: "baas",
			ParentJurisdiction: "US",
			OneLineReason:      "US-incorporated",
			EUAlternatives:     []string{"eurobase"},
			Ratings: Ratings{
				EntityControl: "red", DataLocation: "amber",
				OperationalAccess: "amber", SubprocessorChain: "amber",
				TransferMechanism: "amber", Overall: "red",
			},
		},
		{
			Slug: "acme-eu", Name: "Acme EU", Category: "baas",
			ParentJurisdiction: "EU",
			OneLineReason:      "EU-incorporated",
			Ratings: Ratings{
				EntityControl: "green", DataLocation: "green",
				OperationalAccess: "green", SubprocessorChain: "green",
				TransferMechanism: "green", Overall: "green",
			},
		},
		{
			Slug: "hybrid-amber", Name: "Hybrid Amber", Category: "cdn",
			ParentJurisdiction: "US",
			OneLineReason:      "amber",
			EUAlternatives:     []string{"bunny"},
			Ratings: Ratings{
				EntityControl: "amber", DataLocation: "amber",
				OperationalAccess: "amber", SubprocessorChain: "amber",
				TransferMechanism: "amber", Overall: "amber",
			},
		},
	}
	byslug := map[string]*Vendor{}
	for _, v := range vendors {
		byslug[v.Slug] = v
	}
	return &Registry{byslug: byslug}
}

func TestScore_EmptyInput(t *testing.T) {
	rep := Score(testRegistry(t), nil, "")
	if rep.Overall != "green" {
		t.Errorf("empty input overall = %q; want green", rep.Overall)
	}
	if rep.RedCount+rep.AmberCount+rep.GreenCount != 0 {
		t.Errorf("empty input should have zero counts, got %+v", rep)
	}
	if rep.ExposurePercent != 0 {
		t.Errorf("empty input exposure percent = %d; want 0", rep.ExposurePercent)
	}
}

func TestScore_AllGreen(t *testing.T) {
	rep := Score(testRegistry(t), []string{"acme-eu"}, "")
	if rep.Overall != "green" || rep.GreenCount != 1 || rep.RedCount != 0 {
		t.Errorf("all-green picks = %+v", rep)
	}
	if rep.ExposurePercent != 0 {
		t.Errorf("all-green exposure percent = %d; want 0", rep.ExposurePercent)
	}
}

func TestScore_MixedRedAmberGreen(t *testing.T) {
	// 1 red + 1 amber + 1 green; total 3
	// exposure = (1 + 0.5*1) / 3 * 100 = 50%
	rep := Score(testRegistry(t), []string{"acme-us", "hybrid-amber", "acme-eu"}, "")
	if rep.Overall != "red" {
		t.Errorf("mixed picks overall = %q; want red (any red wins)", rep.Overall)
	}
	if rep.RedCount != 1 || rep.AmberCount != 1 || rep.GreenCount != 1 {
		t.Errorf("counts wrong: %+v", rep)
	}
	if rep.ExposurePercent != 50 {
		t.Errorf("exposure percent = %d; want 50", rep.ExposurePercent)
	}
	// Cards ordered red first, then amber, then green
	if len(rep.Cards) != 3 {
		t.Fatalf("cards len = %d; want 3", len(rep.Cards))
	}
	if rep.Cards[0].Slug != "acme-us" || rep.Cards[1].Slug != "hybrid-amber" || rep.Cards[2].Slug != "acme-eu" {
		t.Errorf("card order wrong: %v %v %v",
			rep.Cards[0].Slug, rep.Cards[1].Slug, rep.Cards[2].Slug)
	}
}

func TestScore_SeverityPromotesAmberToRed(t *testing.T) {
	// hybrid-amber alone: standard → overall amber, exposure 50%
	std := Score(testRegistry(t), []string{"hybrid-amber"}, "")
	if std.Overall != "amber" || std.RedCount != 0 || std.AmberCount != 1 {
		t.Errorf("standard hybrid-amber = %+v", std)
	}
	if std.ExposurePercent != 50 {
		t.Errorf("standard amber exposure = %d; want 50", std.ExposurePercent)
	}
	// Same slug + severity=health → promoted to red
	sev := Score(testRegistry(t), []string{"hybrid-amber"}, SeverityHealth)
	if sev.Overall != "red" || sev.RedCount != 1 || sev.AmberCount != 0 {
		t.Errorf("severity=health hybrid-amber = %+v; want promoted to red", sev)
	}
	if sev.ExposurePercent != 100 {
		t.Errorf("severity-promoted exposure = %d; want 100", sev.ExposurePercent)
	}
}

func TestScore_SeverityDoesNotDowngradeRed(t *testing.T) {
	// Red stays red regardless of severity — we only promote amber → red.
	rep := Score(testRegistry(t), []string{"acme-us"}, SeverityLegal)
	if rep.Overall != "red" || rep.RedCount != 1 {
		t.Errorf("severity legal on red = %+v", rep)
	}
}

func TestScore_UnknownSlugs(t *testing.T) {
	rep := Score(testRegistry(t), []string{"acme-us", "does-not-exist", "also-missing"}, "")
	if len(rep.UnknownSlugs) != 2 {
		t.Errorf("unknown slugs len = %d; want 2", len(rep.UnknownSlugs))
	}
	// Alphabetized for stable output
	if rep.UnknownSlugs[0] != "also-missing" || rep.UnknownSlugs[1] != "does-not-exist" {
		t.Errorf("unknown slugs not sorted: %v", rep.UnknownSlugs)
	}
	// The known slug still scored
	if rep.RedCount != 1 || len(rep.Cards) != 1 {
		t.Errorf("unknowns should not affect scoring, got %+v", rep)
	}
}

func TestScore_DedupesInput(t *testing.T) {
	// Duplicate slugs in the caller input should only count once.
	rep := Score(testRegistry(t), []string{"acme-us", "acme-us", "acme-us"}, "")
	if rep.RedCount != 1 || len(rep.Cards) != 1 {
		t.Errorf("duplicate-input dedup failed: %+v", rep)
	}
}

func TestScore_AlternativesByCategory(t *testing.T) {
	// Two red-baas + one red-cdn → two swap rows: one per category,
	// each pointing at that category's alternatives.
	rep := Score(testRegistry(t), []string{"acme-us", "hybrid-amber"}, "")
	if len(rep.Alternatives) != 2 {
		t.Fatalf("swap groups = %d; want 2 (one per category)", len(rep.Alternatives))
	}
	// Order matches insertion order: acme-us was first, so baas first.
	if rep.Alternatives[0].Category != "baas" || rep.Alternatives[1].Category != "cdn" {
		t.Errorf("swap group order wrong: %v %v",
			rep.Alternatives[0].Category, rep.Alternatives[1].Category)
	}
	if rep.Alternatives[0].Alternatives[0] != "eurobase" {
		t.Errorf("baas alt = %v; want eurobase", rep.Alternatives[0].Alternatives)
	}
}

func TestScore_LoadEmbeddedIntegration(t *testing.T) {
	// End-to-end: load the real embedded dataset (submodule
	// checkout at data/vendors/*.yaml) and score a known mix.
	// Skipped when the submodule isn't populated (e.g. shallow
	// checkout in a dev environment) so the unit tests above stay
	// runnable in isolation.
	reg, err := LoadEmbedded()
	if err != nil {
		t.Skipf("embedded registry unavailable — submodule not checked out? err=%v", err)
	}
	if reg.Count() < 5 {
		t.Fatalf("embedded registry only has %d vendors; expected the 11 seeds", reg.Count())
	}
	// Supabase should be red; Scaleway should be green.
	rep := Score(reg, []string{"supabase", "scaleway"}, "")
	if rep.Overall != "red" {
		t.Errorf("supabase+scaleway overall = %q; want red", rep.Overall)
	}
	if rep.RedCount != 1 || rep.GreenCount != 1 {
		t.Errorf("supabase+scaleway counts = red=%d amber=%d green=%d; want 1/0/1",
			rep.RedCount, rep.AmberCount, rep.GreenCount)
	}
}
