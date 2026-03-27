// Package plans provides plan limits, usage tracking, and enforcement for Eurobase projects.
package plans

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PlanLimits represents the resource limits for a billing plan.
type PlanLimits struct {
	Plan             string `json:"plan"`
	DBSizeMB         int    `json:"db_size_mb"`
	StorageMB        int    `json:"storage_mb"`
	BandwidthMB      int    `json:"bandwidth_mb"`
	MAULimit         int    `json:"mau_limit"`
	RateLimitRPS     int    `json:"rate_limit_rps"`
	WSConnections    int    `json:"ws_connections"`
	UploadSizeMB     int    `json:"upload_size_mb"`
	WebhookLimit     int    `json:"webhook_limit"`
	ProjectLimit     int    `json:"project_limit"`
	LogRetentionDays int    `json:"log_retention_days"`
	CustomTemplates  bool   `json:"custom_templates"`
}

// LimitsService provides plan limit lookups with in-memory caching.
type LimitsService struct {
	pool  *pgxpool.Pool
	mu    sync.RWMutex
	cache map[string]*PlanLimits
}

// NewLimitsService creates a new LimitsService backed by the given connection pool.
func NewLimitsService(pool *pgxpool.Pool) *LimitsService {
	return &LimitsService{
		pool:  pool,
		cache: make(map[string]*PlanLimits),
	}
}

// GetLimits returns the limits for the given plan name. Results are cached in memory.
func (s *LimitsService) GetLimits(ctx context.Context, plan string) (*PlanLimits, error) {
	// Check cache first.
	s.mu.RLock()
	if cached, ok := s.cache[plan]; ok {
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	// Query database.
	var l PlanLimits
	err := s.pool.QueryRow(ctx,
		`SELECT plan, db_size_mb, storage_mb, bandwidth_mb, mau_limit,
		        rate_limit_rps, ws_connections, upload_size_mb, webhook_limit,
		        project_limit, log_retention_days, custom_templates
		 FROM plan_limits WHERE plan = $1`, plan,
	).Scan(
		&l.Plan, &l.DBSizeMB, &l.StorageMB, &l.BandwidthMB, &l.MAULimit,
		&l.RateLimitRPS, &l.WSConnections, &l.UploadSizeMB, &l.WebhookLimit,
		&l.ProjectLimit, &l.LogRetentionDays, &l.CustomTemplates,
	)
	if err != nil {
		slog.Error("failed to load plan limits", "plan", plan, "error", err)
		return nil, fmt.Errorf("plan %q not found: %w", plan, err)
	}

	// Cache the result.
	s.mu.Lock()
	s.cache[plan] = &l
	s.mu.Unlock()

	slog.Debug("plan limits loaded", "plan", plan)
	return &l, nil
}

// GetProjectLimits looks up the plan for a project and returns its limits.
func (s *LimitsService) GetProjectLimits(ctx context.Context, projectID string) (*PlanLimits, error) {
	var plan string
	err := s.pool.QueryRow(ctx,
		`SELECT plan FROM projects WHERE id = $1`, projectID,
	).Scan(&plan)
	if err != nil {
		slog.Error("failed to look up project plan", "project_id", projectID, "error", err)
		return nil, fmt.Errorf("project %q not found: %w", projectID, err)
	}

	return s.GetLimits(ctx, plan)
}
