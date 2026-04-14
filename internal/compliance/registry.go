// Package compliance provides GDPR DPA compliance reporting for Eurobase projects.
package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SubProcessor represents a third-party data processor in the Eurobase stack.
type SubProcessor struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	LegalEntity       string   `json:"legal_entity"`
	Country           string   `json:"country"`
	CountryCode       string   `json:"country_code"`
	Jurisdiction      string   `json:"jurisdiction"`
	Service           string   `json:"service"`
	Purpose           string   `json:"purpose"`
	DataCategories    []string `json:"data_categories"`
	DataSubjects      string   `json:"data_subjects"`
	TransferMechanism string   `json:"transfer_mechanism"`
	SecurityCerts     []string `json:"security_certs"`
	DPAUrl            string   `json:"dpa_url,omitempty"`
	PrivacyUrl        string   `json:"privacy_url,omitempty"`
	CloudActRisk      bool     `json:"cloud_act_risk"`
	AddedAt           time.Time `json:"added_at"`
}

// ComplianceService provides methods for querying the sub-processor registry
// and generating GDPR compliance reports.
type ComplianceService struct {
	pool *pgxpool.Pool
}

// NewComplianceService creates a new ComplianceService backed by the given pool.
func NewComplianceService(pool *pgxpool.Pool) *ComplianceService {
	return &ComplianceService{pool: pool}
}

// ListAllSubProcessors returns all active sub-processors.
func (s *ComplianceService) ListAllSubProcessors(ctx context.Context) ([]SubProcessor, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, legal_entity, country, country_code, jurisdiction,
		       service, purpose, data_categories, data_subjects,
		       transfer_mechanism, COALESCE(security_certs, '{}'),
		       COALESCE(dpa_url, ''), COALESCE(privacy_url, ''),
		       cloud_act_risk, added_at
		FROM sub_processors
		WHERE active = true
		ORDER BY name, service
	`)
	if err != nil {
		return nil, fmt.Errorf("querying sub_processors: %w", err)
	}
	defer rows.Close()

	var result []SubProcessor
	for rows.Next() {
		var sp SubProcessor
		if err := rows.Scan(
			&sp.ID, &sp.Name, &sp.LegalEntity, &sp.Country, &sp.CountryCode,
			&sp.Jurisdiction, &sp.Service, &sp.Purpose, &sp.DataCategories,
			&sp.DataSubjects, &sp.TransferMechanism, &sp.SecurityCerts,
			&sp.DPAUrl, &sp.PrivacyUrl, &sp.CloudActRisk, &sp.AddedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning sub_processor row: %w", err)
		}
		result = append(result, sp)
	}
	return result, rows.Err()
}

// projectConfig holds the fields we need to determine which features a project uses.
type projectConfig struct {
	ID         string
	Name       string
	Slug       string
	Plan       string
	S3Bucket   string
	AuthConfig json.RawMessage
	Settings   json.RawMessage
}

// getProjectConfig fetches the project configuration needed for feature detection.
func (s *ComplianceService) getProjectConfig(ctx context.Context, projectID string) (*projectConfig, error) {
	var pc projectConfig
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, slug, COALESCE(plan, 'free'),
		       COALESCE(s3_bucket, ''), COALESCE(auth_config, '{}'), COALESCE(settings, '{}')
		FROM projects
		WHERE id = $1
	`, projectID).Scan(&pc.ID, &pc.Name, &pc.Slug, &pc.Plan, &pc.S3Bucket, &pc.AuthConfig, &pc.Settings)
	if err != nil {
		return nil, fmt.Errorf("fetching project %s: %w", projectID, err)
	}
	return &pc, nil
}

// resolveActiveFeatures determines which Eurobase features the project is using.
func (s *ComplianceService) resolveActiveFeatures(ctx context.Context, pc *projectConfig) []string {
	// Always-on features.
	features := []string{"database", "compute", "cache"}

	// Storage: every project gets an S3 bucket, so it's always active.
	if pc.S3Bucket != "" {
		features = append(features, "storage")
	}

	// Email: check if project has email templates configured or auth requires email.
	var emailTemplateCount int
	_ = s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM email_templates WHERE project_id = $1
	`, pc.ID).Scan(&emailTemplateCount)
	if emailTemplateCount > 0 {
		features = append(features, "email")
	} else {
		// Also enable email if auth_config requests email confirmation.
		var authCfg struct {
			RequireEmailConfirmation bool `json:"require_email_confirmation"`
		}
		if json.Unmarshal(pc.AuthConfig, &authCfg) == nil && authCfg.RequireEmailConfirmation {
			features = append(features, "email")
		}
	}

	// Billing: if plan is not free.
	if pc.Plan != "free" {
		features = append(features, "billing")
	}

	// OAuth providers and phone auth.
	var authCfg struct {
		Providers map[string]struct {
			Enabled bool `json:"enabled"`
		} `json:"providers"`
		OAuthProviders map[string]struct {
			Enabled bool `json:"enabled"`
		} `json:"oauth_providers"`
	}
	if json.Unmarshal(pc.AuthConfig, &authCfg) == nil {
		if authCfg.OAuthProviders != nil {
			if p, ok := authCfg.OAuthProviders["google"]; ok && p.Enabled {
				features = append(features, "oauth_google")
			}
			if p, ok := authCfg.OAuthProviders["github"]; ok && p.Enabled {
				features = append(features, "oauth_github")
			}
		}
		if authCfg.Providers != nil {
			if p, ok := authCfg.Providers["phone"]; ok && p.Enabled {
				features = append(features, "sms")
			}
		}
	}

	return features
}

// GetActiveSubProcessors returns the sub-processors that are active for a specific project,
// based on which features the project has enabled.
func (s *ComplianceService) GetActiveSubProcessors(ctx context.Context, projectID string) ([]SubProcessor, error) {
	pc, err := s.getProjectConfig(ctx, projectID)
	if err != nil {
		return nil, err
	}

	features := s.resolveActiveFeatures(ctx, pc)
	if len(features) == 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (sp.id)
		       sp.id, sp.name, sp.legal_entity, sp.country, sp.country_code,
		       sp.jurisdiction, sp.service, sp.purpose, sp.data_categories,
		       sp.data_subjects, sp.transfer_mechanism,
		       COALESCE(sp.security_certs, '{}'),
		       COALESCE(sp.dpa_url, ''), COALESCE(sp.privacy_url, ''),
		       sp.cloud_act_risk, sp.added_at
		FROM sub_processors sp
		JOIN service_dependencies sd ON sd.sub_processor_id = sp.id
		WHERE sp.active = true
		  AND sd.eurobase_feature = ANY($1)
		ORDER BY sp.id
	`, features)
	if err != nil {
		return nil, fmt.Errorf("querying active sub_processors: %w", err)
	}
	defer rows.Close()

	var result []SubProcessor
	for rows.Next() {
		var sp SubProcessor
		if err := rows.Scan(
			&sp.ID, &sp.Name, &sp.LegalEntity, &sp.Country, &sp.CountryCode,
			&sp.Jurisdiction, &sp.Service, &sp.Purpose, &sp.DataCategories,
			&sp.DataSubjects, &sp.TransferMechanism, &sp.SecurityCerts,
			&sp.DPAUrl, &sp.PrivacyUrl, &sp.CloudActRisk, &sp.AddedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning active sub_processor row: %w", err)
		}
		result = append(result, sp)
	}
	return result, rows.Err()
}
