package tools

import (
	"context"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	maxGuardedWebQueryRunes = 320
	maxUserWebURLScanBytes  = 16384
	maxExplicitWebURLs      = 16
)

var userWebURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

// WebEgressGuard is one user turn's public-web authority. Construct it only
// from the original user message, before private knowledge/tool output enters
// the model context. Never share a guard across turns or tenants.
//
// A model may decide to search, but its query cannot contribute outbound text:
// only the bounded, normalized user message can. Fetches are limited to URLs
// explicitly present in that message or returned by this turn's search.
type WebEgressGuard struct {
	query string
	// authorizer is assigned once before the per-turn tools can execute.
	authorizer func(context.Context) error
	mu         sync.RWMutex
	urls       map[string]struct{}
}

// WithAuthorizer adds a live permission check to every outbound search/fetch.
// Configure this only before the guard is attached to running tools. The
// callback can verify current IM membership, binding and policy revision.
func (g *WebEgressGuard) WithAuthorizer(authorizer func(context.Context) error) *WebEgressGuard {
	if g != nil {
		g.authorizer = authorizer
	}
	return g
}

// Authorize must be called immediately before a provider/network request.
func (g *WebEgressGuard) Authorize(ctx context.Context) error {
	if g == nil || g.authorizer == nil {
		return nil
	}
	return g.authorizer(ctx)
}

// NewWebEgressGuard creates a request-local guard. An empty query means web
// search is unavailable for this turn; explicit public URLs can still be read.
func NewWebEgressGuard(originalUserTurn string) *WebEgressGuard {
	guard := &WebEgressGuard{
		query: normalizeGuardedWebQuery(originalUserTurn),
		urls:  make(map[string]struct{}),
	}
	if len(originalUserTurn) > maxUserWebURLScanBytes {
		originalUserTurn = originalUserTurn[:maxUserWebURLScanBytes]
	}
	for _, match := range userWebURLPattern.FindAllString(originalUserTurn, maxExplicitWebURLs) {
		candidate := strings.TrimRight(match, ".,;:!?)]}>，。；：！？）】》")
		guard.allowURL(candidate)
	}
	return guard
}

func normalizeGuardedWebQuery(value string) string {
	return normalizeGuardedWebQueryUpTo(value, maxGuardedWebQueryRunes)
}

func normalizeGuardedWebQueryUpTo(value string, limit int) string {
	// Discard control characters before collapsing whitespace. The query is
	// drawn from user text alone, never from model-selected terms or KB output.
	var out strings.Builder
	lastSpace := true
	count := 0
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			if !lastSpace && count < limit {
				out.WriteByte(' ')
				count++
				lastSpace = true
			}
			continue
		}
		if count >= limit {
			break
		}
		out.WriteRune(r)
		count++
		lastSpace = false
	}
	return strings.TrimSpace(out.String())
}

// SearchQuery returns the only query this turn may send to a web provider.
func (g *WebEgressGuard) SearchQuery() string {
	if g == nil {
		return ""
	}
	return g.query
}

// AllowsSearchQuery returns true for the exact normalized user message or an
// empty model query. Empty lets the model choose the search tool without
// constructing outbound text. All other model text is rejected, not silently
// substituted, so attempted egress has zero provider requests.
func (g *WebEgressGuard) AllowsSearchQuery(modelQuery string) bool {
	// Read one rune beyond the outbound limit so a model cannot append private
	// text after a 320-rune prefix and have it compare equal by truncation.
	modelQuery = normalizeGuardedWebQueryUpTo(modelQuery, maxGuardedWebQueryRunes+1)
	return g != nil && g.query != "" &&
		(modelQuery == "" || modelQuery == g.query)
}

// AllowSearchResult grants a URL returned by this turn's search provider.
// Only public HTTP(S) URLs qualify. The fetcher's DNS/redirect SSRF checks
// remain mandatory and are deliberately not replaced by this static check.
func (g *WebEgressGuard) AllowSearchResult(rawURL string) bool {
	if g == nil {
		return false
	}
	return g.allowURL(rawURL)
}

func (g *WebEgressGuard) allowURL(rawURL string) bool {
	if !isPublicWebURL(rawURL) {
		return false
	}
	key := canonicalFetchURL(rawURL)
	g.mu.Lock()
	g.urls[key] = struct{}{}
	g.mu.Unlock()
	return true
}

// AllowsFetch must run before cache lookup or any network operation. It uses
// the complete canonical URL, including query parameters: a model cannot add
// private data to a previously authorized host/path.
func (g *WebEgressGuard) AllowsFetch(rawURL string) bool {
	if g == nil || !isPublicWebURL(rawURL) {
		return false
	}
	g.mu.RLock()
	_, ok := g.urls[canonicalFetchURL(rawURL)]
	g.mu.RUnlock()
	return ok
}

func isPublicWebURL(rawURL string) bool {
	if rawURL == "" || len(rawURL) > 2048 || !utf8.ValidString(rawURL) {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.User != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".invalid", ".test"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	// A credential-bearing or control-character URL is never public input.
	for _, r := range rawURL {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
