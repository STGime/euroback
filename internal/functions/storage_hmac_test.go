package functions

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testStorageSecret = "01234567890123456789012345678901" // 32 bytes

func makeStorageHeader(projectID, schemaName, userID, storageKey string) http.Header {
	h := http.Header{}
	h.Set("X-Project-ID", projectID)
	h.Set("X-Schema-Name", schemaName)
	h.Set("X-User-ID", userID)
	h.Set("X-Storage-Key", storageKey)
	return h
}

func TestSignVerifyStorage_Roundtrip(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	h := makeStorageHeader("p-1", "tenant_x", "u-1", "moodboards/abc/0.png")
	now := time.Unix(1_700_000_000, 0)

	SignStorage([]byte(testStorageSecret), h, StorageOpUpload, SHA256Hex(body), now)

	if h.Get("X-Eurobase-Storage-Timestamp") == "" {
		t.Fatal("expected timestamp header to be set")
	}
	if h.Get("X-Eurobase-Storage-Signature") == "" {
		t.Fatal("expected signature header to be set")
	}

	if err := VerifyStorage([]byte(testStorageSecret), h, StorageOpUpload, SHA256Hex(body), VerifyOptions{Now: func() time.Time { return now }}); err != nil {
		t.Errorf("verify failed: %v", err)
	}
}

func TestVerifyStorage_RejectsBodyTamper(t *testing.T) {
	body := []byte("original")
	h := makeStorageHeader("p-1", "tenant_x", "u-1", "k")
	now := time.Unix(1_700_000_000, 0)
	SignStorage([]byte(testStorageSecret), h, StorageOpUpload, SHA256Hex(body), now)

	tampered := []byte("modified")
	err := VerifyStorage([]byte(testStorageSecret), h, StorageOpUpload, SHA256Hex(tampered), VerifyOptions{Now: func() time.Time { return now }})
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("expected ErrSignatureMismatch on body tamper, got %v", err)
	}
}

func TestVerifyStorage_RejectsHeaderTamper(t *testing.T) {
	body := []byte("x")
	h := makeStorageHeader("p-1", "tenant_x", "u-1", "k")
	now := time.Unix(1_700_000_000, 0)
	SignStorage([]byte(testStorageSecret), h, StorageOpUpload, SHA256Hex(body), now)

	// Swap project ID after signing — should break the signature.
	h.Set("X-Project-ID", "p-evil")
	err := VerifyStorage([]byte(testStorageSecret), h, StorageOpUpload, SHA256Hex(body), VerifyOptions{Now: func() time.Time { return now }})
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("expected ErrSignatureMismatch on project tamper, got %v", err)
	}
}

func TestVerifyStorage_RejectsCrossOpReplay(t *testing.T) {
	// Sign as upload, verify as signed_url — must reject so a runner
	// can't replay an upload signature against the signed-url endpoint.
	body := []byte("x")
	h := makeStorageHeader("p-1", "tenant_x", "u-1", "k")
	now := time.Unix(1_700_000_000, 0)
	SignStorage([]byte(testStorageSecret), h, StorageOpUpload, SHA256Hex(body), now)

	err := VerifyStorage([]byte(testStorageSecret), h, StorageOpSignedURL, SHA256Hex(body), VerifyOptions{Now: func() time.Time { return now }})
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("expected ErrSignatureMismatch on cross-op replay, got %v", err)
	}
}

func TestVerifyStorage_RejectsClockSkew(t *testing.T) {
	body := []byte("x")
	h := makeStorageHeader("p-1", "tenant_x", "u-1", "k")
	signedAt := time.Unix(1_700_000_000, 0)
	SignStorage([]byte(testStorageSecret), h, StorageOpDelete, SHA256Hex(body), signedAt)

	verifyAt := signedAt.Add(10 * time.Minute) // 10 minutes > default 5min window
	err := VerifyStorage([]byte(testStorageSecret), h, StorageOpDelete, SHA256Hex(body), VerifyOptions{Now: func() time.Time { return verifyAt }})
	if !errors.Is(err, ErrTimestampOutOfWindow) {
		t.Errorf("expected ErrTimestampOutOfWindow, got %v", err)
	}
}

func TestVerifyStorage_RejectsMissingSignature(t *testing.T) {
	h := makeStorageHeader("p-1", "tenant_x", "u-1", "k")
	err := VerifyStorage([]byte(testStorageSecret), h, StorageOpUpload, SHA256Hex(nil), VerifyOptions{})
	if !errors.Is(err, ErrMissingSignature) {
		t.Errorf("expected ErrMissingSignature, got %v", err)
	}
}

func TestSHA256Hex_Format(t *testing.T) {
	h := SHA256Hex(nil)
	if h != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("SHA256Hex(nil) wrong: %s", h)
	}
	if len(h) != 64 {
		t.Errorf("SHA256Hex output wrong length: %d", len(h))
	}
	if strings.ToLower(h) != h {
		t.Errorf("SHA256Hex must be lowercase, got %s", h)
	}
}
