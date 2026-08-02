package dbprovider

import "sync"

// Registry maps a provider name (as stored in
// project_databases.provider) to a Provider implementation.
//
// Only EU-headquartered providers are permitted. Register() panics
// on any non-EU name — making cross-Atlantic misconfigurations loud
// at startup rather than quiet at request time. When you add a new
// provider, extend the allow-list below AFTER confirming the parent
// company is EU-owned.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds a provider by name. Panics if name is not on the
// EU-only allow-list (see euProviders). Safe to call at startup only;
// callers must Register before any Get.
func (r *Registry) Register(p Provider) {
	name := p.Name()
	mustBeEU(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = p
}

// Get looks up a provider by name; returns ErrProviderNotRegistered
// if unknown.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrProviderNotRegistered
	}
	return p, nil
}

// List returns the set of currently-registered provider names.
// Order is not guaranteed. Useful for admin diagnostics.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	return out
}

// EU-only provider allow-list. Add a new entry ONLY after confirming
// the parent company is headquartered in the EU/EEA. See
// docs/legal/v2/sub-processors.md for the current sub-processor list.
//
// Stubs are intentional — they document the extension point without
// requiring a working implementation. When you're ready to add
// (say) Aiven, drop an aiven.go file and register the concrete impl
// from cmd/gateway/main.go; the allow-list entry is already here.
var euProviders = map[string]string{
	"scaleway":    "Scaleway (France)",
	"aiven":       "Aiven (Finland)",      // stub — not yet implemented
	"ovhcloud":    "OVHcloud (France)",    // stub — not yet implemented
	"clevercloud": "CleverCloud (France)", // stub — not yet implemented
	"selfhosted":  "self-hosted (any EU)", // stub — not yet implemented
}

// mustBeEU panics if name is not on the EU-only allow-list.
func mustBeEU(name string) {
	if _, ok := euProviders[name]; !ok {
		panic("dbprovider: refusing to register non-EU provider " + name +
			" — Team-tier is EU-only by design (see internal/dbprovider/registry.go)")
	}
}
