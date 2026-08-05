package dbprovider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ScaleawyConfig — typo-guard: use ScalewayConfig.

// ScalewayConfig carries the settings NewScaleway needs.
//
// Endpoints target Scaleway's RDB (Relational Database) v1 API. The
// client is a straight HTTP wrapper — no SDK — so the dependency
// footprint stays small and mocking with httptest is trivial.
//
// Reference: https://www.scaleway.com/en/developers/api/managed-database-postgre-mysql/
type ScalewayConfig struct {
	// SecretKey is the Scaleway secret-key used in the X-Auth-Token
	// header. Empty is legal at construction time — the client will
	// refuse every request with ErrUnauthorized without hitting the
	// network, matching the vault + mollie packages' shape.
	SecretKey string

	// ProjectID is the Scaleway project UUID (NOT the Eurobase
	// project ID) that new instances are provisioned into. All
	// Team-tier managed-PG instances land under a single Scaleway
	// project so billing + IAM are unified.
	ProjectID string

	// Region is the Scaleway region (e.g. "fr-par"). Locked to
	// fr-par for M1 but passed through per-instance in ProvisionOpts.
	// If empty, provisioning defaults to fr-par.
	DefaultRegion string

	// BaseURL is the Scaleway API root. Left empty in production
	// (defaults to https://api.scaleway.com); set in tests to point
	// at an httptest server. Trailing slash is trimmed.
	BaseURL string

	// HTTPClient lets tests inject a client with a fake transport.
	// Nil uses a client with a 30-second timeout — provisioning
	// calls are async so the response is fast, but the polling
	// margin is generous.
	HTTPClient *http.Client
}

// Scaleway implements Provider against Scaleway's RDB v1 API.
type Scaleway struct {
	secretKey     string
	projectID     string
	defaultRegion string
	baseURL       string
	httpClient    *http.Client
}

const (
	scalewayDefaultBaseURL = "https://api.scaleway.com"
	scalewayEngine         = "PostgreSQL-15"
	scalewayVolumeType     = "bssd" // block SSD; provider default for RDB
	// scalewayDefaultDB is the maintenance database Scaleway creates
	// on every RDB instance. Also used as the DBName on our Instance
	// returns; app DBs are created via CREATE DATABASE later.
	scalewayDefaultDB = "rdb"
	// scalewayMaxInstanceName is Scaleway's slug limit for RDB
	// instance names. Composed names are capped to this.
	scalewayMaxInstanceName = 63
)

// scalewayNodeType maps our coarse Size hint to a Scaleway RDB node
// type. Node names below are Scaleway's SMB catalogue — check the
// dashboard for the current list before adding new sizes.
var scalewayNodeType = map[Size]string{
	SizeSmall:  "db-dev-s",  // 2 vCPU / 4 GB RAM — good for dev / staging
	SizeMedium: "db-gp-s",   // 4 vCPU / 16 GB RAM — production default
	SizeLarge:  "db-gp-m",   // 8 vCPU / 32 GB RAM — high-traffic Team projects
}

// scalewayVolumeSizeGB is the initial storage size mapped from the
// Size hint. Scaleway supports online volume resize so this is a
// starting point, not a ceiling.
var scalewayVolumeSizeGB = map[Size]int{
	SizeSmall:  10,
	SizeMedium: 50,
	SizeLarge:  200,
}

// NewScaleway constructs a Scaleway provider. Cheap — no network
// calls. Safe to call at server startup even when a Scaleway secret
// isn't yet configured; unconfigured clients return
// ErrUnauthorized on every method.
func NewScaleway(cfg ScalewayConfig) *Scaleway {
	s := &Scaleway{
		secretKey:     strings.TrimSpace(cfg.SecretKey),
		projectID:     strings.TrimSpace(cfg.ProjectID),
		defaultRegion: cfg.DefaultRegion,
		baseURL:       strings.TrimRight(cfg.BaseURL, "/"),
	}
	if s.baseURL == "" {
		s.baseURL = scalewayDefaultBaseURL
	}
	if s.defaultRegion == "" {
		s.defaultRegion = "fr-par"
	}
	s.httpClient = cfg.HTTPClient
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return s
}

// Name — see Provider.
func (s *Scaleway) Name() string { return "scaleway" }

// Configured returns true if the client has a secret key + project
// ID. Callers may use this at startup to log a soft warning without
// aborting boot.
func (s *Scaleway) Configured() bool {
	return s.secretKey != "" && s.projectID != ""
}

// ── Wire shapes ────────────────────────────────────────────────────

// scalewayInstance mirrors the shape Scaleway returns for an
// instance resource. Only fields we consume are named; the rest is
// discarded by encoding/json's default behaviour.
type scalewayInstance struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Status     string                    `json:"status"` // ready | provisioning | initializing | error | ...
	Endpoints  []scalewayEndpoint        `json:"endpoints"`
	CreatedAt  time.Time                 `json:"created_at"`
	Region     string                    `json:"region"`
	NodeType   string                    `json:"node_type"`
	Engine     string                    `json:"engine"`
	Tags       []string                  `json:"tags,omitempty"`
	InitSets   []scalewayInstanceSetting `json:"init_settings,omitempty"`
}

type scalewayEndpoint struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Name string `json:"name,omitempty"`
	// Scaleway may return either a plain IP endpoint or a load-
	// balancer hostname; the caller uses whichever is populated.
	Hostname string `json:"hostname,omitempty"`
}

type scalewayInstanceSetting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type scalewayCreateInstanceRequest struct {
	ProjectID       string   `json:"project_id"`
	Name            string   `json:"name"`
	Engine          string   `json:"engine"`
	UserName        string   `json:"user_name"`
	Password        string   `json:"password"`
	NodeType        string   `json:"node_type"`
	IsHaCluster     bool     `json:"is_ha_cluster"`
	DisableBackup   bool     `json:"disable_backup"`
	Tags            []string `json:"tags,omitempty"`
	VolumeType      string   `json:"volume_type"`
	VolumeSize      int64    `json:"volume_size"` // bytes
	BackupSameRegion bool     `json:"backup_same_region"`
}

type scalewayBackup struct {
	ID           string    `json:"id"`
	InstanceID   string    `json:"instance_id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Size         int64     `json:"size"` // bytes
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	SameRegion   bool      `json:"same_region"`
	DatabaseName string    `json:"database_name"`
}

type scalewayBackupList struct {
	TotalCount int              `json:"total_count"`
	Backups    []scalewayBackup `json:"backups"`
}

type scalewayCreateBackupRequest struct {
	InstanceID string `json:"instance_id"`
	// DatabaseName must be non-empty — Scaleway RDB backups are
	// per-database, not per-instance. Snapshot() defaults to the
	// maintenance DB name `rdb` (mapScalewayInstance also uses
	// this as the DBName) so on-demand snapshots cover the DB
	// that carries the tenant's data.
	DatabaseName string `json:"database_name"`
	Name         string `json:"name"`
	// ExpiresAt is a *time.Time (not time.Time) because
	// encoding/json's omitempty never treats a zero-value struct
	// as empty — a plain time.Time field would always serialise
	// as "0001-01-01T00:00:00Z" and Scaleway would either 400 or
	// interpret it as immediate-expiry. See the pattern used in
	// billing/mollie/types.go for the same reason.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type scalewayRestoreBackupRequest struct {
	// InstanceID is optional — omit to spawn a NEW instance from
	// the backup. Two-instance restore always spawns fresh.
	InstanceName string `json:"instance_name"`
	NodeType     string `json:"node_type"`
	IsHaCluster  bool   `json:"is_ha_cluster,omitempty"`
}

type scalewayPITRRequest struct {
	InstanceName string    `json:"instance_name"`
	NodeType     string    `json:"node_type"`
	IsHaCluster  bool      `json:"is_ha_cluster,omitempty"`
	TargetTime   time.Time `json:"target_time"`
}

// ── Provider methods ───────────────────────────────────────────────

// Provision creates a new managed-PG instance in this client's
// region. Returns once Scaleway has assigned an ID; caller polls
// Describe/Health to reach StateActive. When opts.IdempotencyKey is
// non-empty, a retry (same key) returns the previously-created
// instance rather than a duplicate — critical for River-retry
// safety since a leaked instance costs ~€50-500/mo of orphan spend.
func (s *Scaleway) Provision(ctx context.Context, opts ProvisionOpts) (*Instance, error) {
	region := s.defaultRegion
	size := opts.Size
	if size == "" {
		size = SizeMedium
	}
	nodeType, ok := scalewayNodeType[size]
	if !ok {
		return nil, fmt.Errorf("%w: unknown size %q", ErrInvalidRequest, size)
	}
	volGB := scalewayVolumeSizeGB[size]

	// Instance name doubles as the display name in the Scaleway
	// dashboard. Prefix + slug + short random suffix makes it easy
	// to correlate to the Eurobase project while keeping it unique
	// across the account.
	suffix, err := randomHex(4)
	if err != nil {
		return nil, err
	}
	instanceName := fmt.Sprintf("eurobase-%s-%s", sanitizeSlug(opts.Slug), suffix)

	// Root credentials for the instance. Username is fixed so the
	// gateway knows what to open the DSN with; password is a fresh
	// 32-byte random hex the caller seals before writing.
	username := "eurobase_owner"
	password, err := randomHex(32)
	if err != nil {
		return nil, err
	}

	req := scalewayCreateInstanceRequest{
		ProjectID:        s.projectID,
		Name:             instanceName,
		Engine:           scalewayEngine,
		UserName:         username,
		Password:         password,
		NodeType:         nodeType,
		IsHaCluster:      false, // beta default; upgrade path is a follow-up
		DisableBackup:    false, // Scaleway daily backups on by default
		Tags:             []string{"eurobase", "team-tier", "project:" + opts.ProjectID},
		VolumeType:       scalewayVolumeType,
		VolumeSize:       int64(volGB) * 1024 * 1024 * 1024, // GB → bytes
		BackupSameRegion: true,
	}

	var out scalewayInstance
	path := fmt.Sprintf("/rdb/v1/regions/%s/instances", region)
	if err := s.do(ctx, http.MethodPost, path, req, &out, withIdempotency(opts.IdempotencyKey)); err != nil {
		return nil, err
	}
	return mapScalewayInstance(&out, region, username, password), nil
}

// Health — see Provider.
func (s *Scaleway) Health(ctx context.Context, instanceID string) (State, error) {
	inst, err := s.getInstance(ctx, instanceID, s.defaultRegion)
	if err != nil {
		return StateUnknown, err
	}
	return mapScalewayStatus(inst.Status), nil
}

// Describe — see Provider. Password is not populated on the
// returned Instance (Scaleway does not return credentials on GET);
// caller relies on the sealed row in project_databases for that.
func (s *Scaleway) Describe(ctx context.Context, instanceID string) (*Instance, error) {
	inst, err := s.getInstance(ctx, instanceID, s.defaultRegion)
	if err != nil {
		return nil, err
	}
	// "eurobase_owner" is the fixed username set at Provision time —
	// safe to hardcode here since we don't have another source of
	// truth and the Scaleway GET response doesn't include it.
	return mapScalewayInstance(inst, s.defaultRegion, "eurobase_owner", ""), nil
}

func (s *Scaleway) getInstance(ctx context.Context, instanceID, region string) (*scalewayInstance, error) {
	var out scalewayInstance
	path := fmt.Sprintf("/rdb/v1/regions/%s/instances/%s", region, instanceID)
	if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Snapshot — see Provider. Creates an on-demand backup of the
// maintenance DB `rdb` (which is where the tenant's schema lives).
func (s *Scaleway) Snapshot(ctx context.Context, instanceID string) (*Snapshot, error) {
	name, err := randomHex(6)
	if err != nil {
		return nil, err
	}
	req := scalewayCreateBackupRequest{
		InstanceID:   instanceID,
		DatabaseName: scalewayDefaultDB,
		Name:         "eurobase-ondemand-" + name,
	}
	var out scalewayBackup
	path := fmt.Sprintf("/rdb/v1/regions/%s/backups", s.defaultRegion)
	if err := s.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return mapScalewayBackup(&out, SnapshotKindOnDemand), nil
}

// ListSnapshots — see Provider.
func (s *Scaleway) ListSnapshots(ctx context.Context, instanceID string) ([]Snapshot, error) {
	var out scalewayBackupList
	path := fmt.Sprintf("/rdb/v1/regions/%s/backups?instance_id=%s&page_size=100", s.defaultRegion, instanceID)
	if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	snaps := make([]Snapshot, 0, len(out.Backups))
	for i := range out.Backups {
		// Scaleway distinguishes automatic vs manual backups via the
		// Name convention: automatic backups get an "autobackup_"
		// prefix. Ours use "eurobase-ondemand-" — easy to filter.
		kind := SnapshotKindScheduled
		if strings.HasPrefix(out.Backups[i].Name, "eurobase-ondemand-") {
			kind = SnapshotKindOnDemand
		}
		snaps = append(snaps, *mapScalewayBackup(&out.Backups[i], kind))
	}
	return snaps, nil
}

// Restore — see Provider. Spawns a NEW instance from the source.
// Never mutates the source instance.
func (s *Scaleway) Restore(ctx context.Context, instanceID string, source RestoreSource) (*Instance, error) {
	if err := source.Valid(); err != nil {
		return nil, err
	}
	region := s.defaultRegion

	// Look up the source to inherit its node_type (restore requires
	// same-or-larger). Skip HA flag propagation — the beta default
	// is single-node.
	src, err := s.getInstance(ctx, instanceID, region)
	if err != nil {
		return nil, err
	}

	suffix, err := randomHex(4)
	if err != nil {
		return nil, err
	}
	// Shorter marker (`-r-` vs `-restore-`) saves 6 chars vs the
	// obvious composition. Even so, worst-case source names (58
	// chars per Provision + sanitizeSlug's 40-char cap) plus the
	// 12-char suffix would still overflow the 63-char limit, so
	// cap defensively.
	base := strings.TrimSuffix(src.Name, "-")
	restoredName := fmt.Sprintf("%s-r-%s", base, suffix)
	if len(restoredName) > scalewayMaxInstanceName {
		// Trim the source name portion so the composed result
		// exactly hits the limit, then trim any trailing '-'
		// left over from a mid-word cut.
		overflow := len(restoredName) - scalewayMaxInstanceName
		trimmed := base[:len(base)-overflow]
		trimmed = strings.TrimRight(trimmed, "-")
		restoredName = fmt.Sprintf("%s-r-%s", trimmed, suffix)
	}

	if source.SnapshotID != "" {
		req := scalewayRestoreBackupRequest{
			InstanceName: restoredName,
			NodeType:     src.NodeType,
		}
		var out scalewayInstance
		path := fmt.Sprintf("/rdb/v1/regions/%s/backups/%s/restore", region, source.SnapshotID)
		if err := s.do(ctx, http.MethodPost, path, req, &out); err != nil {
			return nil, err
		}
		// Restored instance keeps the source's credentials — we
		// don't get a new password from Scaleway on this path.
		// Caller must rotate via a follow-up step if it wants
		// separate creds. For M1 we return the source's Username
		// with an empty Password; the M4 rotation endpoint handles
		// setting a fresh one.
		return mapScalewayInstance(&out, region, "eurobase_owner", ""), nil
	}

	// PITR path.
	req := scalewayPITRRequest{
		InstanceName: restoredName,
		NodeType:     src.NodeType,
		TargetTime:   source.PITRTarget,
	}
	var out scalewayInstance
	path := fmt.Sprintf("/rdb/v1/regions/%s/instances/%s/renew-pitr", region, instanceID)
	if err := s.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return mapScalewayInstance(&out, region, "eurobase_owner", ""), nil
}

// Delete — see Provider. Idempotent — 404 counts as success.
func (s *Scaleway) Delete(ctx context.Context, instanceID string) error {
	path := fmt.Sprintf("/rdb/v1/regions/%s/instances/%s", s.defaultRegion, instanceID)
	err := s.do(ctx, http.MethodDelete, path, nil, nil)
	if err == nil {
		return nil
	}
	// 404 on delete → already gone. Match ErrNotFound and swallow.
	if isNotFound(err) {
		slog.Info("scaleway: delete on missing instance treated as success", "instance_id", instanceID)
		return nil
	}
	return err
}

// scalewayUpdateUserRequest is the Scaleway RDB PATCH-user body.
// We only ever set the password field; other fields (permissions,
// etc.) are managed at provision time.
type scalewayUpdateUserRequest struct {
	Password string `json:"password"`
}

// RotatePassword — see PasswordRotator. Endpoint:
//   PATCH /rdb/v1/regions/{region}/instances/{instance_id}/users/{name}
// with `{"password":"..."}`. Scaleway invalidates old credentials
// immediately (documented behaviour of the users API).
func (s *Scaleway) RotatePassword(ctx context.Context, instanceID, username, newPassword string) error {
	if newPassword == "" {
		return fmt.Errorf("%w: newPassword required", ErrInvalidRequest)
	}
	if username == "" {
		return fmt.Errorf("%w: username required", ErrInvalidRequest)
	}
	path := fmt.Sprintf("/rdb/v1/regions/%s/instances/%s/users/%s",
		s.defaultRegion, instanceID, username)
	req := scalewayUpdateUserRequest{Password: newPassword}
	return s.do(ctx, http.MethodPatch, path, req, nil)
}

// ── Helpers ────────────────────────────────────────────────────────

// requestOption tunes a single request — currently only for the
// idempotency-key header. Variadic option slice on the do() call
// leaves headroom for other per-call headers without needing to
// bump the signature.
type requestOption func(*http.Request)

// withIdempotency attaches Scaleway's Idempotency-Key header when
// key is non-empty. Scaleway caches the response for the key so a
// retry lands on the same instance instead of creating a duplicate.
func withIdempotency(key string) requestOption {
	return func(r *http.Request) {
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
	}
}

// do is the shared request-execution path — same shape as the mollie
// client. Auth via X-Auth-Token, JSON marshalling, structured error
// classification into the sentinels in errors.go.
func (s *Scaleway) do(ctx context.Context, method, path string, body, out any, opts ...requestOption) error {
	if !s.Configured() {
		return ErrUnauthorized
	}
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("scaleway: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("scaleway: build request: %w", err)
	}
	req.Header.Set("X-Auth-Token", s.secretKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("scaleway: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		slog.Warn("scaleway: non-2xx response",
			"method", method,
			"path", path,
			"status", resp.StatusCode,
			"body", truncate(string(respBody), 400),
		)
		return classifyStatus(resp.StatusCode, respBody)
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("scaleway: decode response: %w", err)
		}
	}
	return nil
}

// classifyStatus maps HTTP status to a sentinel. The Scaleway API
// returns a structured envelope on 4xx/5xx ({"type":"...","message":"..."})
// but we don't currently parse it — the response body goes into slog
// for debugging and the sentinel drives the retry decision.
func classifyStatus(status int, body []byte) error {
	msg := truncate(strings.TrimSpace(string(body)), 200)
	switch {
	case status == 404:
		return fmt.Errorf("%w (body: %s)", ErrNotFound, msg)
	case status == 401 || status == 403:
		return fmt.Errorf("%w (status %d, body: %s)", ErrUnauthorized, status, msg)
	case status == 429:
		return fmt.Errorf("%w (body: %s)", ErrRateLimited, msg)
	case status >= 400 && status < 500:
		return fmt.Errorf("%w (status %d, body: %s)", ErrInvalidRequest, status, msg)
	case status >= 500:
		return fmt.Errorf("%w (status %d, body: %s)", ErrProviderUnavailable, status, msg)
	}
	return fmt.Errorf("scaleway: unexpected status %d (body: %s)", status, msg)
}

// mapScalewayStatus translates Scaleway's own status strings into
// our State enum. Anything unrecognised becomes StateUnknown so
// callers retry rather than treat as terminal.
//
// Scaleway RDB known states: ready, provisioning, initializing,
// configuring, deleting, error, autohealing, locked, snapshotting,
// restarting.
func mapScalewayStatus(status string) State {
	switch strings.ToLower(status) {
	case "ready":
		return StateActive
	case "provisioning", "initializing", "configuring", "autohealing", "snapshotting":
		return StateProvisioning
	case "restarting":
		// Restart mid-lifecycle counts as active for our purposes —
		// the endpoint is coming back up on the same host.
		return StateActive
	case "deleting":
		return StateDeleting
	case "error", "locked":
		return StateFailed
	}
	return StateUnknown
}

func mapScalewayInstance(in *scalewayInstance, region, username, password string) *Instance {
	host, port := pickEndpoint(in.Endpoints)
	return &Instance{
		ProviderID: in.ID,
		Host:       host,
		Port:       port,
		DBName:     "rdb", // Scaleway creates the maintenance DB as `rdb`; app DBs are provisioned via CREATE DATABASE later
		Username:   username,
		Password:   password,
		State:      mapScalewayStatus(in.Status),
		Region:     region,
		CreatedAt:  in.CreatedAt,
	}
}

func mapScalewayBackup(in *scalewayBackup, kind SnapshotKind) *Snapshot {
	return &Snapshot{
		ProviderID: in.ID,
		InstanceID: in.InstanceID,
		Name:       in.Name,
		SizeMB:     in.Size / (1024 * 1024),
		CreatedAt:  in.CreatedAt,
		ExpiresAt:  in.ExpiresAt,
		Kind:       kind,
	}
}

// pickEndpoint chooses the first endpoint that has both a
// host/hostname and port. Scaleway may return several (public IP,
// LB name, private) — we take the first usable one.
func pickEndpoint(eps []scalewayEndpoint) (host string, port int) {
	for _, e := range eps {
		if e.Port == 0 {
			continue
		}
		if e.Hostname != "" {
			return e.Hostname, e.Port
		}
		if e.IP != "" {
			return e.IP, e.Port
		}
	}
	return "", 0
}

// isNotFound peels the sentinel out of a wrapped error. classifyStatus
// wraps 404 with %w so errors.Is works directly.
func isNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("scaleway: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// sanitizeSlug keeps Scaleway's instance-name rules happy: lowercase
// alphanumerics + hyphens, max 63 chars.
func sanitizeSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	if out == "" {
		return "project"
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
