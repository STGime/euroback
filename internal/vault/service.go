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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	pool *pgxpool.Pool
	key  []byte // 32-byte AES-256 key
}

// NewVaultService creates a new VaultService. The encryptionKey must be a
// base64-encoded 32-byte key for AES-256-GCM.
func NewVaultService(pool *pgxpool.Pool, encryptionKey string) (*VaultService, error) {
	key, err := base64.StdEncoding.DecodeString(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("VAULT_ENCRYPTION_KEY must be valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("VAULT_ENCRYPTION_KEY must be 32 bytes (got %d)", len(key))
	}
	return &VaultService{pool: pool, key: key}, nil
}

// Configured returns true if the vault service is ready to use.
func (s *VaultService) Configured() bool {
	return s != nil && len(s.key) == 32
}

// encrypt using AES-256-GCM.
func (s *VaultService) encrypt(plaintext string) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(s.key)
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

// decrypt using AES-256-GCM.
func (s *VaultService) decrypt(ciphertext, nonce []byte) (string, error) {
	block, err := aes.NewCipher(s.key)
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
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("list vault secrets: %w", err)
	}
	defer rows.Close()

	secrets := make([]Secret, 0)
	for rows.Next() {
		var sec Secret
		if err := rows.Scan(&sec.ID, &sec.Name, &sec.Description, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan vault secret: %w", err)
		}
		secrets = append(secrets, sec)
	}
	return secrets, rows.Err()
}

// Get returns a single decrypted secret by name.
func (s *VaultService) Get(ctx context.Context, schemaName, name string) (*Secret, error) {
	sql := `SELECT id, name, secret, nonce, description, created_at, updated_at
		 FROM ` + vaultTable(schemaName) + ` WHERE name = $1`

	var sec Secret
	var encrypted, nonce []byte
	err := s.pool.QueryRow(ctx, sql, name).Scan(
		&sec.ID, &sec.Name, &encrypted, &nonce,
		&sec.Description, &sec.CreatedAt, &sec.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("secret %q not found", name)
		}
		return nil, fmt.Errorf("get vault secret: %w", err)
	}

	value, err := s.decrypt(encrypted, nonce)
	if err != nil {
		slog.Error("vault decryption failed", "name", name, "error", err)
		return nil, fmt.Errorf("decrypt vault secret: %w", err)
	}
	sec.Value = value

	return &sec, nil
}

// Set creates or updates (upserts) a secret.
func (s *VaultService) Set(ctx context.Context, schemaName, name, value, description string) (*Secret, error) {
	encrypted, nonce, err := s.encrypt(value)
	if err != nil {
		return nil, fmt.Errorf("encrypt vault secret: %w", err)
	}

	sql := `INSERT INTO ` + vaultTable(schemaName) + ` (name, secret, nonce, description)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (name) DO UPDATE SET
		   secret = EXCLUDED.secret,
		   nonce = EXCLUDED.nonce,
		   description = EXCLUDED.description,
		   updated_at = now()
		 RETURNING id, name, description, created_at, updated_at`

	var sec Secret
	err = s.pool.QueryRow(ctx, sql, name, encrypted, nonce, description).Scan(
		&sec.ID, &sec.Name, &sec.Description, &sec.CreatedAt, &sec.UpdatedAt,
	)
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

	// If value is changing, re-encrypt.
	if newValue != nil {
		encrypted, nonce, err := s.encrypt(*newValue)
		if err != nil {
			return nil, fmt.Errorf("encrypt vault secret: %w", err)
		}

		sql := `UPDATE ` + vaultTable(schemaName) + ` SET
			   secret = $2,
			   nonce = $3,
			   description = COALESCE($4, description),
			   updated_at = now()
			 WHERE name = $1
			 RETURNING id, name, description, created_at, updated_at`

		var sec Secret
		err = s.pool.QueryRow(ctx, sql, name, encrypted, nonce, newDescription).Scan(
			&sec.ID, &sec.Name, &sec.Description, &sec.CreatedAt, &sec.UpdatedAt,
		)
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
	err := s.pool.QueryRow(ctx, sql, name, *newDescription).Scan(
		&sec.ID, &sec.Name, &sec.Description, &sec.CreatedAt, &sec.UpdatedAt,
	)
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
	tag, err := s.pool.Exec(ctx, sql, name)
	if err != nil {
		return fmt.Errorf("delete vault secret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("secret %q not found", name)
	}
	return nil
}

// Count returns the number of secrets in a schema.
func (s *VaultService) Count(ctx context.Context, schemaName string) (int, error) {
	sql := `SELECT count(*) FROM ` + vaultTable(schemaName)
	var count int
	err := s.pool.QueryRow(ctx, sql).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count vault secrets: %w", err)
	}
	return count, nil
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
	err := s.pool.QueryRow(ctx, sql, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check vault secret: %w", err)
	}
	return exists, nil
}
