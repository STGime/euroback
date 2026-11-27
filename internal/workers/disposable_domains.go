package workers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/publicsuffix"
)

// Weekly sync of the signup blocklist (public.platform_blocked_signup_domains)
// with a public list of disposable email domains, one domain per line
// (DISPOSABLE_DOMAINS_URL, e.g. the disposable-email-domains project's
// disposable_email_blocklist.conf). Empty URL = disabled.
//
// The sync only adds and removes rows with source 'disposable_list': rows an
// admin added ('manual') are never touched, and domains an admin unblocked
// (public.platform_blocked_domain_exclusions) are never re-added.
//
// The list comes from outside, so a bad or tampered list must not block real
// users. A run is refused (nothing changes, the error is recorded) when:
//   - the download fails, is larger than disposableMaxBytes, or has fewer
//     than disposableMinDomains / more than disposableMaxDomains usable
//     entries;
//   - an entry is, or is a parent of, a protected mail provider
//     (protectedMailDomains), e.g. "gmail.com" or "com";
//   - it would add more than disposableMaxAdded or remove more than
//     disposableMaxRemovedShare of the list's current rows (a truncated or
//     rewritten list), except when the list has no rows yet.
//
// Entries that aren't plain domain names, or are public suffixes ("co.uk",
// which would block a whole registry through the parent-domain match at
// signup), are skipped one by one.
const (
	disposableSyncEvery       = 7 * 24 * time.Hour
	disposableCheckEvery      = 6 * time.Hour
	disposableHTTPTimeout     = 60 * time.Second
	disposableMaxBytes        = 8 << 20
	disposableMinDomains      = 5000
	disposableMaxDomains      = 200000
	disposableMaxAdded        = 5000
	disposableMaxRemovedShare = 0.10
	disposableListReason      = "disposable email (disposable-email-domains list)"
	disposableLockKey         = "eurobase/disposable-domain-sync"
)

// protectedMailDomains are providers real users sign up with. A list that
// contains one of them, or a parent of one, is refused as a whole.
var protectedMailDomains = []string{
	"gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com",
	"msn.com", "yahoo.com", "ymail.com", "icloud.com", "me.com", "mac.com",
	"aol.com", "proton.me", "protonmail.com", "pm.me", "tutanota.com",
	"tuta.io", "gmx.de", "gmx.net", "gmx.at", "gmx.ch", "web.de",
	"t-online.de", "freenet.de", "posteo.de", "mailbox.org", "orange.fr",
	"free.fr", "laposte.net", "sfr.fr", "libero.it", "virgilio.it",
	"seznam.cz", "wp.pl", "o2.pl", "onet.pl", "interia.pl", "yandex.com",
	"zoho.com", "fastmail.com", "hey.com", "eurobase.app",
}

var plainDomainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// StartDisposableDomainSync checks every disposableCheckEvery whether the
// last successful sync is older than disposableSyncEvery, and runs it if so.
func StartDisposableDomainSync(ctx context.Context, pool *pgxpool.Pool) {
	url := strings.TrimSpace(os.Getenv("DISPOSABLE_DOMAINS_URL"))
	if url == "" {
		slog.Info("disposable domain sync disabled: DISPOSABLE_DOMAINS_URL not set")
		return
	}
	client := &http.Client{Timeout: disposableHTTPTimeout}
	go func() {
		ticker := time.NewTicker(disposableCheckEvery)
		defer ticker.Stop()
		for {
			if due, err := disposableSyncDue(ctx, pool); err != nil {
				slog.Warn("disposable domain sync: reading status", "error", err)
			} else if due {
				runDisposableDomainSync(ctx, pool, client, url)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func disposableSyncDue(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var lastOK *time.Time
	err := pool.QueryRow(ctx,
		`SELECT last_ok_at FROM public.platform_blocked_domain_sync WHERE id = 1`).Scan(&lastOK)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return lastOK == nil || time.Since(*lastOK) >= disposableSyncEvery, nil
}

// DisposableSyncResult is one run's outcome.
type DisposableSyncResult struct {
	ListSize, Added, Removed int
}

func runDisposableDomainSync(ctx context.Context, pool *pgxpool.Pool, client *http.Client, url string) {
	res, err := syncDisposableDomains(ctx, pool, client, url)
	status := ""
	if err != nil {
		status = err.Error()
		if len(status) > 500 {
			status = status[:500]
		}
		slog.Error("disposable domain sync failed; blocklist unchanged", "error", err)
	} else {
		slog.Info("disposable domain sync done", "list_size", res.ListSize, "added", res.Added, "removed", res.Removed)
	}
	if _, uerr := pool.Exec(ctx, `
		INSERT INTO public.platform_blocked_domain_sync (id, last_run_at, last_ok_at, list_size, added, removed, error)
		VALUES (1, now(), CASE WHEN $1 = '' THEN now() END, $2, $3, $4, NULLIF($1, ''))
		ON CONFLICT (id) DO UPDATE SET
		    last_run_at = now(),
		    last_ok_at  = CASE WHEN $1 = '' THEN now() ELSE public.platform_blocked_domain_sync.last_ok_at END,
		    list_size   = CASE WHEN $1 = '' THEN $2 ELSE public.platform_blocked_domain_sync.list_size END,
		    added       = CASE WHEN $1 = '' THEN $3 ELSE public.platform_blocked_domain_sync.added END,
		    removed     = CASE WHEN $1 = '' THEN $4 ELSE public.platform_blocked_domain_sync.removed END,
		    error       = NULLIF($1, '')`,
		status, res.ListSize, res.Added, res.Removed); uerr != nil {
		slog.Warn("disposable domain sync: recording status", "error", uerr)
	}
}

// syncDisposableDomains downloads and checks the list, then applies it in
// one transaction. On any error nothing is changed.
func syncDisposableDomains(ctx context.Context, pool *pgxpool.Pool, client *http.Client, url string) (DisposableSyncResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DisposableSyncResult{}, fmt.Errorf("request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return DisposableSyncResult{}, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DisposableSyncResult{}, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	domains, err := parseDisposableList(io.LimitReader(resp.Body, disposableMaxBytes+1))
	if err != nil {
		return DisposableSyncResult{}, err
	}
	return applyDisposableDomains(ctx, pool, domains)
}

// parseDisposableList reads one domain per line (# comments and blank lines
// ignored), lowercases, skips entries that aren't plain domains or are
// public suffixes, and refuses the list when it is too large, too small or
// touches a protected provider.
func parseDisposableList(r io.Reader) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	read := 0
	for sc.Scan() {
		line := sc.Text()
		read += len(line) + 1
		if read > disposableMaxBytes {
			return nil, fmt.Errorf("list larger than %d bytes", disposableMaxBytes)
		}
		d := strings.ToLower(strings.TrimSpace(line))
		if d == "" || strings.HasPrefix(d, "#") {
			continue
		}
		d = strings.TrimSuffix(d, ".")
		if len(d) > 253 || !plainDomainRE.MatchString(d) {
			continue
		}
		if p := protectedParentOf(d); p != "" {
			return nil, fmt.Errorf("list contains %q, which would block %s; refusing the whole list", d, p)
		}
		if _, err := publicsuffix.EffectiveTLDPlusOne(d); err != nil {
			continue // a public suffix itself ("co.uk")
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading list: %w", err)
	}
	if len(out) < disposableMinDomains {
		return nil, fmt.Errorf("list has %d usable domains, fewer than %d", len(out), disposableMinDomains)
	}
	if len(out) > disposableMaxDomains {
		return nil, fmt.Errorf("list has %d usable domains, more than %d", len(out), disposableMaxDomains)
	}
	return out, nil
}

// protectedParentOf returns the protected provider that d equals or is a
// parent domain of, or "" when there is none.
func protectedParentOf(d string) string {
	for _, p := range protectedMailDomains {
		if p == d || strings.HasSuffix(p, "."+d) {
			return p
		}
	}
	return ""
}

func applyDisposableDomains(ctx context.Context, pool *pgxpool.Pool, domains []string) (DisposableSyncResult, error) {
	res := DisposableSyncResult{ListSize: len(domains)}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext($1))`, disposableLockKey).Scan(&locked); err != nil {
		return res, fmt.Errorf("lock: %w", err)
	}
	if !locked {
		return res, errors.New("another sync is running")
	}

	var current, toAdd, toRemove int
	// The list travels as one text[] parameter (no temp table).
	if err := tx.QueryRow(ctx, `
		WITH disposable_upstream AS (SELECT unnest($1::text[]) AS domain)
		SELECT
		  (SELECT count(*) FROM public.platform_blocked_signup_domains WHERE source = 'disposable_list'),
		  (SELECT count(*) FROM disposable_upstream u
		    WHERE NOT EXISTS (SELECT 1 FROM public.platform_blocked_signup_domains b WHERE b.domain = u.domain)
		      AND NOT EXISTS (SELECT 1 FROM public.platform_blocked_domain_exclusions e WHERE e.domain = u.domain)),
		  (SELECT count(*) FROM public.platform_blocked_signup_domains b
		    WHERE b.source = 'disposable_list'
		      AND NOT EXISTS (SELECT 1 FROM disposable_upstream u WHERE u.domain = b.domain))`,
		domains,
	).Scan(&current, &toAdd, &toRemove); err != nil {
		return res, fmt.Errorf("diff: %w", err)
	}
	if current > 0 {
		if toAdd > disposableMaxAdded {
			return res, fmt.Errorf("would add %d domains (limit %d); refusing", toAdd, disposableMaxAdded)
		}
		if float64(toRemove) > disposableMaxRemovedShare*float64(current) {
			return res, fmt.Errorf("would remove %d of %d listed domains (limit %.0f%%); refusing",
				toRemove, current, disposableMaxRemovedShare*100)
		}
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO public.platform_blocked_signup_domains (domain, reason, source)
		SELECT u.domain, $1, 'disposable_list' FROM unnest($2::text[]) AS u(domain)
		 WHERE NOT EXISTS (SELECT 1 FROM public.platform_blocked_domain_exclusions e WHERE e.domain = u.domain)
		ON CONFLICT (domain) DO NOTHING`, disposableListReason, domains)
	if err != nil {
		return res, fmt.Errorf("add: %w", err)
	}
	res.Added = int(tag.RowsAffected())

	tag, err = tx.Exec(ctx, `
		WITH disposable_upstream AS (SELECT unnest($1::text[]) AS domain)
		DELETE FROM public.platform_blocked_signup_domains b
		 WHERE b.source = 'disposable_list'
		   AND NOT EXISTS (SELECT 1 FROM disposable_upstream u WHERE u.domain = b.domain)`, domains)
	if err != nil {
		return res, fmt.Errorf("remove: %w", err)
	}
	res.Removed = int(tag.RowsAffected())

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}
