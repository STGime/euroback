package mailaddr

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Hash is the key of an address in public.email_bounce_blocks: SHA-256
// of the trimmed, lower-cased address, hex.
func Hash(addr string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(addr))))
	return hex.EncodeToString(h[:])
}
