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

	secretKey, secretKeyHash, err = GenerateSecretKey()
	if err != nil {
		return "", "", "", "", err
	}

	publicKey = "eb_pk_" + pubRandom
	publicKeyHash = hashSHA256(publicKey)

	return publicKey, secretKey, publicKeyHash, secretKeyHash, nil
}

// GenerateSecretKey generates a secret key (eb_sk_…) and its SHA-256 hash.
func GenerateSecretKey() (secretKey, secretKeyHash string, err error) {
	secRandom, err := randomHex(16)
	if err != nil {
		return "", "", fmt.Errorf("generate secret key: %w", err)
	}
	secretKey = "eb_sk_" + secRandom
	return secretKey, hashSHA256(secretKey), nil
}

// lockProjectKeys serialises key changes for one project within tx, so two
// concurrent rotations can't both delete the old row and leave two keys.
func lockProjectKeys(ctx context.Context, tx pgx.Tx, projectID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('api_keys:' || $1::text))`, projectID)
	return err
}

// StoreAPIKeys inserts two rows into the api_keys table within the given
// transaction: one for the public key and one for the secret key. The public
// key is also kept in plaintext (it's public: it ships in every browser app),
// so the console can show it again; the secret key only as a hash.
func StoreAPIKeys(ctx context.Context, tx pgx.Tx, projectID, publicKey string, publicKeyHash, publicKeyPrefix, secretKeyHash, secretKeyPrefix string) error {
	slog.Info("storing api keys", "project_id", projectID)

	_, err := tx.Exec(ctx,
		`INSERT INTO api_keys (project_id, key_hash, key_prefix, type, public_key)
		 VALUES ($1, $2, $3, $4, $5)`,
		projectID, publicKeyHash, publicKeyPrefix, "public", publicKey,
	)
	if err != nil {
		return fmt.Errorf("insert public api key: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO api_keys (project_id, key_hash, key_prefix, type)
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
