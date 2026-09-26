package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		{Path: "secrets/notes.md", Type: "blob", Mode: "100644", SHA: sha, Size: 500},
		{Path: ".env", Type: "blob", Mode: "100644", SHA: sha, Size: 50},
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
	require.Len(t, full.Files, 5) // source code and dotfiles are not document candidates
	sensitive := 0
	for _, file := range full.Files {
		if file.Sensitive {
			sensitive++
			require.Equal(t, "secrets/notes.md", file.Path)
		}
	}
	require.Equal(t, 1, sensitive)
	require.Zero(t, blobs.Load())

	config.Settings["paths"] = []string{"docs", "README.md"}
	selected, err := c.PreviewDocumentTree(context.Background(), config)
	require.NoError(t, err)
	require.Len(t, selected.Files, 3)
	require.Equal(t, []string{"README.md", "docs"}, selected.Paths)
	require.Zero(t, blobs.Load())
}

func TestPreviewSourceTreeIncludesCodeWithoutReadingBlobs(t *testing.T) {
	c, blobs, closeServer := githubPreviewFixture(t, previewEntries(), false)
	defer closeServer()
	config := &types.DataSourceConfig{Settings: map[string]interface{}{
		"repository": "example/repo", "ref": "main", "paths": []string{"src", "README.md"}, "mode": "source",
	}}
	tree, err := c.PreviewSourceTree(context.Background(), config)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("a", 40), tree.Commit)
	require.Empty(t, tree.MissingPaths)
	require.Len(t, tree.Files, 2)
	require.Equal(t, "README.md", tree.Files[0].Path)
	require.Equal(t, "src/app.js", tree.Files[1].Path)
	require.Zero(t, blobs.Load())
	config.Settings["paths"] = []string{"missing"}
	missing, err := c.PreviewSourceTree(context.Background(), config)
	require.NoError(t, err)
	require.Equal(t, []string{"missing"}, missing.MissingPaths)
}

func TestFetchIncrementalSkipsSensitiveBeforeBlobReadAndPreservesOldCursor(t *testing.T) {
	entries := previewEntries()
	c, blobs, closeServer := githubPreviewFixture(t, entries, false)
	defer closeServer()
	config := &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "example/repo"}}
	selection, err := parseSelection(config)
	require.NoError(t, err)
	encoded, err := json.Marshal(selection)
	require.NoError(t, err)
	hash := sha256.Sum256(encoded)
	key := hex.EncodeToString(hash[:])
	oldFiles := map[string]entry{"secrets/notes.md": entries[6]}
	for _, e := range entries {
		if allowedGitHubDocument(e) {
			oldFiles[e.Path] = e
		}
	}
	old := &types.SyncCursor{ConnectorCursor: map[string]interface{}{
		"selection": key, "commit": strings.Repeat("d", 40), "files": oldFiles,
	}}
	items, next, err := c.FetchIncremental(context.Background(), config, old)
	require.NoError(t, err)
	require.Empty(t, items, "neither unchanged README nor sensitive file may be read or deleted")
	require.Equal(t, 1, config.SkippedSensitive)
	require.Zero(t, blobs.Load())
	var parsed cursor
	encoded, err = json.Marshal(next.ConnectorCursor)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &parsed))
	require.Contains(t, parsed.Files, "secrets/notes.md", "old sensitive row must retain cursor lineage for later review")

	// Even if the remote file disappears, this safety-policy run cannot silently
	// delete an older indexed document; that requires a separate audited action.
	withoutSensitive := append([]entry(nil), entries[:6]...)
	withoutSensitive = append(withoutSensitive, entries[7:]...)
	removedConnector, removedBlobs, closeRemoved := githubPreviewFixture(t, withoutSensitive, false)
	defer closeRemoved()
	items, next, err = removedConnector.FetchIncremental(context.Background(), config, old)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Zero(t, config.SkippedSensitive)
	require.Zero(t, removedBlobs.Load())
	encoded, err = json.Marshal(next.ConnectorCursor)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &parsed))
	require.Contains(t, parsed.Files, "secrets/notes.md")

	// Changing the selected scope is not permission to forget or purge an old
	// sensitive row. The canonical row remains for a separate audited removal.
	config.Settings["paths"] = []string{"secrets/notes.md"}
	items, next, err = c.FetchIncremental(context.Background(), config, old)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Equal(t, 1, config.SkippedSensitive)
	require.Zero(t, blobs.Load())
	encoded, err = json.Marshal(next.ConnectorCursor)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &parsed))
	require.Contains(t, parsed.Files, "secrets/notes.md")
}

func TestFetchIncrementalUnchangedTrustedCursorDoesNotReadSensitiveTreeOrBlob(t *testing.T) {
	commit, treeSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/example/repo":
			_ = json.NewEncoder(w).Encode(map[string]string{"full_name": "example/repo", "default_branch": "main"})
		case "/repos/example/repo/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": commit, "commit": map[string]any{"tree": map[string]string{"sha": treeSHA}}})
		default:
			http.Error(w, "unexpected tree/blob read", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	c := NewConnectorWithHTTPClient(server.Client(), server.URL)
	config := &types.DataSourceConfig{
		Settings:   map[string]interface{}{"repository": "example/repo"},
		SyncSource: &types.DataSourceSyncSource{TenantID: 7, KnowledgeBaseID: "kb", DataSourceID: "ds"},
	}
	selection, err := parseSelection(config)
	require.NoError(t, err)
	encoded, err := json.Marshal(selection)
	require.NoError(t, err)
	hash := sha256.Sum256(encoded)
	old := &types.SyncCursor{ConnectorCursor: map[string]interface{}{
		"selection": hex.EncodeToString(hash[:]), "commit": commit,
		"files": map[string]entry{"secrets/notes.md": {Path: "secrets/notes.md", Type: "blob", Mode: "100644", SHA: strings.Repeat("c", 40), Size: 100}},
	}}
	items, next, err := c.FetchIncremental(context.Background(), config, old)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Same(t, old, next)
	require.Equal(t, int32(2), calls.Load(), "unchanged trusted commit only needs repo and commit metadata")
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
