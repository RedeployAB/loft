// Package web holds the shared HTTP transport concerns: the central authentication middleware,
// access to the resolved user, per-request tenant (site) derivation, and small response helpers.
// Domain packages (db, uploads, ai, realtime) depend on this, not on each other.
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/RedeployAB/loft/internal/identity"
)

type ctxKey struct{}
type scopeKey struct{}
type siteKey struct{}

// Apex is the tenant of the platform's own host (the console). The proxy names it explicitly in
// X-Loft-Site. It is never a fallback: an unset header is a misconfigured proxy, not the apex.
const Apex = "_apex"

var nonLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// Auth is the single authentication gate for every /api/* route: it resolves the caller's identity
// and rejects the request with 401 if none can be established, otherwise stores the user in the
// request context for the wrapped handler. Mount it only on API routes so unmatched paths still 404.
//
// Authorization model, FLAT TRUST (deliberate): Auth proves a caller is an authenticated, whitelisted
// user. It is intentionally the ONLY trust boundary: every authenticated user is authorized for
// EVERY site. There is no per-site membership check, by design: this is an internal tool where all
// users are trusted to build on and collaborate across all sites. Cross-site isolation is therefore
// DATA SCOPING, not authorization: Auth pins the tenant from the trusted X-Loft-Site header so a caller
// cannot reach a tenant other than the one whose host they are on, RLS enforces that boundary in
// Postgres, and uploads key on the same prefix. The risks this model accepts (one user can mutate or
// delete another site's shared documents and uploads, and consume its AI budget) are bounded NOT by
// authz but by abuse/cost guards: per-(site,user) write/upload/AI rate limits and per-site + per-(site,
// user) daily AI token budgets. Owner-only collections add intra-site, per-document protection on top.
// If per-site authorization is ever required, add a membership table and a 403 gate here. Do not weaken
// the data-scoping that this model leans on.
//
// The tenant is resolved here too, and a request the proxy did not pin to one is refused with 500
// rather than served under some default. A proxy that fails to name the tenant (a host its rules do
// not match, a rule written for another environment) must break loudly, never fold every site into
// one shared bucket.
func Auth(r *identity.Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, scopes, ok := r.UserFromHeaders(req.Context(), req.Header)
		if !ok {
			Error(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		site, ok := tenant(req.Header.Get("X-Loft-Site"))
		if !ok {
			Error(w, http.StatusInternalServerError, "tenant not set: the proxy must send X-Loft-Site for every request")
			return
		}
		ctx := context.WithValue(req.Context(), ctxKey{}, user)
		ctx = context.WithValue(ctx, scopeKey{}, scopes)
		ctx = context.WithValue(ctx, siteKey{}, site)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

// User returns the authenticated user placed in the context by Auth. The bool is false if the
// handler was reached without the middleware (a programming error).
func User(ctx context.Context) (identity.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(identity.User)
	return u, ok
}

// RequireScope gates a handler on the caller's token carrying `scope`: a credential without it (the
// CLI's deploy-only token, which lacks the full API scope) gets 403. Wrap it INSIDE Auth, which puts
// the scopes in the context. An empty scope is a no-op (scope enforcement off).
func RequireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		scopes, _ := req.Context().Value(scopeKey{}).([]string)
		if scope != "" && !slices.Contains(scopes, scope) {
			Error(w, http.StatusForbidden, "this credential is not allowed to use this endpoint")
			return
		}
		next.ServeHTTP(w, req)
	})
}

// Site is the calling tenant, as Auth resolved it from the X-Loft-Site header. The ingress proxy
// sets that header from the validated server name and overwrites it on every request, so a client
// cannot spoof it to reach another tenant. We must NEVER derive the tenant from a client-controllable
// value such as X-Forwarded-Host or the raw Host: that would let any caller read or write another
// site's data (the boundary RLS and the upload key prefix both hang off this). Empty only if the
// handler was reached without Auth (a programming error); consumers must refuse an empty site, not
// default it.
func Site(req *http.Request) string {
	s, _ := req.Context().Value(siteKey{}).(string)
	return s
}

// tenant parses an X-Loft-Site value. ok is false when the proxy named no tenant: the header is
// absent or empty, or holds nothing a hostname label could carry. The apex is only ever named
// explicitly.
func tenant(h string) (string, bool) {
	if h == Apex {
		return Apex, true
	}
	s := nonLabel.ReplaceAllString(toLower(h), "-")
	if strings.Trim(s, "-") == "" {
		return "", false
	}
	return s, true
}

// SanitizeLabel lowercases and reduces a string to [a-z0-9-], or the apex if nothing remains.
func SanitizeLabel(s string) string {
	s = nonLabel.ReplaceAllString(toLower(s), "-")
	if s == "" {
		return Apex
	}
	return s
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// JSON writes v as a JSON response with no-store caching.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a plain-text error response.
func Error(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}
