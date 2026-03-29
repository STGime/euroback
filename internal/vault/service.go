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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	sql := fmt.Sprintf(
		`SELECT id, name, description, created_at, updated_at
		 FROM %q.vault_secrets ORDER BY name`,
		schemaName,
	)
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
	sql := fmt.Sprintf(
		`SELECT id, name, secret, nonce, description, created_at, updated_at
		 FROM %q.vault_secrets WHERE name = $1`,
		schemaName,
	)

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

	sql := fmt.Sprintf(
		`INSERT INTO %q.vault_secrets (name, secret, nonce, description)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (name) DO UPDATE SET
		   secret = EXCLUDED.secret,
		   nonce = EXCLUDED.nonce,
		   description = EXCLUDED.description,
		   updated_at = now()
		 RETURNING id, name, description, created_at, updated_at`,
		schemaName,
	)

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

		sql := fmt.Sprintf(
			`UPDATE %q.vault_secrets SET
			   secret = $2,
			   nonce = $3,
			   description = COALESCE($4, description),
			   updated_at = now()
			 WHERE name = $1
			 RETURNING id, name, description, created_at, updated_at`,
			schemaName,
		)

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
	sql := fmt.Sprintf(
		`UPDATE %q.vault_secrets SET
		   description = $2,
		   updated_at = now()
		 WHERE name = $1
		 RETURNING id, name, description, created_at, updated_at`,
		schemaName,
	)

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
	sql := fmt.Sprintf(
		`DELETE FROM %q.vault_secrets WHERE name = $1`,
		schemaName,
	)
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
	sql := fmt.Sprintf(`SELECT count(*) FROM %q.vault_secrets`, schemaName)
	var count int
	err := s.pool.QueryRow(ctx, sql).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count vault secrets: %w", err)
	}
	return count, nil
}
