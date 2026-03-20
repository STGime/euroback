package tenant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// GenerateAPIKeyPair generates a pair of cryptographically random API keys
// (public and secret) along with their SHA-256 hashes for storage.
// The plaintext keys are returned to be shown to the user once; only the hashes
// are persisted in the database.
func GenerateAPIKeyPair() (publicKey, secretKey, publicKeyHash, secretKeyHash string, err error) {
	pubRandom, err := randomHex(16) // 16 bytes = 32 hex chars
	if err != nil {
		return "", "", "", "", fmt.Errorf("generate public key: %w", err)
	}

	secRandom, err := randomHex(16)
	if err != nil {
		return "", "", "", "", fmt.Errorf("generate secret key: %w", err)
	}

	publicKey = "eb_pk_" + pubRandom
	secretKey = "eb_sk_" + secRandom

	publicKeyHash = hashSHA256(publicKey)
	secretKeyHash = hashSHA256(secretKey)

	return publicKey, secretKey, publicKeyHash, secretKeyHash, nil
}

// StoreAPIKeys inserts two rows into the api_keys table within the given
// transaction: one for the public key and one for the secret key.
func StoreAPIKeys(ctx context.Context, tx pgx.Tx, projectID string, publicKeyHash, publicKeyPrefix, secretKeyHash, secretKeyPrefix string) error {
	slog.Info("storing api keys", "project_id", projectID)

	_, err := tx.Exec(ctx,
		`INSERT INTO api_keys (project_id, key_hash, key_prefix, key_type)
		 VALUES ($1, $2, $3, $4)`,
		projectID, publicKeyHash, publicKeyPrefix, "public",
	)
	if err != nil {
		return fmt.Errorf("insert public api key: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO api_keys (project_id, key_hash, key_prefix, key_type)
		 VALUES ($1, $2, $3, $4)`,
		projectID, secretKeyHash, secretKeyPrefix, "secret",
	)
	if err != nil {
		return fmt.Errorf("insert secret api key: %w", err)
	}

	slog.Info("api keys stored", "project_id", projectID)
	return nil
}

// randomHex generates n cryptographically random bytes and returns them as a hex string.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashSHA256 returns the hex-encoded SHA-256 hash of the input string.
func hashSHA256(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}
