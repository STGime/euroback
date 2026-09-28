package tenant

import (
	"encoding/json"
	"testing"
)

// #689: secret_set true/false when the vault answered; secret_status
// "unknown" (and no secret_set) when it couldn't be read; a stored
// client_secret never leaves.
func TestAnnotateOAuthSecretStatus(t *testing.T) {
	raw := []byte(`{"oauth_providers":{
		"github":{"enabled":true,"client_id":"a","client_secret":"leak"},
		"google":{"enabled":true,"client_id":"b","secret_set":true},
		"apple":{"enabled":true,"client_id":"c","secret_status":"unknown"}}}`)
	out := AnnotateOAuthSecretStatus(raw, func(provider string) (bool, bool) {
		switch provider {
		case "github":
			return true, true
		case "google":
			return false, false // vault unreachable
		default:
			return false, true
		}
	})
	var cfg struct {
		OAuth map[string]map[string]any `json:"oauth_providers"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if gh := cfg.OAuth["github"]; gh["secret_set"] != true || gh["secret_status"] != nil || gh["client_secret"] != nil {
		t.Errorf("github: %v", gh)
	}
	if g := cfg.OAuth["google"]; g["secret_set"] != nil || g["secret_status"] != "unknown" {
		t.Errorf("google (vault unreachable): %v, want secret_status unknown and no secret_set", g)
	}
	if a := cfg.OAuth["apple"]; a["secret_set"] != false || a["secret_status"] != nil {
		t.Errorf("apple: %v, want secret_set false and a stale secret_status removed", a)
	}
}
