package functions

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// HMAC for runner → gateway internal storage RPC traffic. Mirrors the
// existing gateway → runner scheme in hmac.go but signs a different
// canonical message and uses different header names so signatures
// across the two directions can't be confused.
//
// Why a separate scheme: the storage RPC carries different signed
// fields (storage key, body hash) and runs in the opposite direction.
// Sharing one canonical would either bake every possible field into
// every signature (brittle) or special-case by op (hard to reason
// about). A second tiny scheme is cheaper.

const (
	storageHeaderTimestamp = "X-Eurobase-Storage-Timestamp"
	storageHeaderSignature = "X-Eurobase-Storage-Signature"
)

// StorageOp identifies which runner→gateway storage operation is being
// signed. Included in the canonical so a signature for one op can't be
// replayed against another endpoint.
type StorageOp string

const (
	StorageOpUpload    StorageOp = "storage_upload"
	StorageOpSignedURL StorageOp = "storage_signed_url"
	StorageOpDelete    StorageOp = "storage_delete"
)

// SignStorage attaches the storage timestamp + signature headers to h.
// bodyHash must be the lowercase-hex SHA-256 of the request body
// (empty body → SHA-256 of the empty string).
func SignStorage(secret []byte, h http.Header, op StorageOp, bodyHashHex string, now time.Time) {
	ts := strconv.FormatInt(now.Unix(), 10)
	h.Set(storageHeaderTimestamp, ts)
	msg := storageCanonicalMessage(h, op, ts, bodyHashHex)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	h.Set(storageHeaderSignature, hex.EncodeToString(mac.Sum(nil)))
}

// VerifyStorage checks the timestamp + signature on an incoming storage
// RPC. bodyHash is the verifier's locally computed SHA-256 of the body
// — the comparison fails if the runner's signed hash doesn't match,
// catching body tampering between runner and gateway.
//
// Returns ErrMissingSignature, ErrTimestampOutOfWindow, or
// ErrSignatureMismatch (sentinel values from hmac.go) so callers can
// distinguish missing headers from tampered ones.
func VerifyStorage(secret []byte, h http.Header, op StorageOp, bodyHashHex string, opts VerifyOptions) error {
	skew := opts.MaxClockSkew
	if skew == 0 {
		skew = 5 * time.Minute
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	ts := h.Get(storageHeaderTimestamp)
	sig := h.Get(storageHeaderSignature)
	if ts == "" || sig == "" {
		return ErrMissingSignature
	}

	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: invalid timestamp", ErrTimestampOutOfWindow)
	}
	delta := now().Unix() - tsInt
	if delta < 0 {
		delta = -delta
	}
	if time.Duration(delta)*time.Second > skew {
		return ErrTimestampOutOfWindow
	}

	expected := hmac.New(sha256.New, secret)
	expected.Write([]byte(storageCanonicalMessage(h, op, ts, bodyHashHex)))
	expectedHex := hex.EncodeToString(expected.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedHex)) {
		return ErrSignatureMismatch
	}
	return nil
}

// SHA256Hex returns the lowercase hex SHA-256 of body.
func SHA256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Format:
//
//	v=1
//	op=<storage_upload|storage_signed_url|storage_delete>
//	ts=<unix-seconds>
//	project=<X-Project-ID>
//	schema=<X-Schema-Name>
//	user=<X-User-ID>
//	storage_key=<X-Storage-Key>
//	content_sha256=<hex>
//
// Newlines are LF. Empty values stay as empty strings (never omitted)
// so a forged header that adds an unset value can't mutate the
// canonical form to match a different signature.
func storageCanonicalMessage(h http.Header, op StorageOp, ts, bodyHashHex string) string {
	return "v=1\n" +
		"op=" + string(op) + "\n" +
		"ts=" + ts + "\n" +
		"project=" + h.Get("X-Project-ID") + "\n" +
		"schema=" + h.Get("X-Schema-Name") + "\n" +
		"user=" + h.Get("X-User-ID") + "\n" +
		"storage_key=" + h.Get("X-Storage-Key") + "\n" +
		"content_sha256=" + bodyHashHex
}

