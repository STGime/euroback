package email

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBounceKind(t *testing.T) {
	cases := map[string]string{
		"dropped [mailbox_full,hard_bounce]":      BounceMailboxFull,
		"dropped [blocklisted,mailbox_not_found]": BounceMailboxNotFound,
		"dropped: 550 non-existing domain":        BounceDomainNotFound,
		"dropped [blocklisted]":                   BounceBlocklisted,
		"dropped [hard_bounce]: 550 user unknown": BounceHard,
		"failed": BounceHard,
	}
	for in, want := range cases {
		if got := BounceKind(in); got != want {
			t.Errorf("BounceKind(%q) = %q, want %q", in, got, want)
		}
	}
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if d := BounceBlockedUntil(BounceMailboxFull, at).Sub(at); d != 7*24*time.Hour {
		t.Errorf("mailbox_full blocked for %v", d)
	}
	if d := BounceBlockedUntil(BounceMailboxNotFound, at).Sub(at); d != 30*24*time.Hour {
		t.Errorf("mailbox_not_found blocked for %v", d)
	}
}

func TestAddressHashNormalises(t *testing.T) {
	if AddressHash(" Ann@Example.EU ") != AddressHash("ann@example.eu") {
		t.Fatal("hash must ignore case and surrounding spaces")
	}
	if len(AddressHash("a@b.eu")) != 64 {
		t.Fatal("hash must be 64 hex chars")
	}
}

// A reserved test address is skipped before any lookup.
func TestPlatformSendSkip_Reserved(t *testing.T) {
	s := &EmailService{}
	for _, a := range []string{"test@example.com", "x@shop.test", "y@localhost"} {
		if r, _ := s.platformSendSkip(context.Background(), a); r != ReasonReservedAddress {
			t.Errorf("%s: reason %q", a, r)
		}
	}
	if r, _ := s.platformSendSkip(context.Background(), "real@eurobase.app"); r != "" {
		t.Errorf("real address skipped: %q", r)
	}
}

func TestClassifySend_Skipped(t *testing.T) {
	e := classifySend(FlowVerification, "a@b.eu", Delivery{SkipReason: ReasonAddressBounced, SkipDetail: "(x)"}, nil)
	if e.Outcome != OutcomeSkipped || e.Reason != ReasonAddressBounced || e.Detail != "(x)" || e.Via != "" {
		t.Fatalf("got %+v", e)
	}
}

// Under the production roles: the worker (gateway role) records blocks,
// the send path (gateway role) reads them, only a hash is stored, and the
// SQL hash the console-verification path uses matches AddressHash.
//
//	EMAIL_LOG_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	EMAIL_LOG_TEST_GATEWAY_URL=postgres://eurobase_gateway:localdev@localhost:5432/eurobase \
//	go test ./internal/email/ -run BounceBlocks
func TestBounceBlocks_ProductionRoles(t *testing.T) {
	adminURL, gwURL := os.Getenv("EMAIL_LOG_TEST_ADMIN_URL"), os.Getenv("EMAIL_LOG_TEST_GATEWAY_URL")
	if adminURL == "" || gwURL == "" {
		t.Skip("EMAIL_LOG_TEST_{ADMIN,GATEWAY}_URL not set")
	}
	ctx := context.Background()
	open := func(u string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	admin, gw := open(adminURL), open(gwURL)

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	addr := "Bounce-" + hex.EncodeToString(b) + "@Bounce-Check.eu"
	full := "full-" + hex.EncodeToString(b) + "@bounce-check.eu"
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DELETE FROM public.email_bounce_blocks WHERE address_hash IN ($1, $2)`, AddressHash(addr), AddressHash(full))
	})

	now := time.Now()
	if err := RecordBounceBlock(ctx, gw, FailedEmail{Recipient: strings.ToLower(addr), Reason: "dropped [mailbox_not_found]", At: now}); err != nil {
		t.Fatal(err)
	}
	// An older failure doesn't move the block back or change the kind.
	if err := RecordBounceBlock(ctx, gw, FailedEmail{Recipient: addr, Reason: "dropped [mailbox_full]", At: now.Add(-60 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var kind, stored string
	var until time.Time
	if err := admin.QueryRow(ctx, `SELECT kind, blocked_until, address_hash FROM public.email_bounce_blocks WHERE address_hash = $1`, AddressHash(addr)).Scan(&kind, &until, &stored); err != nil {
		t.Fatal(err)
	}
	if kind != BounceMailboxNotFound || until.Before(now.Add(29*24*time.Hour)) {
		t.Fatalf("kind %q until %v", kind, until)
	}
	if strings.Contains(stored, "@") {
		t.Fatal("address stored in plain text")
	}

	s := &EmailService{pool: gw}
	if r, d := s.platformSendSkip(ctx, addr); r != ReasonAddressBounced || !strings.Contains(d, "doesn't exist") {
		t.Fatalf("bounced address: reason %q detail %q", r, d)
	}
	if r, _ := s.platformSendSkip(ctx, "other-"+hex.EncodeToString(b)+"@bounce-check.eu"); r != "" {
		t.Fatalf("unknown address skipped: %q", r)
	}

	// An expired block sends again and is cleaned up.
	if err := RecordBounceBlock(ctx, gw, FailedEmail{Recipient: full, Reason: "[mailbox_full]", At: now.Add(-8 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.platformSendSkip(ctx, full); r != "" {
		t.Fatalf("expired block still skipped: %q", r)
	}
	if err := RemoveExpiredBounceBlocks(ctx, gw); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = admin.QueryRow(ctx, `SELECT count(*) FROM public.email_bounce_blocks WHERE address_hash = $1`, AddressHash(full)).Scan(&n)
	if n != 0 {
		t.Fatal("expired block not removed")
	}

	// The console verification path clears by a hash computed in SQL.
	var sqlHash string
	if err := gw.QueryRow(ctx, `SELECT encode(sha256(convert_to(lower(btrim($1)), 'UTF8')), 'hex')`, addr).Scan(&sqlHash); err != nil {
		t.Fatal(err)
	}
	if sqlHash != AddressHash(addr) {
		t.Fatalf("SQL hash %s != Go hash %s", sqlHash, AddressHash(addr))
	}
}
