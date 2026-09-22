package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func releaseTestConfig() *types.DataSourceConfig {
	return &types.DataSourceConfig{
		Credentials: map[string]interface{}{"access_token": "release-test-token"},
		Settings:    map[string]interface{}{"repository": "Acme/Widget", "mode": "source"},
	}
}

func TestFetchReleaseCatalogUsesConditionalETagAndKeepsTagsSeparate(t *testing.T) {
	var releaseCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer release-test-token", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/Acme/Widget/releases":
			releaseCalls.Add(1)
			if r.Header.Get("If-None-Match") == `"release-v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"release-v1"`)
			_, _ = w.Write([]byte(`[
				{"id":2,"tag_name":"v2.0.0-rc.1","name":"RC","body":"pre","published_at":"2026-09-02T00:00:00Z","created_at":"2026-09-02T00:00:00Z","prerelease":true},
				{"id":1,"tag_name":"v1.9.0","name":"Stable","body":"release notes","published_at":"2026-09-01T00:00:00Z","created_at":"2026-09-01T00:00:00Z","target_commitish":"main","assets":[{"name":"widget.zip","browser_download_url":"https://github.com/Acme/Widget/releases/download/v1.9.0/widget.zip","size":12}]}
			]`))
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cache := NewReleaseCatalogCache()
	cache.ttl = 0 // force the second call down the ETag path
	connector := NewConnectorWithHTTPClient(server.Client(), server.URL)
	first, err := connector.FetchReleaseCatalog(context.Background(), cache, "ds-1", releaseTestConfig(), false)
	require.NoError(t, err)
	latest, ok := first.LatestStable()
	require.True(t, ok)
	require.Equal(t, "v1.9.0", latest.TagName)
	require.Equal(t, "https://github.com/Acme/Widget/releases/tag/v1.9.0", latest.URL)
	require.Len(t, latest.Assets, 1)
	prerelease, ok := first.LatestPrerelease()
	require.True(t, ok)
	require.Equal(t, "v2.0.0-rc.1", prerelease.TagName)
	require.True(t, first.HistoryComplete)

	second, err := connector.FetchReleaseCatalog(context.Background(), cache, "ds-1", releaseTestConfig(), false)
	require.NoError(t, err)
	latest, ok = second.LatestStable()
	require.True(t, ok)
	require.Equal(t, "v1.9.0", latest.TagName)
	require.Equal(t, int32(2), releaseCalls.Load())
	require.Empty(t, second.Tags, "release lookup must not manufacture tags from releases")
}

func TestFetchReleaseCatalogUsesTagFallbackWithoutCallingItARelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Acme/Widget/releases":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/Acme/Widget/tags":
			_, _ = w.Write([]byte(`[{"name":"v0.8.0","commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]`))
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	catalog, err := NewConnectorWithHTTPClient(server.Client(), server.URL).
		FetchReleaseCatalog(context.Background(), NewReleaseCatalogCache(), "ds-2", releaseTestConfig(), true)
	require.NoError(t, err)
	_, ok := catalog.LatestStable()
	require.False(t, ok)
	require.Equal(t, []Tag{{
		Name:      "v0.8.0",
		CommitSHA: strings.Repeat("a", 40),
		URL:       "https://github.com/Acme/Widget/tree/v0.8.0",
	}}, catalog.Tags)
}

func TestFetchLatestReleaseUsesGitHubLatestEndpointRatherThanHistoryPublishedAt(t *testing.T) {
	var latestCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Acme/Widget/releases":
			// A release drafted against an old commit and published today sorts
			// ahead by published_at in the bounded history catalog, but it is not
			// GitHub's latest release.
			_, _ = w.Write([]byte(`[
				{"id":1,"tag_name":"v1.0.0","published_at":"2026-09-22T00:00:00Z","created_at":"2025-01-01T00:00:00Z"},
				{"id":2,"tag_name":"v2.0.0","published_at":"2026-09-21T00:00:00Z","created_at":"2026-09-21T00:00:00Z"}
			]`))
		case "/repos/Acme/Widget/releases/latest":
			latestCalls.Add(1)
			if r.Header.Get("If-None-Match") == `"latest-v2"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"latest-v2"`)
			_, _ = w.Write([]byte(`{"id":2,"tag_name":"v2.0.0","published_at":"2026-09-21T00:00:00Z","created_at":"2026-09-21T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cache := NewReleaseCatalogCache()
	cache.ttl = 0 // exercise conditional validation on the second latest call
	connector := NewConnectorWithHTTPClient(server.Client(), server.URL)
	catalog, err := connector.FetchReleaseCatalog(context.Background(), cache, "ds-latest", releaseTestConfig(), false)
	require.NoError(t, err)
	legacy, ok := catalog.LatestStable()
	require.True(t, ok)
	require.Equal(t, "v1.0.0", legacy.TagName, "the history sort is intentionally not latest authority")

	latest, err := connector.FetchLatestRelease(context.Background(), cache, "ds-latest", releaseTestConfig())
	require.NoError(t, err)
	require.NotNil(t, latest.LatestStable)
	require.Equal(t, "v2.0.0", latest.LatestStable.TagName)
	require.False(t, latest.NoPublishedStable)
	require.False(t, latest.Stale)

	validated, err := connector.FetchLatestRelease(context.Background(), cache, "ds-latest", releaseTestConfig())
	require.NoError(t, err)
	require.NotNil(t, validated.LatestStable)
	require.Equal(t, "v2.0.0", validated.LatestStable.TagName)
	require.Equal(t, int32(2), latestCalls.Load(), "latest endpoint should use ETag validation")
}

func TestFetchLatestReleaseMarksCachedResultStaleAfterRefreshFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/Acme/Widget/releases/latest" {
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
		}
		if calls.Add(1) == 1 {
			w.Header().Set("ETag", `"latest-v1"`)
			_, _ = w.Write([]byte(`{"id":1,"tag_name":"v1.0.0","published_at":"2026-09-01T00:00:00Z","created_at":"2026-09-01T00:00:00Z"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cache := NewReleaseCatalogCache()
	cache.ttl = 0
	connector := NewConnectorWithHTTPClient(server.Client(), server.URL)
	first, err := connector.FetchLatestRelease(context.Background(), cache, "ds-stale", releaseTestConfig())
	require.NoError(t, err)
	require.NotNil(t, first.LatestStable)

	stale, err := connector.FetchLatestRelease(context.Background(), cache, "ds-stale", releaseTestConfig())
	require.NoError(t, err)
	require.NotNil(t, stale.LatestStable)
	require.Equal(t, "v1.0.0", stale.LatestStable.TagName)
	require.True(t, stale.Stale)
}

func TestReleaseCatalogCacheSeparatesCredentialReplacements(t *testing.T) {
	first := releaseCatalogCacheKey("ds", "Acme/Widget", &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "one"}})
	second := releaseCatalogCacheKey("ds", "Acme/Widget", &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "two"}})
	require.NotEqual(t, first, second)
	require.NotContains(t, first, "one")
}
