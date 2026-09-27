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
	maxGuardedWebQueryRunes  = 320
	maxGuardedWebSourceRunes = 2048
	maxGuardedWebQueries     = 6
	maxUserWebURLScanBytes   = 16384
	maxExplicitWebURLs       = 16
)

var userWebURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

// WebEgressGuard is one user turn's public-web authority. Construct it only
// from the original user message, before private knowledge/tool output enters
// the model context. Never share a guard across turns or tenants.
//
// A model may decide to search and select one of the bounded, normalized
// original-user-text queries. It cannot contribute new outbound text. Fetches
// are limited to URLs in the user message or returned by this turn's search.
type WebEgressGuard struct {
	queries []string
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
		queries: guardedWebQueries(originalUserTurn),
		urls:    make(map[string]struct{}),
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

// guardedWebQueries offers the full question and a few literal clauses from
// the same user turn. After private retrieval the model can choose among
// these strings, but cannot invent a public query with retrieved material.
func guardedWebQueries(original string) []string {
	full := normalizeGuardedWebQuery(original)
	if full == "" {
		return nil
	}
	queries := make([]string, 0, maxGuardedWebQueries)
	queries = append(queries, full)

	// Collect literal clauses from a bounded prefix, then sample across their
	// positions. Taking only the first few would lose a final question after
	// several context clauses, a common shape of a longer user turn.
	clauses := make([]string, 0)
	clauseSeen := make(map[string]struct{})
	addClause := func(value string) {
		query := normalizeGuardedWebQuery(value)
		if query == "" || query == full {
			return
		}
		if _, exists := clauseSeen[query]; exists {
			return
		}
		clauseSeen[query] = struct{}{}
		clauses = append(clauses, query)
	}
	var clause strings.Builder
	count := 0
	for _, r := range original {
		if count >= maxGuardedWebSourceRunes {
			break
		}
		count++
		if isGuardedWebClauseBoundary(r) {
			addClause(clause.String())
			clause.Reset()
			continue
		}
		clause.WriteRune(r)
	}
	addClause(clause.String())
	slots := maxGuardedWebQueries - len(queries)
	if len(clauses) <= slots {
		queries = append(queries, clauses...)
		return queries
	}
	for i := 0; i < slots; i++ {
		index := 0
		if slots > 1 {
			index = i * (len(clauses) - 1) / (slots - 1)
		}
		queries = append(queries, clauses[index])
	}
	return queries
}

func isGuardedWebClauseBoundary(r rune) bool {
	switch r {
	case '\n', '\r', '。', '！', '？', '!', '?', '；', ';', '，', ',', '、':
		return true
	default:
		return false
	}
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

// SearchQuery is the default query for an empty model argument.
func (g *WebEgressGuard) SearchQuery() string {
	if g == nil || len(g.queries) == 0 {
		return ""
	}
	return g.queries[0]
}

// AllowedSearchQueries returns a copy of the pre-authorized user-text queries.
func (g *WebEgressGuard) AllowedSearchQueries() []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.queries...)
}

// ResolveSearchQuery selects one exact original-user-text query. Empty selects
// the full question. Other model text is rejected, never silently substituted.
func (g *WebEgressGuard) ResolveSearchQuery(modelQuery string) (string, bool) {
	// Validate the entire model argument before comparison. Truncating it at
	// the public limit could hide an appended private suffix behind whitespace.
	var valid bool
	modelQuery, valid = normalizeModelWebQuery(modelQuery)
	if !valid {
		return "", false
	}
	if g == nil || len(g.queries) == 0 {
		return "", false
	}
	if modelQuery == "" {
		return g.queries[0], true
	}
	for _, query := range g.queries {
		if modelQuery == query {
			return query, true
		}
	}
	return "", false
}

func normalizeModelWebQuery(value string) (string, bool) {
	if len(value) > 4096 {
		return "", false
	}
	var out strings.Builder
	count := 0
	pendingSpace := false
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			if count > 0 {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			if count >= maxGuardedWebQueryRunes {
				return "", false
			}
			out.WriteByte(' ')
			count++
			pendingSpace = false
		}
		if count >= maxGuardedWebQueryRunes {
			return "", false
		}
		out.WriteRune(r)
		count++
	}
	return out.String(), true
}

// AllowsSearchQuery is retained for callers needing only an admission check.
func (g *WebEgressGuard) AllowsSearchQuery(modelQuery string) bool {
	_, ok := g.ResolveSearchQuery(modelQuery)
	return ok
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
