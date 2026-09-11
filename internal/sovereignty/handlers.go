package sovereignty

// HTTP handlers for the public CLOUD Act Exposure Checker.
//
// Two endpoints:
//   POST /platform/public/sovereignty/report — anonymous, IP-rate-limited.
//     Body: {vendor_slugs: string[], severity_modifier?: string}
//     Response 201: {hash, score, cards, alternatives}
//     Runs on the GATEWAY pool (INSERT-only grant per migration 000116).
//
//   GET  /platform/public/sovereignty/report/{hash}
//     Response 200: {hash, score, cards, alternatives, created_at}
//     Runs on the DEVELOPER pool (SELECT grant is developer-only).
//
// Report hash is 10-char Crockford-base32; ~50 bits of entropy is
// plenty for the shareable /r/{hash} permalink and it stays URL-safe
// without an escape step.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rate limits — anonymous public surface. Tight enough to block
// scripted abuse; loose enough that a curious visitor exploring
// the checker doesn't hit a wall.
const (
	rateReportCreatePerIPCount  = 20
	rateReportCreatePerIPWindow = 1 * time.Hour
	rateReportLookupPerIPCount  = 120
	rateReportLookupPerIPWindow = 1 * time.Hour

	// Body cap for POST /report. The picker submits vendor slugs
	// (short strings) — 16 KiB is generous.
	maxReportBodyBytes = 16 * 1024

	// Max vendor slugs per report. Beyond this we assume abuse or
	// a UI bug; nothing legitimate needs more.
	maxVendorSlugsPerReport = 100

	// Report hash bytes-of-entropy → 10 Crockford chars.
	reportHashLen = 10
)

// CreateReportBody is the JSON payload of POST /report.
type CreateReportBody struct {
	VendorSlugs      []string           `json:"vendor_slugs"`
	SeverityModifier string             `json:"severity_modifier,omitempty"`
	CampaignMetadata *CampaignMetadata  `json:"campaign_metadata,omitempty"`
}

// CampaignMetadata is optional attribution the frontend can pass —
// referrer, UTM tags, client language. Every field optional.
type CampaignMetadata struct {
	Referrer     string `json:"referrer,omitempty"`
	UTMSource    string `json:"utm_source,omitempty"`
	UTMMedium    string `json:"utm_medium,omitempty"`
	UTMCampaign  string `json:"utm_campaign,omitempty"`
	UTMTerm      string `json:"utm_term,omitempty"`
	UTMContent   string `json:"utm_content,omitempty"`
	ClientLang   string `json:"client_lang,omitempty"`
}

// ReportResponse is the JSON body returned by both POST /report
// (immediately) and GET /report/{hash} (persisted lookup).
type ReportResponse struct {
	Hash             string    `json:"hash"`
	Overall          string    `json:"overall"`
	ExposurePercent  int       `json:"exposure_percent"`
	RedCount         int       `json:"red_count"`
	AmberCount       int       `json:"amber_count"`
	GreenCount       int       `json:"green_count"`
	SeverityModifier string    `json:"severity_modifier,omitempty"`
	Cards            []VendorCard `json:"cards"`
	Alternatives     []Swap    `json:"alternatives"`
	UnknownSlugs     []string  `json:"unknown_slugs,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// Handler wraps the state the two endpoints share. Constructed
// once at gateway startup.
type Handler struct {
	// GatewayPool is used by the create endpoint (INSERT-only per
	// migration 000116).
	GatewayPool *pgxpool.Pool
	// DeveloperPool is used by the lookup endpoint (SELECT grant is
	// developer-only). Nil means lookups return 503 — safe fallback.
	DeveloperPool *pgxpool.Pool
	// Registry is the loaded vendor DB (see LoadEmbedded).
	Registry *Registry
	// Limiter is the shared rate-limit backend (may be nil in dev;
	// missing limiter falls open, matches the codebase convention).
	Limiter *ratelimit.RateLimiter
}

// HandleCreateReport — POST /platform/public/sovereignty/report
// Anonymous, IP-rate-limited, gateway pool.
func (h *Handler) HandleCreateReport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Cap the body before we touch the JSON parser.
		r.Body = http.MaxBytesReader(w, r.Body, maxReportBodyBytes)

		var body CreateReportBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		if len(body.VendorSlugs) == 0 {
			writeJSONErr(w, http.StatusBadRequest, "vendor_slugs is required and non-empty")
			return
		}
		if len(body.VendorSlugs) > maxVendorSlugsPerReport {
			writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("vendor_slugs must be %d or fewer", maxVendorSlugsPerReport))
			return
		}
		sev := strings.TrimSpace(body.SeverityModifier)
		if sev != "" && !isValidSeverity(sev) {
			writeJSONErr(w, http.StatusBadRequest, "severity_modifier must be one of health/legal/financial/children")
			return
		}

		clientIP := ratelimit.ClientIP(r)
		if h.Limiter != nil {
			if blocked := ratelimit.CheckAuthRate(
				h.Limiter, w, r.Context(),
				"sovereignty_report_create", clientIP,
				rateReportCreatePerIPCount, rateReportCreatePerIPWindow,
			); blocked {
				return
			}
		} else {
			slog.Warn("sovereignty: rate limiter unavailable, request allowed unrated",
				"path", r.URL.Path)
		}

		// Run the scoring function.
		report := Score(h.Registry, body.VendorSlugs, sev)

		// Marshal the persisted shape once so we can INSERT the same
		// JSON we return (frontend + DB stay identical).
		slugsJSON, err := json.Marshal(body.VendorSlugs)
		if err != nil {
			slog.Error("sovereignty: marshal vendor_slugs", "error", err)
			writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}
		scoreJSON, err := json.Marshal(persistedScore{
			Overall:         report.Overall,
			ExposurePercent: report.ExposurePercent,
			RedCount:        report.RedCount,
			AmberCount:      report.AmberCount,
			GreenCount:      report.GreenCount,
			Cards:           report.Cards,
			Alternatives:    report.Alternatives,
			UnknownSlugs:    report.UnknownSlugs,
		})
		if err != nil {
			slog.Error("sovereignty: marshal score", "error", err)
			writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}
		var metaJSON []byte
		if body.CampaignMetadata != nil {
			metaJSON, _ = json.Marshal(body.CampaignMetadata)
		}

		userAgent := truncateAtByte(r.Header.Get("User-Agent"), 500)

		// Insert with hash-collision retry. 10 Crockford chars =
		// ~50 bits, collision probability on a small table is
		// negligible, but we still handle it as a defensive loop
		// rather than pretending it can't happen.
		insertCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var savedHash string
		var savedCreatedAt time.Time
		for attempt := 0; attempt < 4; attempt++ {
			hash, err := newReportHash()
			if err != nil {
				slog.Error("sovereignty: generate hash", "error", err)
				writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
				return
			}
			err = h.GatewayPool.QueryRow(insertCtx, `
				INSERT INTO public.sovereignty_reports
				    (hash, vendor_slugs, severity_modifier, computed_score,
				     campaign_metadata, ip_address, user_agent)
				VALUES ($1, $2::jsonb, NULLIF($3, ''), $4::jsonb,
				        NULLIF($5, '')::jsonb, $6::inet, NULLIF($7, ''))
				RETURNING hash, created_at
			`, hash, string(slugsJSON), sev, string(scoreJSON),
				string(metaJSON), clientIP, userAgent).Scan(&savedHash, &savedCreatedAt)
			if err == nil {
				break
			}
			if isUniqueViolation(err) {
				// Rare — retry with a fresh hash. If we've tried
				// four times, something is very wrong.
				continue
			}
			slog.Error("sovereignty: insert report", "error", err)
			writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}
		if savedHash == "" {
			slog.Error("sovereignty: hash collision retry exhausted")
			writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}

		resp := ReportResponse{
			Hash:             savedHash,
			Overall:          report.Overall,
			ExposurePercent:  report.ExposurePercent,
			RedCount:         report.RedCount,
			AmberCount:       report.AmberCount,
			GreenCount:       report.GreenCount,
			SeverityModifier: report.SeverityModifier,
			Cards:            report.Cards,
			Alternatives:     report.Alternatives,
			UnknownSlugs:     report.UnknownSlugs,
			CreatedAt:        savedCreatedAt,
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// HandleGetReport — GET /platform/public/sovereignty/report/{hash}
// Anonymous, IP-rate-limited, developer pool.
func (h *Handler) HandleGetReport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if h.DeveloperPool == nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "report lookup temporarily unavailable")
			return
		}

		hash := strings.TrimSpace(chi.URLParam(r, "hash"))
		if !isValidHash(hash) {
			writeJSONErr(w, http.StatusBadRequest, "invalid report hash")
			return
		}

		clientIP := ratelimit.ClientIP(r)
		if h.Limiter != nil {
			if blocked := ratelimit.CheckAuthRate(
				h.Limiter, w, r.Context(),
				"sovereignty_report_lookup", clientIP,
				rateReportLookupPerIPCount, rateReportLookupPerIPWindow,
			); blocked {
				return
			}
		}

		var scoreJSON []byte
		var createdAt time.Time
		var severityModifier *string
		err := h.DeveloperPool.QueryRow(r.Context(), `
			SELECT computed_score, severity_modifier, created_at
			  FROM public.sovereignty_reports
			 WHERE hash = $1
		`, hash).Scan(&scoreJSON, &severityModifier, &createdAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONErr(w, http.StatusNotFound, "report not found")
			return
		}
		if err != nil {
			slog.Error("sovereignty: lookup report", "error", err, "hash", hash)
			writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}
		var stored persistedScore
		if err := json.Unmarshal(scoreJSON, &stored); err != nil {
			slog.Error("sovereignty: decode stored score", "error", err, "hash", hash)
			writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
			return
		}
		resp := ReportResponse{
			Hash:            hash,
			Overall:         stored.Overall,
			ExposurePercent: stored.ExposurePercent,
			RedCount:        stored.RedCount,
			AmberCount:      stored.AmberCount,
			GreenCount:      stored.GreenCount,
			Cards:           stored.Cards,
			Alternatives:    stored.Alternatives,
			UnknownSlugs:    stored.UnknownSlugs,
			CreatedAt:       createdAt,
		}
		if severityModifier != nil {
			resp.SeverityModifier = *severityModifier
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// ── helpers ────────────────────────────────────────────────────

// persistedScore matches the shape we persist in
// sovereignty_reports.computed_score. Kept internal to this file
// so a change to the wire response doesn't accidentally break
// stored-report re-renders.
type persistedScore struct {
	Overall         string       `json:"overall"`
	ExposurePercent int          `json:"exposure_percent"`
	RedCount        int          `json:"red_count"`
	AmberCount      int          `json:"amber_count"`
	GreenCount      int          `json:"green_count"`
	Cards           []VendorCard `json:"cards"`
	Alternatives    []Swap       `json:"alternatives"`
	UnknownSlugs    []string     `json:"unknown_slugs,omitempty"`
}

// Crockford base32 alphabet — url-safe without percent-encoding,
// no ambiguous chars (no I/L/O/U).
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newReportHash() (string, error) {
	// One byte per char = plenty of range for our 32-symbol
	// alphabet; the modulo bias is fine at this alphabet size and
	// hash length.
	buf := make([]byte, reportHashLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, reportHashLen)
	for i, b := range buf {
		out[i] = crockfordAlphabet[int(b)&31]
	}
	return string(out), nil
}

func isValidHash(s string) bool {
	if len(s) != reportHashLen {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune(crockfordAlphabet, c) {
			return false
		}
	}
	return true
}

func isValidSeverity(s string) bool {
	switch s {
	case SeverityHealth, SeverityLegal, SeverityFinancial, SeverityChildren:
		return true
	}
	return false
}

func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// isUniqueViolation matches pgx's SQLSTATE 23505 ("unique_violation").
// Used to spot hash-collision races on the reports insert.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// Prefer the typed pgconn.PgError if available; but stringy
	// matches work too and avoid a new import.
	return strings.Contains(err.Error(), "SQLSTATE 23505") ||
		strings.Contains(err.Error(), "duplicate key value violates unique constraint")
}

// truncateAtByte trims s to at most n bytes. Coarser than the
// rune-boundary truncation used elsewhere (contact form) but
// user_agent is ASCII in the common case and we don't render it
// anywhere the mojibake would matter.
func truncateAtByte(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
