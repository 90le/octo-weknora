package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type unexpectedEOFReader struct{}

func (unexpectedEOFReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func githubTestResponse(r *http.Request, status int, body io.Reader) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(body), Request: r}
}

func TestGitHubResponseReadFailureDiffersFromSizeLimit(t *testing.T) {
	const endpoint = "/repos/test/docs/git/blobs/abc"
	t.Run("mid-stream disconnect", func(t *testing.T) {
		c := NewConnector()
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return githubTestResponse(r, http.StatusOK, io.MultiReader(strings.NewReader(`{"sha":`), unexpectedEOFReader{})), nil
		})
		var out map[string]any
		err := c.get(context.Background(), &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "secret-token"}}, endpoint, &out, 40)
		var apiErr *Error
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, "github_response_incomplete", apiErr.Code)
		require.Equal(t, "blob", apiErr.Stage)
		require.EqualValues(t, 7, apiErr.BytesRead)
		require.NotContains(t, err.Error(), endpoint)
		require.NotContains(t, err.Error(), "secret-token")
	})
	t.Run("body exceeds cap", func(t *testing.T) {
		c := NewConnector()
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return githubTestResponse(r, http.StatusOK, strings.NewReader("123456")), nil
		})
		var out map[string]any
		err := c.get(context.Background(), &types.DataSourceConfig{}, endpoint, &out, 4)
		var apiErr *Error
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, "github_response_limit", apiErr.Code)
		require.Equal(t, "blob", apiErr.Stage)
		require.EqualValues(t, 5, apiErr.BytesRead)
		require.EqualValues(t, 4, apiErr.LimitBytes)
	})
	t.Run("declared length exceeds returned bytes", func(t *testing.T) {
		c := NewConnector()
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp := githubTestResponse(r, http.StatusOK, strings.NewReader("part"))
			resp.ContentLength = 10
			return resp, nil
		})
		var out map[string]any
		err := c.get(context.Background(), &types.DataSourceConfig{}, endpoint, &out, 40)
		var apiErr *Error
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, "github_response_incomplete", apiErr.Code)
		require.EqualValues(t, 4, apiErr.BytesRead)
	})
}

func TestGitHubDocumentBlobRetryIsBoundedAndCancelable(t *testing.T) {
	const endpoint = "/repos/test/docs/git/blobs/abc"
	t.Run("transient disconnect then success", func(t *testing.T) {
		calls := 0
		c := NewConnector()
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return githubTestResponse(r, http.StatusOK, io.MultiReader(strings.NewReader("{"), unexpectedEOFReader{})), nil
			}
			return githubTestResponse(r, http.StatusOK, strings.NewReader(`{"sha":"ok"}`)), nil
		})
		var out map[string]string
		require.NoError(t, c.getDocumentBlob(context.Background(), &types.DataSourceConfig{}, endpoint, &out))
		require.Equal(t, "ok", out["sha"])
		require.Equal(t, 2, calls)
	})
	t.Run("context cancels backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		c := NewConnector()
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			go func() { time.Sleep(10 * time.Millisecond); cancel() }()
			return githubTestResponse(r, http.StatusServiceUnavailable, strings.NewReader("unavailable")), nil
		})
		var out map[string]string
		require.ErrorIs(t, c.getDocumentBlob(ctx, &types.DataSourceConfig{}, endpoint, &out), context.Canceled)
		require.Equal(t, 1, calls)
	})
	t.Run("long retry-after is surfaced, not slept", func(t *testing.T) {
		calls := 0
		c := NewConnector()
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			resp := githubTestResponse(r, http.StatusTooManyRequests, strings.NewReader("rate limited"))
			resp.Header.Set("Retry-After", "60")
			return resp, nil
		})
		var out map[string]string
		start := time.Now()
		err := c.getDocumentBlob(context.Background(), &types.DataSourceConfig{}, endpoint, &out)
		var apiErr *Error
		require.ErrorAs(t, err, &apiErr)
		require.NotNil(t, apiErr.RetryAfter)
		require.Equal(t, 1, calls)
		require.Less(t, time.Since(start), time.Second)
	})
	t.Run("size limit is not retried", func(t *testing.T) {
		calls := 0
		c := NewConnector()
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			resp := githubTestResponse(r, http.StatusOK, strings.NewReader("{}"))
			resp.ContentLength = 24<<20 + 1
			return resp, nil
		})
		var out map[string]string
		err := c.getDocumentBlob(context.Background(), &types.DataSourceConfig{}, endpoint, &out)
		var apiErr *Error
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, "github_response_limit", apiErr.Code)
		require.Equal(t, 1, calls)
	})
}

func TestGitHubDocumentFailurePreservesPreviousManifest(t *testing.T) {
	oldSHA, newSHA := strings.Repeat("c", 40), strings.Repeat("d", 40)
	oldCommit, newCommit := strings.Repeat("a", 40), strings.Repeat("e", 40)
	oldTree, newTree := strings.Repeat("b", 40), strings.Repeat("f", 40)
	changed, disconnect := false, false
	blobCalls := 0
	c := NewConnector()
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload interface{}
		switch {
		case r.URL.Path == "/repos/test/docs":
			payload = map[string]string{"full_name": "test/docs", "default_branch": "main"}
		case strings.Contains(r.URL.Path, "/commits/"):
			commit, treeSHA := oldCommit, oldTree
			if changed {
				commit, treeSHA = newCommit, newTree
			}
			payload = map[string]interface{}{"sha": commit, "commit": map[string]interface{}{"tree": map[string]string{"sha": treeSHA}}}
		case strings.Contains(r.URL.Path, "/git/trees/"):
			sha := oldSHA
			if changed {
				sha = newSHA
			}
			payload = tree{Tree: []entry{{Path: "guide.md", Type: "blob", Mode: "100644", SHA: sha, Size: 6}}}
		case strings.Contains(r.URL.Path, "/git/blobs/"):
			blobCalls++
			if disconnect {
				return githubTestResponse(r, http.StatusOK, io.MultiReader(strings.NewReader(`{"sha":`), unexpectedEOFReader{})), nil
			}
			sha, content := oldSHA, "old-v1"
			if changed {
				sha, content = newSHA, "new-v2"
			}
			payload = map[string]string{"sha": sha, "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))}
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		return githubTestResponse(r, http.StatusOK, strings.NewReader(string(body))), nil
	})
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "test/docs", "ref": "main"}}
	previousItems, previous, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.Len(t, previousItems, 1)
	require.Equal(t, "old-v1", string(previousItems[0].Content))
	previousCursor, err := json.Marshal(previous.ConnectorCursor)
	require.NoError(t, err)
	changed, disconnect = true, true
	items, next, err := c.FetchIncremental(context.Background(), cfg, previous)
	var apiErr *Error
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, "github_response_incomplete", apiErr.Code)
	require.Nil(t, items)
	require.Nil(t, next, "failed fetch cannot acknowledge the new manifest")
	currentCursor, err := json.Marshal(previous.ConnectorCursor)
	require.NoError(t, err)
	require.Equal(t, string(previousCursor), string(currentCursor), "previous cursor must remain unchanged")
	require.Equal(t, "old-v1", string(previousItems[0].Content), "previously fetched content is not replaced")
	require.Equal(t, 4, blobCalls, "initial read plus three bounded attempts")
	disconnect = false
	items, next, err = c.FetchIncremental(context.Background(), cfg, previous)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Len(t, items, 1)
	require.Equal(t, "new-v2", string(items[0].Content))
}
