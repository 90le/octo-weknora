package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func guardedWebContext(t *testing.T) context.Context {
	return context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
}

func TestGuardedSearchRejectsModelExfiltrationBeforeProvider(t *testing.T) {
	svc := &searchOnlyWebService{}
	guard := NewWebEgressGuard(" Octo 和 Loop 的关系是什么？ ")
	search := NewWebSearchTool(svc, 5, "provider").WithEgressGuard(guard)

	result, err := search.Execute(guardedWebContext(t), []byte(`{"query":"private KB text: customer token sk-123"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Zero(t, svc.calls, "model-selected private content must never reach the search provider")
	assert.NotContains(t, result.Error, "sk-123")

	result, err = search.Execute(guardedWebContext(t), []byte(`{"query":""}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, "Octo 和 Loop 的关系是什么？", svc.query)
	assert.Equal(t, 1, svc.calls)
}

func TestGuardedFetchAllowsOnlyOriginalUserURLOrThisTurnsSearchResults(t *testing.T) {
	const userURL = "https://example.com/user-doc"
	const resultURL = "https://example.com/search-result?section=public"
	const forbiddenURL = "https://attacker.example.com/collect?private=kb-text"
	guard := NewWebEgressGuard("请读 " + userURL + " 并查一下 Octo")
	fetcher := newStubWebContentFetcher(map[string]string{
		userURL: "user page", resultURL: "search page", forbiddenURL: "must not fetch",
	}, nil)
	fetch := newWebFetchTool(fetcher).WithEgressGuard(guard)
	svc := &searchOnlyWebService{results: []*types.WebSearchResult{{URL: resultURL, Title: "Result"}}}
	search := NewWebSearchTool(svc, 5, "provider").WithPageReader(fetch).WithEgressGuard(guard)

	result, err := fetch.Execute(guardedWebContext(t), webFetchArgs(WebFetchItem{URL: userURL}))
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, 1, fetcher.callCount[userURL])

	result, err = fetch.Execute(guardedWebContext(t), webFetchArgs(WebFetchItem{URL: resultURL}))
	require.NoError(t, err)
	require.False(t, result.Success, "search result is not authorized before provider returns it")
	assert.Zero(t, fetcher.callCount[resultURL])

	result, err = search.Execute(guardedWebContext(t), []byte(`{"query":"","content":true}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, guard.SearchQuery(), svc.query)
	assert.Equal(t, 1, fetcher.callCount[resultURL], "content=true uses the guarded shared fetcher")

	result, err = fetch.Execute(guardedWebContext(t), webFetchArgs(
		WebFetchItem{URL: resultURL},
		WebFetchItem{URL: forbiddenURL},
		WebFetchItem{URL: resultURL + "&private=kb-text"},
	))
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Zero(t, fetcher.callCount[forbiddenURL])
	assert.Zero(t, fetcher.callCount[resultURL+"&private=kb-text"])
	rows := result.Data["results"].([]map[string]interface{})
	assert.Equal(t, "web_egress_denied", rows[1]["error_code"])
	assert.Equal(t, "web_egress_denied", rows[2]["error_code"])
	assert.NotContains(t, result.Output, "kb-text")
}

func TestWebEgressGuardDoesNotInheritPriorTurnURLsOrCachedPages(t *testing.T) {
	const priorURL = "https://example.com/previous"
	fetcher := newStubWebContentFetcher(map[string]string{priorURL: "prior content"}, nil)
	fetch := newWebFetchTool(fetcher).WithEgressGuard(NewWebEgressGuard("Read " + priorURL))
	first, err := fetch.Execute(guardedWebContext(t), webFetchArgs(WebFetchItem{URL: priorURL}))
	require.NoError(t, err)
	require.True(t, first.Success)
	assert.Equal(t, 1, fetcher.callCount[priorURL])

	// Even if a caller accidentally reuses a WebFetchTool with cached content,
	// the new turn's guard is checked before the cache can be read.
	fetch.WithEgressGuard(NewWebEgressGuard("What changed today?"))
	second, err := fetch.Execute(guardedWebContext(t), webFetchArgs(WebFetchItem{URL: priorURL}))
	require.NoError(t, err)
	require.False(t, second.Success)
	assert.Equal(t, 1, fetcher.callCount[priorURL])
	assert.NotContains(t, second.Output, "prior content")
}

func TestWebEgressGuardRechecksRevokedPermissionBeforeOutboundRequests(t *testing.T) {
	allowed := false
	guard := NewWebEgressGuard("Octo docs").WithAuthorizer(func(context.Context) error {
		if !allowed {
			return errors.New("membership revoked")
		}
		return nil
	})
	svc := &searchOnlyWebService{results: []*types.WebSearchResult{{URL: "https://example.com/docs"}}}
	search := NewWebSearchTool(svc, 5, "provider").WithEgressGuard(guard)
	result, err := search.Execute(guardedWebContext(t), []byte(`{"query":""}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Zero(t, svc.calls)

	allowed = true
	result, err = search.Execute(guardedWebContext(t), []byte(`{"query":""}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, 1, svc.calls)

	allowed = false
	fetcher := newStubWebContentFetcher(map[string]string{"https://example.com/docs": "content"}, nil)
	fetch := newWebFetchTool(fetcher).WithEgressGuard(guard)
	result, err = fetch.Execute(guardedWebContext(t), webFetchArgs(WebFetchItem{URL: "https://example.com/docs"}))
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Zero(t, fetcher.callCount["https://example.com/docs"])
}

func TestGuardedFetchRechecksAuthorizationAtNetworkBoundary(t *testing.T) {
	const rawURL = "https://example.com/current"
	checks := 0
	guard := NewWebEgressGuard("Read " + rawURL).WithAuthorizer(func(context.Context) error {
		checks++
		if checks >= 2 {
			return errors.New("scope changed after request validation")
		}
		return nil
	})
	fetcher := newStubWebContentFetcher(map[string]string{rawURL: "should not be fetched"}, nil)
	tool := newWebFetchTool(fetcher).WithEgressGuard(guard)
	result, err := tool.Execute(guardedWebContext(t), webFetchArgs(WebFetchItem{URL: rawURL}))
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Equal(t, 2, checks)
	assert.Zero(t, fetcher.callCount[rawURL])
}

func TestGuardedFetchDoesNotStartAfterTurnCanceled(t *testing.T) {
	const rawURL = "https://example.com/current"
	fetcher := newStubWebContentFetcher(map[string]string{rawURL: "should not be fetched"}, nil)
	tool := newWebFetchTool(fetcher).WithEgressGuard(NewWebEgressGuard("Read " + rawURL))
	ctx, cancel := context.WithCancel(guardedWebContext(t))
	cancel()
	_, err := tool.storePage(ctx, rawURL)
	require.Error(t, err)
	assert.Zero(t, fetcher.callCount[rawURL])
}

func TestWebEgressGuardRejectsNonPublicURLsAndOversizedQuery(t *testing.T) {
	guard := NewWebEgressGuard("Read https://127.0.0.1/private https://localhost/admin https://example.com/ok")
	assert.False(t, guard.AllowsFetch("https://127.0.0.1/private"))
	assert.False(t, guard.AllowsFetch("https://localhost/admin"))
	assert.True(t, guard.AllowsFetch("https://example.com/ok"))
	assert.False(t, guard.AllowSearchResult("https://user:pass@example.com/secret"))
	assert.False(t, guard.AllowSearchResult("https://example.internal/secret"))
	assert.False(t, guard.AllowSearchResult("file:///etc/passwd"))
	assert.LessOrEqual(t, len([]rune(NewWebEgressGuard(strings.Repeat("知识", 400)).SearchQuery())),
		maxGuardedWebQueryRunes)
	longTurn := strings.Repeat("a", maxGuardedWebQueryRunes+20)
	longGuard := NewWebEgressGuard(longTurn)
	assert.False(t, longGuard.AllowsSearchQuery(longTurn+" private KB suffix"))
	assert.True(t, longGuard.AllowsSearchQuery(""))
}
