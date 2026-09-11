package sovereignty

import (
	"sort"
	"strings"
)

// Report is the per-check result the frontend renders. Persisted
// verbatim in sovereignty_reports.computed_score (JSONB) so the OG
// image + PDF re-renders don't need to re-run the scoring function
// against a potentially-changed vendor DB.
type Report struct {
	Overall          string       `json:"overall"`
	ExposurePercent  int          `json:"exposure_percent"`
	RedCount         int          `json:"red_count"`
	AmberCount       int          `json:"amber_count"`
	GreenCount       int          `json:"green_count"`
	SeverityModifier string       `json:"severity_modifier,omitempty"`
	Cards            []VendorCard `json:"cards"`
	Alternatives     []Swap       `json:"alternatives"`
	// UnknownSlugs lists any slugs the caller submitted that we
	// couldn't find in the vendor DB. Included in the report so the
	// caller sees which picks were dropped; scoring ignores them.
	UnknownSlugs []string `json:"unknown_slugs,omitempty"`
}

// VendorCard is the per-vendor detail rendered in the report. Only
// the fields the UI needs — the full Vendor is kept in the registry
// and looked up separately for the vendor-detail SEO pages.
type VendorCard struct {
	Slug              string  `json:"slug"`
	Name              string  `json:"name"`
	Category          string  `json:"category"`
	Overall           string  `json:"overall"`
	OneLineReason     string  `json:"one_line_reason"`
	ParentJurisdiction string `json:"parent_jurisdiction"`
	Ratings           Ratings `json:"ratings"`
	SelfDisclosure    bool    `json:"self_disclosure,omitempty"`
}

// Swap points the user at an EU alternative for one of the red /
// amber vendors they picked. Grouped by category so the UI can
// render a "swap this row" strip per red vendor.
type Swap struct {
	FromSlug     string   `json:"from_slug"`
	Category     string   `json:"category"`
	Alternatives []string `json:"alternatives"`
}

// Severity modifiers tighten the thresholds when the user tells us
// they process one of the higher-risk data categories. In practice:
// an amber vendor gets promoted to red if the user is processing
// health / legal / financial / children's data.
const (
	SeverityHealth     = "health"
	SeverityLegal      = "legal"
	SeverityFinancial  = "financial"
	SeverityChildren   = "children"
)

// Score runs the deterministic scoring function against the
// registry. `slugs` is the vendor set the user picked; `severity`
// is the optional data-sensitivity modifier ("" = standard).
//
// Behavior:
//   - Missing slugs are collected into Report.UnknownSlugs and
//     otherwise ignored.
//   - Exposure percent = (red_count + 0.5*amber_count) / total *
//     100, rounded to nearest int. Amber contributes half so the
//     score responds meaningfully to swaps of amber → green.
//   - Overall report color is the WORST of any card (red if any
//     card is red; amber if none red but ≥1 amber; green if all
//     green). This mirrors the "worst dimension wins" rule the
//     scoring model documents at the per-vendor level.
//   - Severity modifier promotes each vendor card's overall from
//     amber → red BEFORE we compute the count and exposure. This
//     is what "tighten thresholds" means in the plan.
//   - Alternatives are collected from every non-green card,
//     deduped by category so the UI doesn't render the same swap
//     row twice for two red vendors in the same category.
func Score(reg *Registry, slugs []string, severity string) Report {
	rep := Report{SeverityModifier: severity}
	if reg == nil || len(slugs) == 0 {
		rep.Overall = "green"
		return rep
	}

	tighten := isTighteningSeverity(severity)
	seen := make(map[string]bool, len(slugs))
	altsByCategory := make(map[string][]string)
	altsCategoryOrder := make([]string, 0)
	altsFromSlug := make(map[string]string) // category → representative from-slug

	for _, slug := range slugs {
		s := strings.TrimSpace(slug)
		if s == "" {
			continue
		}
		if seen[s] {
			continue // ignore duplicates in caller input
		}
		seen[s] = true
		v := reg.Get(s)
		if v == nil {
			rep.UnknownSlugs = append(rep.UnknownSlugs, s)
			continue
		}
		card := VendorCard{
			Slug:               v.Slug,
			Name:               v.Name,
			Category:           v.Category,
			Overall:            v.Ratings.Overall,
			OneLineReason:      v.OneLineReason,
			ParentJurisdiction: v.ParentJurisdiction,
			Ratings:            v.Ratings,
			SelfDisclosure:     v.SelfDisclosure,
		}
		if tighten && card.Overall == "amber" {
			card.Overall = "red"
		}
		switch card.Overall {
		case "red":
			rep.RedCount++
		case "amber":
			rep.AmberCount++
		case "green":
			rep.GreenCount++
		}
		rep.Cards = append(rep.Cards, card)

		// Collect swap suggestions for non-green vendors. Only
		// remember the first swap group per category so we don't
		// spam the UI with the same alternative list.
		if card.Overall != "green" && len(v.EUAlternatives) > 0 {
			if _, exists := altsByCategory[v.Category]; !exists {
				altsByCategory[v.Category] = v.EUAlternatives
				altsCategoryOrder = append(altsCategoryOrder, v.Category)
				altsFromSlug[v.Category] = v.Slug
			}
		}
	}

	total := rep.RedCount + rep.AmberCount + rep.GreenCount
	if total == 0 {
		rep.Overall = "green"
		return rep
	}

	// Exposure percent — amber contributes half, integer round.
	pct := (float64(rep.RedCount) + 0.5*float64(rep.AmberCount)) / float64(total) * 100
	rep.ExposurePercent = int(pct + 0.5)

	switch {
	case rep.RedCount > 0:
		rep.Overall = "red"
	case rep.AmberCount > 0:
		rep.Overall = "amber"
	default:
		rep.Overall = "green"
	}

	// Deterministic ordering: cards sorted by overall (red first),
	// then by slug within each bucket. Predictable for tests and
	// nicer for the UI (worst offenders on top).
	sort.SliceStable(rep.Cards, func(i, j int) bool {
		a, b := rep.Cards[i], rep.Cards[j]
		if colorRank(a.Overall) != colorRank(b.Overall) {
			return colorRank(a.Overall) > colorRank(b.Overall)
		}
		return a.Slug < b.Slug
	})

	// Build the Swaps list in the category order we first saw
	// non-green vendors — stable for a given input.
	for _, cat := range altsCategoryOrder {
		rep.Alternatives = append(rep.Alternatives, Swap{
			FromSlug:     altsFromSlug[cat],
			Category:     cat,
			Alternatives: altsByCategory[cat],
		})
	}

	// Stable order for UnknownSlugs so tests + shared reports are
	// byte-identical for the same input.
	sort.Strings(rep.UnknownSlugs)

	return rep
}

func isTighteningSeverity(s string) bool {
	switch s {
	case SeverityHealth, SeverityLegal, SeverityFinancial, SeverityChildren:
		return true
	}
	return false
}

func colorRank(c string) int {
	switch c {
	case "red":
		return 2
	case "amber":
		return 1
	}
	return 0
}
