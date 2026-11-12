package email

// Bounce blocks (000152): addresses whose mail from the platform sender
// failed permanently are not sent transactional mail again for a while.
// Hard bounces hurt the sending domain's reputation, and the provider
// counts a send to an address it already blocked as another rejection.
// Only a SHA-256 of the address is kept (end users of projects included).

import (
	"context"
	"errors"
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
	return mailaddr.Hash(addr)
}

// BounceKind sorts a FailedEmail's reason into a kind. The reason is
// "<status> [<provider flags>]: <last server message>" (see
// parseFailedEmailsPage); flags are matched only inside the brackets, so
// a server's own wording can't pick the kind.
func BounceKind(reason string) string {
	r := strings.ToLower(reason)
	flags := map[string]bool{}
	if i := strings.IndexByte(r, '['); i >= 0 {
		if j := strings.IndexByte(r[i:], ']'); j > 0 {
			for _, f := range strings.Split(r[i+1:i+j], ",") {
				flags[strings.TrimSpace(f)] = true
			}
		}
	}
	switch {
	case flags["mailbox_full"]:
		return BounceMailboxFull
	case flags["mailbox_not_found"]:
		return BounceMailboxNotFound
	case strings.Contains(r, "non-existing domain"):
		return BounceDomainNotFound
	case flags["blocklisted"]:
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

// consoleRetryAfter: console account mail (verification, password reset)
// to a blocked address still goes out once a day, so an account whose
// mailbox was full or not yet set up can recover; a new bounce renews
// the block. Project auth mail stays skipped for the whole block.
const consoleRetryAfter = 24 * time.Hour

// platformSendSkip says whether the platform sender should skip `to`:
// the email log reason and detail, or "" to send. A failed lookup sends
// (the block protects reputation; it must not stop auth mail). The
// detail names the kind of failure but not when it happened: the same
// address may be another project's user.
func (s *EmailService) platformSendSkip(ctx context.Context, to string) (reason, detail string) {
	return s.platformSendSkipAfter(ctx, to, 0)
}

// platformSendSkipAfter: with retryAfter > 0, a block whose last failure
// is older than that doesn't skip.
func (s *EmailService) platformSendSkipAfter(ctx context.Context, to string, retryAfter time.Duration) (reason, detail string) {
	if mailaddr.IsReserved(to) {
		return ReasonReservedAddress, ""
	}
	if s.pool == nil {
		return "", ""
	}
	var kind string
	var lastSeen time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT kind, last_seen_at FROM public.email_bounce_blocks
		  WHERE address_hash = $1 AND blocked_until > now()`,
		AddressHash(to)).Scan(&kind, &lastSeen)
	switch {
	case err == nil:
		if retryAfter > 0 && time.Since(lastSeen) > retryAfter {
			return "", ""
		}
		return ReasonAddressBounced, "Reason: " + bounceKindText(kind) + "."
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
