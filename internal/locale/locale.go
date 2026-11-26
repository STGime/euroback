// Package locale picks the language of the auth emails a project sends to
// its app users (verification, password reset, magic link, …).
//
// The language is chosen per email, in this order:
//
//  1. the user's stored language (users.locale). Once set it always
//     decides: a request's language never overrides it;
//  2. the language the app sent with the request (a "locale" field);
//  3. the project's default (auth_config.email_locale), when the project
//     chose one: an explicit default beats the browser, so an app in one
//     language keeps its emails in that language;
//  4. the request's Accept-Language header (projects on "automatic");
//  5. English.
//
// Every step goes through Normalize, so an unsupported or malformed value
// falls through to the next step instead of failing the email.
package locale

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Default is the language used when nothing else applies.
const Default = "en"

// Supported lists the languages with built-in email templates.
var Supported = []string{"en", "de", "fr", "nl", "sv", "es", "nb"}

var supported = func() map[string]bool {
	m := make(map[string]bool, len(Supported))
	for _, l := range Supported {
		m[l] = true
	}
	return m
}()

// aliases maps language subtags to the supported language that serves them.
// Norwegian: "no" (macrolanguage) and "nn" (Nynorsk) get Bokmål.
var aliases = map[string]string{
	"no": "nb",
	"nn": "nb",
}

// IsSupported reports whether l is one of the Supported languages exactly
// (already normalised).
func IsSupported(l string) bool { return supported[l] }

// Normalize maps a language tag such as "de-AT", "de_CH", "NB" or "no" to a
// supported language ("de", "nb"), or "" if the language isn't supported or
// the tag is malformed. Only the primary language subtag counts.
func Normalize(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" || len(tag) > 35 { // BCP 47 tags are short; ignore junk
		return ""
	}
	primary, _, _ := strings.Cut(strings.ReplaceAll(tag, "_", "-"), "-")
	primary = strings.ToLower(primary)
	if len(primary) < 2 || len(primary) > 3 {
		return ""
	}
	for _, r := range primary {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	if a, ok := aliases[primary]; ok {
		primary = a
	}
	if supported[primary] {
		return primary
	}
	return ""
}

// FromAcceptLanguage returns the supported language the header prefers
// most (by q value, then by order), or "". A malformed entry is skipped.
func FromAcceptLanguage(header string) string {
	if len(header) > 1024 {
		header = header[:1024]
	}
	type cand struct {
		lang  string
		q     float64
		order int
	}
	var cands []cand
	for i, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if params != "" {
			for _, p := range strings.Split(params, ";") {
				k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
				if ok && strings.TrimSpace(k) == "q" {
					f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
					if err != nil || f < 0 || f > 1 {
						f = 0
					}
					q = f
				}
			}
		}
		if q <= 0 {
			continue
		}
		if l := Normalize(tag); l != "" {
			cands = append(cands, cand{l, q, i})
		}
	}
	if len(cands) == 0 {
		return ""
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].q != cands[b].q {
			return cands[a].q > cands[b].q
		}
		return cands[a].order < cands[b].order
	})
	return cands[0].lang
}

// Resolve picks the email language (see the package comment for the
// order). projectDefault "" means "automatic": the browser's language
// applies.
func Resolve(stored, requested, acceptLanguage, projectDefault string) string {
	if l := Normalize(stored); l != "" {
		return l
	}
	if l := Normalize(requested); l != "" {
		return l
	}
	if l := Normalize(projectDefault); l != "" {
		return l
	}
	if l := FromAcceptLanguage(acceptLanguage); l != "" {
		return l
	}
	return Default
}

// ForNewUser is the language to store for a user created by this request:
// the requested language; else, only for a project on "automatic" (no
// explicit default), the Accept-Language header; else "" (store nothing,
// the project default then applies at send time). With an explicit
// default the browser's language isn't stored, or it would later win over
// the project's choice.
func ForNewUser(ctx context.Context, projectDefault string) string {
	h := HintsFrom(ctx)
	if l := Normalize(h.Requested); l != "" {
		return l
	}
	if Normalize(projectDefault) != "" {
		return ""
	}
	return FromAcceptLanguage(h.AcceptLanguage)
}

// Hints are the language signals of the current request.
type Hints struct {
	Requested      string // the app's "locale" field
	AcceptLanguage string // the Accept-Language header
}

type hintsKey struct{}

// WithAcceptLanguage records the request's Accept-Language header.
func WithAcceptLanguage(ctx context.Context, header string) context.Context {
	h := HintsFrom(ctx)
	h.AcceptLanguage = header
	return context.WithValue(ctx, hintsKey{}, h)
}

// WithRequested records the language the app sent with the request. An
// empty value leaves the context unchanged.
func WithRequested(ctx context.Context, tag string) context.Context {
	if strings.TrimSpace(tag) == "" {
		return ctx
	}
	h := HintsFrom(ctx)
	h.Requested = tag
	return context.WithValue(ctx, hintsKey{}, h)
}

// HintsFrom returns the request's language hints (zero value if none).
func HintsFrom(ctx context.Context) Hints {
	h, _ := ctx.Value(hintsKey{}).(Hints)
	return h
}

// Middleware records each request's Accept-Language header for the auth
// emails the request may send.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Accept-Language"); h != "" {
			r = r.WithContext(WithAcceptLanguage(r.Context(), h))
		}
		next.ServeHTTP(w, r)
	})
}
