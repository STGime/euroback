package realtime

import "testing"

// A console Viewer's connection can't subscribe to internal-table channels
// (any db:<table>[:…] form); other channels and unrestricted clients are
// unaffected.
func TestClientChannelDenied(t *testing.T) {
	viewer := &Client{denied: map[string]bool{"users": true, "vault_secrets": true}}
	for ch, want := range map[string]bool{
		"db:users":         true,
		"db:users:abc":     true,
		"db:vault_secrets": true,
		"db:todos":         false,
		"db:todos:1":       false,
		"storage:bucket":   false,
		"broadcast:users":  false,
		"db:users_archive": false,
	} {
		if got := viewer.channelDenied(ch); got != want {
			t.Errorf("viewer %q: denied=%v, want %v", ch, got, want)
		}
	}
	if (&Client{}).channelDenied("db:users") {
		t.Error("unrestricted client denied")
	}
}
