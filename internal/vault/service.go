// Package vault provides encrypted secrets storage for tenant projects.
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// All vault methods run inside a service-role transaction (db.RunAsAuthService)
// because PR #65 (advisory GHSA-wcg9-846j-ch78) tightened the RLS policy
// on tenant_*.vault_secrets to `USING (public.is_service_role())`. The
// previous direct pool queries silently failed RLS after that migration
// applied — closes #71. Same pattern that AuthService uses.

// quoteIdent double-quotes a SQL identifier, escaping embedded double quotes.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// vaultTable returns a fully-qualified reference to the vault_secrets table.
func vaultTable(schemaName string) string {
	return quoteIdent(schemaName) + ".vault_secrets"
}

// Secret represents a vault secret.
type Secret struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Value       string    `json:"value,omitempty"` // only populated on Get, never on List
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// VaultService provides encrypted secret storage backed by PostgreSQL.
type VaultService struct {
	pool     *pgxpool.Pool
	provider KeyProvider
}

// NewVaultService creates a new VaultService. The encryptionKey must be a
// base64-encoded 32-byte master secret. Per-tenant keys are derived from it
// (see KeyProvider / hkdfKeyProvider); the master secret is never used
// directly to seal new rows.
func NewVaultService(pool *pgxpool.Pool, encryptionKey string) (*VaultService, error) {
	key, err := base64.StdEncoding.DecodeString(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("VAULT_ENCRYPTION_KEY must be valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("VAULT_ENCRYPTION_KEY must be 32 bytes (got %d)", len(key))
	}
	return &VaultService{pool: pool, provider: newHKDFKeyProvider(key)}, nil
}

// Configured returns true if the vault service is ready to use.
func (s *VaultService) Configured() bool {
	return s != nil && s.provider != nil
}

// seal encrypts plaintext for a tenant using the provider's current key
// version, returning the ciphertext, nonce, and the version used so the
// caller can persist it alongside the row.
func (s *VaultService) seal(ctx context.Context, schemaName, plaintext string) (ciphertext, nonce []byte, version int16, err error) {
	version = s.provider.CurrentVersion()
	key, err := s.provider.DeriveKey(ctx, schemaName, version)
	if err != nil {
		return nil, nil, 0, err
	}
	ciphertext, nonce, err = encryptWith(key, plaintext)
	return ciphertext, nonce, version, err
}

// open decrypts a row's ciphertext using the key for the version it was
// sealed with.
func (s *VaultService) open(ctx context.Context, schemaName string, ciphertext, nonce []byte, version int16) (string, error) {
	key, err := s.provider.DeriveKey(ctx, schemaName, version)
	if err != nil {
		return "", err
	}
	return decryptWith(key, ciphertext, nonce)
}

// encryptWith seals plaintext under a 32-byte AES-256-GCM key.
func encryptWith(key []byte, plaintext string) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, nonce, nil
}

// decryptWith opens AES-256-GCM ciphertext under the given key.
func decryptWith(key, ciphertext, nonce []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed (wrong key or corrupted data)")
	}
	return string(plaintext), nil
}

// List returns all secret names (NOT values) for a project schema.
func (s *VaultService) List(ctx context.Context, schemaName string) ([]Secret, error) {
	sql := `SELECT id, name, description, created_at, updated_at
		 FROM ` + vaultTable(schemaName) + ` ORDER BY name`
	secrets := make([]Secret, 0)
	err := db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return fmt.Errorf("list vault secrets: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var sec Secret
			if err := rows.Scan(&sec.ID, &sec.Name, &sec.Description, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
				return fmt.Errorf("scan vault secret: %w", err)
			}
			secrets = append(secrets, sec)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return secrets, nil
}

// Get returns a single decrypted secret by name.
func (s *VaultService) Get(ctx context.Context, schemaName, name string) (*Secret, error) {
	sql := `SELECT id, name, secret, nonce, key_version, description, created_at, updated_at
		 FROM ` + vaultTable(schemaName) + ` WHERE name = $1`

	var sec Secret
	var encrypted, nonce []byte
	var keyVersion int16
	err := db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, name).Scan(
			&sec.ID, &sec.Name, &encrypted, &nonce, &keyVersion,
			&sec.Description, &sec.CreatedAt, &sec.UpdatedAt,
		)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("secret %q not found", name)
		}
		return nil, fmt.Errorf("get vault secret: %w", err)
	}

	value, err := s.open(ctx, schemaName, encrypted, nonce, keyVersion)
	if err != nil {
		slog.Error("vault decryption failed", "name", name, "key_version", keyVersion, "error", err)
		return nil, fmt.Errorf("decrypt vault secret: %w", err)
	}
	sec.Value = value

	return &sec, nil
}

// Set creates or updates (upserts) a secret.
func (s *VaultService) Set(ctx context.Context, schemaName, name, value, description string) (*Secret, error) {
	encrypted, nonce, version, err := s.seal(ctx, schemaName, value)
	if err != nil {
		return nil, fmt.Errorf("encrypt vault secret: %w", err)
	}

	sql := `INSERT INTO ` + vaultTable(schemaName) + ` (name, secret, nonce, key_version, description)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (name) DO UPDATE SET
		   secret = EXCLUDED.secret,
		   nonce = EXCLUDED.nonce,
		   key_version = EXCLUDED.key_version,
		   description = EXCLUDED.description,
		   updated_at = now()
		 RETURNING id, name, description, created_at, updated_at`

	var sec Secret
	err = db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, name, encrypted, nonce, version, description).Scan(
			&sec.ID, &sec.Name, &sec.Description, &sec.CreatedAt, &sec.UpdatedAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("upsert vault secret: %w", err)
	}

	return &sec, nil
}

// Update updates a secret's value and/or description.
func (s *VaultService) Update(ctx context.Context, schemaName, name string, newValue *string, newDescription *string) (*Secret, error) {
	if newValue == nil && newDescription == nil {
		return nil, fmt.Errorf("nothing to update")
	}

	// If value is changing, re-encrypt (at the current key version).
	if newValue != nil {
		encrypted, nonce, version, err := s.seal(ctx, schemaName, *newValue)
		if err != nil {
			return nil, fmt.Errorf("encrypt vault secret: %w", err)
		}

		sql := `UPDATE ` + vaultTable(schemaName) + ` SET
			   secret = $2,
			   nonce = $3,
			   key_version = $4,
			   description = COALESCE($5, description),
			   updated_at = now()
			 WHERE name = $1
			 RETURNING id, name, description, created_at, updated_at`

		var sec Secret
		err = db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, sql, name, encrypted, nonce, version, newDescription).Scan(
				&sec.ID, &sec.Name, &sec.Description, &sec.CreatedAt, &sec.UpdatedAt,
			)
		})
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil, fmt.Errorf("secret %q not found", name)
			}
			return nil, fmt.Errorf("update vault secret: %w", err)
		}
		return &sec, nil
	}

	// Only description changing.
	sql := `UPDATE ` + vaultTable(schemaName) + ` SET
		   description = $2,
		   updated_at = now()
		 WHERE name = $1
		 RETURNING id, name, description, created_at, updated_at`

	var sec Secret
	err := db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, name, *newDescription).Scan(
			&sec.ID, &sec.Name, &sec.Description, &sec.CreatedAt, &sec.UpdatedAt,
		)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("secret %q not found", name)
		}
		return nil, fmt.Errorf("update vault secret: %w", err)
	}
	return &sec, nil
}

// Delete removes a secret by name.
func (s *VaultService) Delete(ctx context.Context, schemaName, name string) error {
	sql := `DELETE FROM ` + vaultTable(schemaName) + ` WHERE name = $1`
	var rowsAffected int64
	err := db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, name)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete vault secret: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("secret %q not found", name)
	}
	return nil
}

// Count returns the number of secrets in a schema.
func (s *VaultService) Count(ctx context.Context, schemaName string) (int, error) {
	sql := `SELECT count(*) FROM ` + vaultTable(schemaName)
	var count int
	err := db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql).Scan(&count)
	})
	if err != nil {
		return 0, fmt.Errorf("count vault secrets: %w", err)
	}
	return count, nil
}

// RekeySchema re-encrypts every secret in a tenant schema under the
// provider's current key version. Rows already at the current version are
// skipped. It returns the number of rows re-encrypted.
//
// This is the rotation path: bump the provider's current version (or swap in
// a new KeyProvider) and run RekeySchema to migrate historic ciphertext —
// including legacy version-0 rows still sealed with the shared master key —
// onto the new per-tenant key. The whole schema is rekeyed in a single
// service-role transaction so it is all-or-nothing.
//
// Vaults hold a small number of secrets per tenant (plan-capped, plus a
// handful of machine-managed entries), so a synchronous re-encrypt is cheap.
// If a tenant ever holds enough secrets that this blocks the request, move it
// to a River job.
func (s *VaultService) RekeySchema(ctx context.Context, schemaName string) (int, error) {
	target := s.provider.CurrentVersion()
	selectSQL := `SELECT id, secret, nonce, key_version FROM ` + vaultTable(schemaName) +
		` WHERE key_version <> $1 FOR UPDATE`
	updateSQL := `UPDATE ` + vaultTable(schemaName) +
		` SET secret = $2, nonce = $3, key_version = $4, updated_at = now() WHERE id = $1`

	var rekeyed int
	err := db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectSQL, target)
		if err != nil {
			return fmt.Errorf("select secrets for rekey: %w", err)
		}
		type row struct {
			id         string
			ciphertext []byte
			nonce      []byte
			version    int16
		}
		var pending []row
		for rows.Next() {
			var rw row
			if err := rows.Scan(&rw.id, &rw.ciphertext, &rw.nonce, &rw.version); err != nil {
				rows.Close()
				return fmt.Errorf("scan secret for rekey: %w", err)
			}
			pending = append(pending, rw)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, rw := range pending {
			plaintext, err := s.open(ctx, schemaName, rw.ciphertext, rw.nonce, rw.version)
			if err != nil {
				return fmt.Errorf("decrypt secret %s (v%d) during rekey: %w", rw.id, rw.version, err)
			}
			newCT, newNonce, newVersion, err := s.seal(ctx, schemaName, plaintext)
			if err != nil {
				return fmt.Errorf("re-encrypt secret %s during rekey: %w", rw.id, err)
			}
			if _, err := tx.Exec(ctx, updateSQL, rw.id, newCT, newNonce, newVersion); err != nil {
				return fmt.Errorf("update secret %s during rekey: %w", rw.id, err)
			}
			rekeyed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return rekeyed, nil
}

// ── Raw helpers (no Secret struct, no description) ──
//
// These helpers are used by the tenant package (and other internal callers)
// to store machine-managed secrets like OAuth client secrets. They share the
// same vault_secrets table and encryption, but skip the user-facing metadata.

// SetRaw upserts a secret with an empty description. Equivalent to calling
// Set(ctx, schemaName, name, value, "") but returns only an error.
func (s *VaultService) SetRaw(ctx context.Context, schemaName, name, value string) error {
	_, err := s.Set(ctx, schemaName, name, value, "")
	return err
}

// GetRaw returns the decrypted value for a given secret name. Returns an
// empty string and no error if the secret does not exist (useful for
// "maybe-present" checks in callers that already know the name).
func (s *VaultService) GetRaw(ctx context.Context, schemaName, name string) (string, error) {
	sec, err := s.Get(ctx, schemaName, name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return "", nil
		}
		return "", err
	}
	return sec.Value, nil
}

// DeleteRaw removes a secret by name. If the secret does not exist, returns
// nil (idempotent — used by "disable provider" flows that shouldn't fail
// when nothing was stored yet).
func (s *VaultService) DeleteRaw(ctx context.Context, schemaName, name string) error {
	if err := s.Delete(ctx, schemaName, name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return err
	}
	return nil
}

// HasRaw reports whether a secret with the given name exists in the schema,
// without decrypting it. Used by "secret_set" annotations in API responses.
func (s *VaultService) HasRaw(ctx context.Context, schemaName, name string) (bool, error) {
	sql := `SELECT EXISTS(SELECT 1 FROM ` + vaultTable(schemaName) + ` WHERE name = $1)`
	var exists bool
	err := db.RunAsAuthService(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, name).Scan(&exists)
	})
	if err != nil {
		return false, fmt.Errorf("check vault secret: %w", err)
	}
	return exists, nil
}
