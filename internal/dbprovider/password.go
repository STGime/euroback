package dbprovider

// Scaleway RDB enforces a password complexity policy on
// user create + password rotate:
//   - length 8..128
//   - ≥1 uppercase, ≥1 lowercase, ≥1 digit, ≥1 special character
//
// Our first attempt used `randomHex(32)` which produces 64 chars of
// [0-9a-f] — no uppercase, no special — and Scaleway rejected with
// 400 `password must be between 8 and 128 characters, contain at
// least one digit, one uppercase, one lowercase and one special
// character` at instance-create time. The provisioning job then
// failed non-retryably and the tenant's Connection tab returned
// 409 "no active dedicated database" forever.
//
// This helper generates a password that MEETS the policy by
// construction: uniform pick from a pool that spans all four
// classes, then explicit overwrites at four random positions to
// guarantee at least one of each class (uniform pool doesn't
// technically guarantee it — a short password could randomly land
// with no digit, for example, and the ~1-in-a-billion Scaleway
// rejection isn't good enough for a provisioning path).
//
// Character pools omit visually-ambiguous characters (I/l/1, O/0)
// on the guarantee positions so an operator reading a password off
// a log or ticket doesn't misread them. The main pool retains all
// characters so entropy stays high.
//
// Specials omit characters that need shell-escaping (`, ', ", \,
// $, backtick, /, spaces) and URL-percent-encoding (?, #, &, +,
// space) so a generated password round-trips through psql,
// bash, Kubernetes secrets, and a DATABASE_URL without surprise.

import (
	"crypto/rand"
	"fmt"
)

const (
	// scalewayPasswordUpper omits I/O to avoid the l/1 and 0/O
	// confusion when a password lands in a support ticket.
	scalewayPasswordUpper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	// scalewayPasswordLower omits l/o for the same reason.
	scalewayPasswordLower = "abcdefghijkmnpqrstuvwxyz"
	// scalewayPasswordDigits omits 0/1 to avoid O/l confusion.
	scalewayPasswordDigits = "23456789"
	// scalewayPasswordSpecial deliberately excludes:
	//   ' " ` \       — shell / SQL literal boundary risk
	//   $             — bash + Postgres string interpolation
	//   / #           — DATABASE_URL path separator + fragment
	//   ? & + space   — URL query/param separators
	//   ( ) [ ] { }   — likely to bite some SIEM regex parser
	// What remains still gives plenty of entropy per position and
	// is safe in every downstream consumer we've encountered.
	scalewayPasswordSpecial = "!*-.:;<=>@^_~"
	scalewayPasswordAll     = scalewayPasswordUpper + scalewayPasswordLower + scalewayPasswordDigits + scalewayPasswordSpecial
)

// RandomScalewayPassword returns a cryptographically random password
// that meets Scaleway RDB's complexity policy. length is the total
// character count; values below 12 are bumped to 12 so we're
// comfortably above Scaleway's 8-char floor and have entropy left
// over after the four guaranteed-class positions consume 4.
//
// Uses crypto/rand exclusively (no math/rand fallback) — a
// predictable DB password is a real security hole, and crypto/rand
// failing on a supported OS is a should-never-happen we'd rather
// surface as an error than paper over.
func RandomScalewayPassword(length int) (string, error) {
	if length < 12 {
		length = 12
	}
	out := make([]byte, length)

	// Step 1: fill every position from the combined pool.
	for i := range out {
		b, err := uniformByte(len(scalewayPasswordAll))
		if err != nil {
			return "", fmt.Errorf("dbprovider: password random: %w", err)
		}
		out[i] = scalewayPasswordAll[b]
	}

	// Step 2: pick four DISTINCT positions and overwrite each with
	// a character from a specific class, guaranteeing all four
	// classes are present regardless of the random uniform pick
	// above. Distinct positions keep the overwrites independent
	// (last-write-wins on collisions would drop one class).
	positions, err := uniquePositions(4, length)
	if err != nil {
		return "", err
	}
	classPools := []string{
		scalewayPasswordUpper,
		scalewayPasswordLower,
		scalewayPasswordDigits,
		scalewayPasswordSpecial,
	}
	for i, pos := range positions {
		b, err := uniformByte(len(classPools[i]))
		if err != nil {
			return "", fmt.Errorf("dbprovider: password random: %w", err)
		}
		out[pos] = classPools[i][b]
	}

	// Step 3: Fisher-Yates shuffle so the guaranteed-class
	// positions aren't at predictable offsets. crypto/rand-backed
	// swaps.
	for i := length - 1; i > 0; i-- {
		j, err := uniformByte(i + 1)
		if err != nil {
			return "", fmt.Errorf("dbprovider: password random: %w", err)
		}
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

// uniformByte returns a byte-valued int in [0, max), unbiased.
// Rejection-sampling over crypto/rand — `b % max` alone would bias
// small values whenever 256 doesn't divide max evenly.
func uniformByte(max int) (int, error) {
	if max <= 0 || max > 256 {
		return 0, fmt.Errorf("dbprovider: uniformByte max out of range: %d", max)
	}
	threshold := 256 - (256 % max)
	var b [1]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < threshold {
			return int(b[0]) % max, nil
		}
	}
}

// uniquePositions returns count distinct indices in [0, length).
// Uses partial Fisher-Yates over a scratch slice — small (count=4)
// so the tiny alloc is fine and the code stays obvious.
func uniquePositions(count, length int) ([]int, error) {
	if count > length {
		return nil, fmt.Errorf("dbprovider: uniquePositions count %d > length %d", count, length)
	}
	pool := make([]int, length)
	for i := range pool {
		pool[i] = i
	}
	for i := 0; i < count; i++ {
		j, err := uniformByte(length - i)
		if err != nil {
			return nil, err
		}
		pool[i], pool[i+j] = pool[i+j], pool[i]
	}
	return pool[:count], nil
}
