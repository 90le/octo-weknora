package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func githubPreviewFixture(t *testing.T, entries []entry, truncated bool) (*Connector, *atomic.Int32, func()) {
	t.Helper()
	commit := strings.Repeat("a", 40)
	treeSHA := strings.Repeat("b", 40)
	var blobs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/example/repo":
			_ = json.NewEncoder(w).Encode(map[string]string{"full_name": "example/repo", "default_branch": "main"})
		case "/repos/example/repo/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": commit, "commit": map[string]any{"tree": map[string]string{"sha": treeSHA}},
			})
		case "/repos/example/repo/git/trees/" + treeSHA:
			require.Equal(t, "1", r.URL.Query().Get("recursive"))
			_ = json.NewEncoder(w).Encode(tree{Tree: entries, Truncated: truncated})
		default:
			if strings.Contains(r.URL.Path, "/git/blobs/") {
				blobs.Add(1)
			}
			http.NotFound(w, r)
		}
	}))
	return NewConnectorWithHTTPClient(server.Client(), server.URL), &blobs, server.Close
}

func previewEntries() []entry {
	sha := strings.Repeat("c", 40)
	return []entry{
		{Path: "README.md", Type: "blob", Mode: "100644", SHA: sha, Size: 100},
		{Path: "docs", Type: "tree", Mode: "040000", SHA: sha},
		{Path: "docs/setup.mdx", Type: "blob", Mode: "100644", SHA: sha, Size: 200},
		{Path: "docs/large.pdf", Type: "blob", Mode: "100644", SHA: sha, Size: 17 << 20},
		{Path: "images/logo.webp", Type: "blob", Mode: "100644", SHA: sha, Size: 300},
		{Path: "src/app.js", Type: "blob", Mode: "100644", SHA: sha, Size: 400},
		{Path: ".github/workflows/release.md", Type: "blob", Mode: "100644", SHA: sha, Size: 50},
	}
}

func TestPreviewDocumentTreeFullAndSelectedPathsNeverReadBlobs(t *testing.T) {
	c, blobs, closeServer := githubPreviewFixture(t, previewEntries(), false)
	defer closeServer()
	config := &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "example/repo"}}
	full, err := c.PreviewDocumentTree(context.Background(), config)
	require.NoError(t, err)
	require.False(t, full.Truncated)
	require.Empty(t, full.MissingPaths)
	require.Equal(t, strings.Repeat("a", 40), full.Commit)
	require.Len(t, full.Files, 4) // source code and hidden paths are not document candidates
	require.Zero(t, blobs.Load())

	config.Settings["paths"] = []string{"docs", "README.md"}
	selected, err := c.PreviewDocumentTree(context.Background(), config)
	require.NoError(t, err)
	require.Len(t, selected.Files, 3)
	require.Equal(t, []string{"README.md", "docs"}, selected.Paths)
	require.Zero(t, blobs.Load())
}

func TestPreviewDocumentTreeMissingInvalidAndTruncated(t *testing.T) {
	c, blobs, closeServer := githubPreviewFixture(t, previewEntries(), false)
	defer closeServer()
	config := &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "example/repo", "paths": []string{"missing"}}}
	missing, err := c.PreviewDocumentTree(context.Background(), config)
	require.NoError(t, err)
	require.Equal(t, []string{"missing"}, missing.MissingPaths)
	require.Zero(t, blobs.Load())

	config.Settings["paths"] = []string{"../private"}
	_, err = c.PreviewDocumentTree(context.Background(), config)
	require.Error(t, err)
	require.Zero(t, blobs.Load())

	truncatedConnector, truncatedBlobs, closeTruncated := githubPreviewFixture(t, previewEntries(), true)
	defer closeTruncated()
	config.Settings["paths"] = []string{"not-yet-enumerated"}
	partial, err := truncatedConnector.PreviewDocumentTree(context.Background(), config)
	require.NoError(t, err)
	require.True(t, partial.Truncated)
	require.Empty(t, partial.MissingPaths, "a partial tree cannot prove that a selected path is missing")
	require.Zero(t, truncatedBlobs.Load())
}
