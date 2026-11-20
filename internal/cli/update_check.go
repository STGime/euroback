package cli

// Update notice: once a day at most, after a command has run, the CLI asks
// the platform for the latest released CLI version (GET /v1/meta/releases)
// and prints one line to stderr when a newer version exists. It never
// delays a command by more than a short timeout, stays quiet in CI, when
// stderr isn't a terminal, for development builds, and when
// EUROBASE_NO_UPDATE_CHECK is set.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	updateCheckInterval = 24 * time.Hour
	updateCheckTimeout  = 1500 * time.Millisecond
)

type updateCheckState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
	Install   string    `json:"install,omitempty"`
	URL       string    `json:"url,omitempty"`
}

func updateCheckPath() string {
	return filepath.Join(filepath.Dir(ConfigPath()), "update-check.json")
}

// updateCheckEnabled reports whether a notice may be shown at all.
func updateCheckEnabled(current string) bool {
	if current == "" || current == "dev" || parseVersion(current) == nil {
		return false
	}
	if os.Getenv("EUROBASE_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" {
		return false
	}
	fi, err := os.Stderr.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false // not a terminal (piped, redirected, scripted)
	}
	return true
}

// MaybePrintUpdateNotice refreshes the cached latest version when it is
// older than a day and prints a notice when it is newer than current.
func MaybePrintUpdateNotice(current string) {
	if !updateCheckEnabled(current) {
		return
	}
	state := readUpdateState()
	if time.Since(state.CheckedAt) > updateCheckInterval {
		if fresh, err := fetchLatestCLI(); err == nil {
			state = fresh
		} else {
			// Don't retry on every command while offline.
			state.CheckedAt = time.Now()
		}
		writeUpdateState(state)
	}
	if notice := updateNotice(current, state); notice != "" {
		fmt.Fprintln(os.Stderr, notice)
	}
}

func updateNotice(current string, s updateCheckState) string {
	if s.Latest == "" || !versionLess(current, s.Latest) {
		return ""
	}
	how := s.Install
	if how == "" {
		how = "brew upgrade stgime/tap/eurobase"
	}
	msg := fmt.Sprintf("\nA new Eurobase CLI is available: %s → %s. Upgrade: %s", current, s.Latest, how)
	if s.URL != "" {
		msg += " (or download it from " + s.URL + ")"
	}
	return msg + "\nTurn this notice off with EUROBASE_NO_UPDATE_CHECK=1."
}

func fetchLatestCLI() (updateCheckState, error) {
	apiURL := DefaultAPIURL()
	if cfg, err := LoadConfig(); err == nil && cfg.APIURL != "" {
		apiURL = cfg.APIURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiURL, "/")+"/v1/meta/releases", nil)
	if err != nil {
		return updateCheckState{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return updateCheckState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return updateCheckState{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		CLI *struct {
			Version string `json:"version"`
			Install string `json:"install"`
			URL     string `json:"url"`
		} `json:"cli"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return updateCheckState{}, err
	}
	if body.CLI == nil || parseVersion(body.CLI.Version) == nil {
		return updateCheckState{}, fmt.Errorf("no CLI version in the response")
	}
	return updateCheckState{CheckedAt: time.Now(), Latest: body.CLI.Version, Install: body.CLI.Install, URL: body.CLI.URL}, nil
}

func readUpdateState() updateCheckState {
	var s updateCheckState
	if b, err := os.ReadFile(updateCheckPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func writeUpdateState(s updateCheckState) {
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	p := updateCheckPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o600)
}

// parseVersion reads "1.2.3" (a "-pre" suffix is ignored for ordering
// beyond the numbers). Returns nil when it isn't a version.
func parseVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	core, _, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out[i] = n
	}
	return out
}

// versionLess reports a < b for two versions; false if either isn't one.
func versionLess(a, b string) bool {
	va, vb := parseVersion(a), parseVersion(b)
	if va == nil || vb == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	// Same numbers: a pre-release is older than the release.
	return strings.Contains(a, "-") && !strings.Contains(b, "-")
}
