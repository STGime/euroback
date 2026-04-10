package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateOAuthSecretsToVault scans every project's auth_config for any
// plaintext client_secret values, writes them to the given secret store, and
// strips them from the persisted JSONB. This is an idempotent, one-way
// migration — re-running it after the fact is a no-op because the JSONB no
// longer contains secrets once they've been moved.
//
// Called from gateway startup after the vault is initialized. Safe to run on
// every boot: if the vault isn't configured, the migration logs a warning and
// returns nil (plaintext rows remain untouched until a vault key is provided).
func MigrateOAuthSecretsToVault(ctx context.Context, pool *pgxpool.Pool, store SecretStore) error {
	if store == nil || !store.Configured() {
		slog.Warn("oauth secret migration skipped: vault not configured")
		return nil
	}

	rows, err := pool.Query(ctx,
		`SELECT id, schema_name, auth_config
		 FROM projects
		 WHERE auth_config IS NOT NULL
		   AND auth_config::text LIKE '%client_secret%'`,
	)
	if err != nil {
		return fmt.Errorf("scan projects for oauth secrets: %w", err)
	}
	defer rows.Close()

	type pendingProject struct {
		id         string
		schemaName string
		config     map[string]interface{}
	}
	var pending []pendingProject

	for rows.Next() {
		var p pendingProject
		var raw []byte
		if err := rows.Scan(&p.id, &p.schemaName, &raw); err != nil {
			return fmt.Errorf("scan project row: %w", err)
		}
		if err := json.Unmarshal(raw, &p.config); err != nil {
			slog.Error("oauth migration: invalid auth_config json", "project_id", p.id, "error", err)
			continue
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate project rows: %w", err)
	}

	migrated := 0
	for _, p := range pending {
		oauthRaw, ok := p.config["oauth_providers"]
		if !ok {
			continue
		}
		oauth, ok := oauthRaw.(map[string]interface{})
		if !ok {
			continue
		}

		changed := false
		for providerName, providerRaw := range oauth {
			provider, ok := providerRaw.(map[string]interface{})
			if !ok {
				continue
			}
			secret, hasSecret := provider["client_secret"].(string)
			if !hasSecret || secret == "" {
				continue
			}
			// Write to vault, then strip from JSONB.
			if err := store.SetRaw(ctx, p.schemaName, oauthSecretVaultKey(providerName), secret); err != nil {
				slog.Error("oauth migration: failed to write to vault",
					"project_id", p.id, "provider", providerName, "error", err)
				// Skip this provider but continue with others.
				continue
			}
			delete(provider, "client_secret")
			oauth[providerName] = provider
			changed = true
			slog.Info("oauth secret migrated to vault",
				"project_id", p.id, "provider", providerName)
		}

		if !changed {
			continue
		}
		p.config["oauth_providers"] = oauth

		stripped, err := json.Marshal(p.config)
		if err != nil {
			slog.Error("oauth migration: re-marshal failed", "project_id", p.id, "error", err)
			continue
		}
		if _, err := pool.Exec(ctx,
			`UPDATE projects SET auth_config = $1 WHERE id = $2`,
			stripped, p.id,
		); err != nil {
			slog.Error("oauth migration: update projects failed", "project_id", p.id, "error", err)
			continue
		}
		migrated++
	}

	if migrated > 0 {
		slog.Info("oauth secret migration complete", "projects_migrated", migrated)
	}
	return nil
}
