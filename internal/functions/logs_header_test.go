package functions

import (
	"encoding/base64"
	"testing"
)

// #492: the gateway pulls structured ctx.log.* lines out of the runner's
// X-Function-Logs response header. These tests pin the exact contract
// the runner side (functions-runner/server.ts encodeLogLinesHeader)
// must keep producing: base64(JSON.stringify(lines)), otherwise the
// console will silently render nothing.

func TestDecodeFunctionLogsHeader_ValidRoundTrip(t *testing.T) {
	payload := []byte(`[{"level":"INFO","msg":"hello","ts":"2026-08-25T10:00:00.000Z"}]`)
	header := base64.StdEncoding.EncodeToString(payload)

	got := decodeFunctionLogsHeader(header)
	if string(got) != string(payload) {
		t.Fatalf("round-trip mismatch:\n got=%s\nwant=%s", got, payload)
	}
}

func TestDecodeFunctionLogsHeader_EmptyReturnsNil(t *testing.T) {
	// "" means "runner didn't attach the header" — must map to SQL NULL,
	// not the empty array (which would mean "invoked but all lines
	// truncated"). LogInvocation uses nil-vs-len(0) to distinguish.
	if got := decodeFunctionLogsHeader(""); got != nil {
		t.Fatalf("empty header should return nil, got %q", got)
	}
}

func TestDecodeFunctionLogsHeader_InvalidBase64ReturnsNil(t *testing.T) {
	if got := decodeFunctionLogsHeader("!!!not-base64!!!"); got != nil {
		t.Fatalf("bad base64 should return nil, got %q", got)
	}
}

func TestDecodeFunctionLogsHeader_InvalidJSONReturnsNil(t *testing.T) {
	// Well-formed base64 but garbage inside → also nil (we never want
	// to hand invalid JSON to a JSONB column INSERT — pgx would error
	// and the invocation summary itself would fail to log).
	header := base64.StdEncoding.EncodeToString([]byte("{not valid"))
	if got := decodeFunctionLogsHeader(header); got != nil {
		t.Fatalf("bad JSON should return nil, got %q", got)
	}
}

func TestSafeSample_TruncatesLongInput(t *testing.T) {
	huge := make([]byte, 500)
	for i := range huge {
		huge[i] = 'x'
	}
	got := safeSample(huge)
	// 120 chars + ellipsis
	if len(got) != 120+len("…") {
		t.Fatalf("expected 120-byte sample + ellipsis, got len=%d", len(got))
	}
}

func TestSafeSample_ShortInputUnchanged(t *testing.T) {
	got := safeSample([]byte("small"))
	if got != "small" {
		t.Fatalf("short input mangled: got %q", got)
	}
}
