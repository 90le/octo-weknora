package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(t *testing.T, paths []string, truncated bool) (*Connector, *types.DataSourceConfig, *[]string) {
	t.Helper()
	requests := []string{}
	c := NewConnector()
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "api.github.com", r.URL.Host)
		requests = append(requests, r.URL.EscapedPath())
		var payload interface{}
		switch {
		case r.URL.Path == "/repos/test/docs":
			payload = map[string]string{"full_name": "test/docs", "default_branch": "main"}
		case strings.HasPrefix(r.URL.Path, "/repos/test/docs/commits/"):
			payload = map[string]interface{}{"sha": strings.Repeat("a", 40), "commit": map[string]interface{}{"tree": map[string]string{"sha": strings.Repeat("b", 40)}}}
		case strings.Contains(r.URL.Path, "/git/trees/"):
			es := []entry{}
			for _, p := range paths {
				es = append(es, entry{Path: p, Type: "blob", Mode: "100644", SHA: strings.Repeat("c", 40), Size: 6})
			}
			payload = tree{Tree: es, Truncated: truncated}
		case strings.Contains(r.URL.Path, "/git/blobs/"):
			payload = map[string]string{"sha": strings.Repeat("c", 40), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("hello\n"))}
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		b, err := json.Marshal(payload)
		require.NoError(t, err)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
	})
	return c, &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "test/docs", "ref": "feature/docs"}}, &requests
}
func TestPinnedDocumentsAndIncrementalSync(t *testing.T) {
	c, cfg, requests := fixture(t, []string{"guide.md", "src/app.go", ".env", "node_modules/pkg/README.md"}, false)
	items, next, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "test/docs/guide.md", items[0].FileName)
	require.Equal(t, "https://github.com/test/docs/blob/"+strings.Repeat("a", 40)+"/guide.md", items[0].Metadata["github_url"])
	require.Contains(t, *requests, "/repos/test/docs/commits/feature%2Fdocs")
	items, _, err = c.FetchIncremental(context.Background(), cfg, next)
	require.NoError(t, err)
	require.Empty(t, items)
}
func TestCompleteManifestRequiredForDeletions(t *testing.T) {
	c, cfg, _ := fixture(t, []string{"old.md", "keep.md"}, false)
	_, previous, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.NoError(t, err)
	c, _, _ = fixture(t, []string{"keep.md"}, true)
	items, next, err := c.FetchIncremental(context.Background(), cfg, previous)
	require.ErrorContains(t, err, "truncated")
	require.Nil(t, items)
	require.Nil(t, next)
	c, _, _ = fixture(t, []string{"keep.md"}, false)
	items, _, err = c.FetchIncremental(context.Background(), cfg, previous)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.True(t, items[0].IsDeleted)
	cfg.Settings["paths"] = []string{"keep.md"}
	items, _, err = c.FetchIncremental(context.Background(), cfg, previous)
	require.NoError(t, err)
	for _, i := range items {
		require.False(t, i.IsDeleted, "scope edits must not imply remote deletion")
	}
}
func TestInvalidPathsAndUnsupportedModes(t *testing.T) {
	for _, p := range []string{"../secret.md", "docs/../../secret.md", "/secret", "a\\b.md", "a\x00b.md"} {
		require.False(t, safePath(p), p)
	}
	for _, mode := range []string{"120000", "160000"} {
		require.False(t, allowedDocument(entry{Path: "guide.md", Mode: mode, Type: "blob"}))
	}
	_, err := parseSelection(&types.DataSourceConfig{Settings: map[string]interface{}{"repository": "https://evil.example/test/docs"}})
	require.Error(t, err)
	c, cfg, _ := fixture(t, []string{"guide.md"}, false)
	cfg.Settings["paths"] = []string{"missing"}
	_, next, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.Error(t, err)
	require.Nil(t, next)
}
func TestTokenNeverFollowsRedirectOrAppearsInError(t *testing.T) {
	c := NewConnector()
	calls := 0
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "api.github.com", r.URL.Host)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.example/token"}}, Body: io.NopCloser(strings.NewReader("secret-token")), Request: r}, nil
	})
	err := c.Validate(context.Background(), &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "secret-token"}})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-token")
	require.Equal(t, 1, calls)
}
