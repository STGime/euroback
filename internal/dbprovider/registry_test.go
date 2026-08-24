package dbprovider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubProvider is a Provider used only to test the registry — every
// method is a no-op. Kept lean; concrete provider tests live in
// scaleway_test.go.
type stubProvider struct{ name string }

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Provision(context.Context, ProvisionOpts) (*Instance, error) {
	return nil, nil
}
func (s *stubProvider) Snapshot(context.Context, string, SnapshotOpts) (*Snapshot, error) {
	return nil, nil
}
func (s *stubProvider) ListSnapshots(context.Context, string) ([]Snapshot, error)    { return nil, nil }
func (s *stubProvider) Restore(context.Context, string, RestoreSource) (*Instance, error) {
	return nil, nil
}
func (s *stubProvider) Delete(context.Context, string) error         { return nil }
func (s *stubProvider) Health(context.Context, string) (State, error) { return StateActive, nil }
func (s *stubProvider) Describe(context.Context, string) (*Instance, error) {
	return &Instance{State: StateActive}, nil
}
func (s *stubProvider) SetBackupSchedule(context.Context, string, SetBackupScheduleOpts) error {
	return nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "scaleway"})
	p, err := r.Get("scaleway")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Name() != "scaleway" {
		t.Errorf("name: got %q", p.Name())
	}
}

func TestRegistry_GetUnknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("acmedb")
	if !errors.Is(err, ErrProviderNotRegistered) {
		t.Errorf("unknown: got %v, want ErrProviderNotRegistered", err)
	}
}

// TestRegistry_RejectsNonEUProvider documents the sovereignty guard.
// The panic below is a design contract — do not weaken it without
// updating docs/legal/v2/sub-processors.md and the plan file.
func TestRegistry_RejectsNonEUProvider(t *testing.T) {
	r := NewRegistry()
	defer func() {
		v := recover()
		if v == nil {
			t.Fatal("Register should have panicked on non-EU provider")
		}
		msg, ok := v.(string)
		if !ok || !strings.Contains(msg, "EU-only") {
			t.Errorf("panic message: got %v, want mention of EU-only", v)
		}
	}()
	r.Register(&stubProvider{name: "neon"}) // US-owned
}

func TestRegistry_ListReturnsRegisteredNames(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "scaleway"})
	// ovhcloud is on the EU allow-list but no impl yet — a stub is
	// fine for this test.
	r.Register(&stubProvider{name: "ovhcloud"})
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("List: got %d, want 2", len(got))
	}
	found := map[string]bool{}
	for _, name := range got {
		found[name] = true
	}
	if !found["scaleway"] || !found["ovhcloud"] {
		t.Errorf("List missing entries: %v", got)
	}
}
