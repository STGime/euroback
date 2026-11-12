package email

// Bounce blocks (000152): addresses whose mail from the platform sender
// failed permanently are not sent transactional mail again for a while.
// Hard bounces hurt the sending domain's reputation, and the provider
// counts a send to an address it already blocked as another rejection.
// Only a SHA-256 of the address is kept (end users of projects included).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/mailaddr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kinds of permanent failure (email_bounce_blocks.kind).
const (
	BounceMailboxNotFound = "mailbox_not_found"
	BounceMailboxFull     = "mailbox_full"
	BounceDomainNotFound  = "domain_not_found"
	BounceBlocklisted     = "blocklisted"
	BounceHard            = "hard_bounce"
)

// How long an address is skipped after its last failure. A full mailbox
// is often emptied, so it's retried sooner.
const (
	bounceBlockDefault     = 30 * 24 * time.Hour
	bounceBlockMailboxFull = 7 * 24 * time.Hour
)

// AddressHash is the key of an address in email_bounce_blocks: SHA-256 of
// the trimmed, lower-cased address, hex.
func AddressHash(addr string) string {
	return hashSHA256(strings.ToLower(strings.TrimSpace(addr)))
}

// BounceKind sorts a FailedEmail's reason into a kind. The reason holds
// the provider's flags in brackets (see parseFailedEmailsPage).
func BounceKind(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "mailbox_full"):
		return BounceMailboxFull
	case strings.Contains(r, "mailbox_not_found"):
		return BounceMailboxNotFound
	case strings.Contains(r, "non-existing domain"):
		return BounceDomainNotFound
	case strings.Contains(r, "blocklisted"):
		return BounceBlocklisted
	default:
		return BounceHard
	}
}

// BounceBlockedUntil is when an address that failed at `at` may be sent
// to again.
func BounceBlockedUntil(kind string, at time.Time) time.Time {
	if kind == BounceMailboxFull {
		return at.Add(bounceBlockMailboxFull)
	}
	return at.Add(bounceBlockDefault)
}

// RecordBounceBlock upserts a failed recipient. A later failure moves the
// block forward, never back; the kind follows the latest failure.
func RecordBounceBlock(ctx context.Context, pool *pgxpool.Pool, f FailedEmail) error {
	at := f.At
	if at.IsZero() {
		at = time.Now()
	}
	kind := BounceKind(f.Reason)
	_, err := pool.Exec(ctx, `
		INSERT INTO public.email_bounce_blocks (address_hash, kind, first_seen_at, last_seen_at, blocked_until)
		VALUES ($1, $2, $3, $3, $4)
		ON CONFLICT (address_hash) DO UPDATE
		   SET kind = CASE WHEN EXCLUDED.last_seen_at >= public.email_bounce_blocks.last_seen_at
		                   THEN EXCLUDED.kind ELSE public.email_bounce_blocks.kind END,
		       last_seen_at  = GREATEST(public.email_bounce_blocks.last_seen_at, EXCLUDED.last_seen_at),
		       blocked_until = GREATEST(public.email_bounce_blocks.blocked_until, EXCLUDED.blocked_until)`,
		AddressHash(f.Recipient), kind, at, BounceBlockedUntil(kind, at))
	return err
}

// RemoveExpiredBounceBlocks drops blocks that have run out.
func RemoveExpiredBounceBlocks(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DELETE FROM public.email_bounce_blocks WHERE blocked_until < now()`)
	return err
}

// Why a transactional email to an address was not sent through the
// platform sender (email log reasons).
const (
	// A reserved test domain (example.com, .test, …): mail can't arrive.
	ReasonReservedAddress = "reserved_address"
	// Mail to this address failed permanently a short while ago.
	ReasonAddressBounced = "address_bounced"
)

// platformSendSkip says whether the platform sender should skip `to`:
// the email log reason and detail, or "" to send. A failed lookup sends
// (the block protects reputation; it must not stop auth mail).
func (s *EmailService) platformSendSkip(ctx context.Context, to string) (reason, detail string) {
	if mailaddr.IsReserved(to) {
		return ReasonReservedAddress, ""
	}
	if s.pool == nil {
		return "", ""
	}
	var kind string
	var until time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT kind, blocked_until FROM public.email_bounce_blocks
		  WHERE address_hash = $1 AND blocked_until > now()`,
		AddressHash(to)).Scan(&kind, &until)
	switch {
	case err == nil:
		return ReasonAddressBounced, fmt.Sprintf("(%s) Sending resumes after %s.",
			bounceKindText(kind), until.UTC().Format("2 Jan 2006 15:04 UTC"))
	case errors.Is(err, pgx.ErrNoRows):
		return "", ""
	default:
		slog.Warn("bounce block lookup failed, sending", "error", err)
		return "", ""
	}
}

func bounceKindText(kind string) string {
	switch kind {
	case BounceMailboxNotFound:
		return "the mailbox doesn't exist"
	case BounceMailboxFull:
		return "the mailbox was full"
	case BounceDomainNotFound:
		return "the domain doesn't exist"
	case BounceBlocklisted:
		return "the address is blocked by the email provider"
	default:
		return "the receiving server rejected it"
	}
}
