// Package sovereignty implements the backend for the CLOUD Act
// Exposure Checker (Tool 1 of the growth spec, issue #548).
//
// The vendor DB is a git submodule at internal/sovereignty/data,
// tracking github.com/STGime/sovereignty-vendors. YAML files under
// data/vendors/*.yaml are embedded at build time via //go:embed, so
// the production binary carries a snapshot of the dataset. Updating
// the dataset is a submodule bump + rebuild.
//
// The scoring function is a deterministic pure function of
// (selected slugs, severity modifier) → Report. It never touches
// the network and never reads the DB — the DB (public.sovereignty_reports)
// only persists the computed score for permalink / OG / PDF re-render,
// see handlers.go.
package sovereignty

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed data/vendors/*.yaml
var vendorFS embed.FS

// Vendor is one entry in the sovereignty-vendors dataset. Field
// tags match the YAML shape defined in the schema:
// github.com/STGime/sovereignty-vendors/schema/vendor.schema.json.
type Vendor struct {
	Slug                     string   `yaml:"slug"`
	Name                     string   `yaml:"name"`
	Category                 string   `yaml:"category"`
	ContractingEntity        string   `yaml:"contracting_entity"`
	UltimateParent           string   `yaml:"ultimate_parent"`
	ParentJurisdiction       string   `yaml:"parent_jurisdiction"`
	EntityJurisdiction       string   `yaml:"entity_jurisdiction"`
	HostingRegions           []string `yaml:"hosting_regions"`
	Subprocessors            []string `yaml:"subprocessors"`
	EUAlternatives           []string `yaml:"eu_alternatives"`
	TransferMechanism        string   `yaml:"transfer_mechanism"`
	OperationalAccessRegions []string `yaml:"operational_access_regions"`
	Ratings                  Ratings  `yaml:"ratings"`
	OneLineReason            string   `yaml:"one_line_reason"`
	Sources                  []string `yaml:"sources"`
	LastReviewed             string   `yaml:"last_reviewed"`
	Notes                    string   `yaml:"notes,omitempty"`
	// SelfDisclosure is true for Eurobase's own vendor entry. The
	// checker's UI must render a visible conflict-of-interest banner
	// on that page.
	SelfDisclosure bool `yaml:"self_disclosure,omitempty"`
}

// Ratings is the 5-dimension + overall score for a vendor. Each
// value is one of "red", "amber", "green".
//
// Explicit json tags — without them, Go marshals field names
// verbatim (PascalCase), which broke consistency with the rest of
// the API's snake_case surface when the ratings block hit the
// wire on /report responses. Keep the YAML tags aligned with the
// sovereignty-vendors dataset shape (snake_case on both sides).
type Ratings struct {
	EntityControl     string `yaml:"entity_control"     json:"entity_control"`
	DataLocation      string `yaml:"data_location"      json:"data_location"`
	OperationalAccess string `yaml:"operational_access" json:"operational_access"`
	SubprocessorChain string `yaml:"subprocessor_chain" json:"subprocessor_chain"`
	TransferMechanism string `yaml:"transfer_mechanism" json:"transfer_mechanism"`
	Overall           string `yaml:"overall"            json:"overall"`
}

// Registry holds the loaded vendor DB. Callers should treat it as
// read-only. Constructed once at process start via LoadEmbedded.
type Registry struct {
	byslug map[string]*Vendor
}

var (
	embeddedOnce sync.Once
	embedded     *Registry
	embeddedErr  error
)

// LoadEmbedded returns a singleton Registry built from the embedded
// vendor YAMLs. Safe to call from multiple goroutines. Returns the
// same *Registry on every call; the second and later returns are
// nil-error even if the first attempt failed (we cache the error
// too — see err field on the struct).
func LoadEmbedded() (*Registry, error) {
	embeddedOnce.Do(func() {
		reg, err := loadFromFS(vendorFS)
		if err != nil {
			embeddedErr = err
			return
		}
		embedded = reg
	})
	if embeddedErr != nil {
		return nil, embeddedErr
	}
	return embedded, nil
}

// loadFromFS reads every *.yaml under data/vendors and builds a
// Registry. Exposed for tests that want to substitute a different
// dataset.
func loadFromFS(fsys fs.FS) (*Registry, error) {
	entries, err := fs.ReadDir(fsys, "data/vendors")
	if err != nil {
		return nil, fmt.Errorf("sovereignty: read vendor dir: %w", err)
	}
	byslug := make(map[string]*Vendor, len(entries))
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		raw, err := fs.ReadFile(fsys, "data/vendors/"+name)
		if err != nil {
			return nil, fmt.Errorf("sovereignty: read %s: %w", name, err)
		}
		var v Vendor
		if err := yaml.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("sovereignty: parse %s: %w", name, err)
		}
		if v.Slug == "" {
			return nil, fmt.Errorf("sovereignty: %s has empty slug", name)
		}
		if _, dup := byslug[v.Slug]; dup {
			return nil, fmt.Errorf("sovereignty: duplicate slug %q (in %s)", v.Slug, name)
		}
		byslug[v.Slug] = &v
	}
	if len(byslug) == 0 {
		return nil, fmt.Errorf("sovereignty: no vendors found in embedded dataset")
	}
	return &Registry{byslug: byslug}, nil
}

// Get returns the vendor with the given slug, or nil if not found.
func (r *Registry) Get(slug string) *Vendor {
	if r == nil {
		return nil
	}
	return r.byslug[slug]
}

// All returns every loaded vendor. Order is not stable — callers
// that need a stable order should sort by Slug themselves.
func (r *Registry) All() []*Vendor {
	if r == nil {
		return nil
	}
	out := make([]*Vendor, 0, len(r.byslug))
	for _, v := range r.byslug {
		out = append(out, v)
	}
	return out
}

// Count returns how many vendors are loaded.
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	return len(r.byslug)
}
