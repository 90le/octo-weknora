package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDocumentExclusionsKeepLegacyNilCursorAndInvalidateOnChange(t *testing.T) {
	base := &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "example/repo"}}
	s, err := parseSelection(base)
	require.NoError(t, err)
	legacyJSON, err := json.Marshal(s)
	require.NoError(t, err)
	legacyHash := sha256.Sum256(legacyJSON)
	legacyKey := hex.EncodeToString(legacyHash[:])
	for _, value := range []interface{}{nil, []string{}, []interface{}{}} {
		base.Settings["exclude"] = value
		excludes, err := documentExcludes(base)
		require.NoError(t, err)
		require.Empty(t, excludes)
		require.Equal(t, legacyKey, documentSelectionKey(s, excludes), "legacy nil/empty rules must not churn existing cursors")
	}
	base.Settings["exclude"] = []string{"images/*.png", "docs/drafts"}
	excludes, err := documentExcludes(base)
	require.NoError(t, err)
	changedKey := documentSelectionKey(s, excludes)
	require.NotEqual(t, legacyKey, changedKey)
	base.Settings["exclude"] = []string{"docs/drafts", "images/*.png"}
	reordered, err := documentExcludes(base)
	require.NoError(t, err)
	require.Equal(t, changedKey, documentSelectionKey(s, reordered), "rule order is not a scope change")
	trusted := &types.DataSourceConfig{SyncSource: &types.DataSourceSyncSource{TenantID: 7, KnowledgeBaseID: "kb", DataSourceID: "ds"}}
	prev := cursor{Selection: legacyKey, Commit: strings.Repeat("a", 40)}
	require.True(t, documentCursorReusable(trusted, prev, legacyKey, prev.Commit))
	require.False(t, documentCursorReusable(trusted, prev, changedKey, prev.Commit), "exclude change must invalidate same-commit fast path")
	base.Settings["exclude"] = []string{"bad\\rule"}
	_, err = documentExcludes(base)
	require.Error(t, err)
	base.Settings["exclude"] = []string{"docs/["}
	_, err = documentExcludes(base)
	var githubErr *Error
	require.ErrorAs(t, err, &githubErr)
	require.Equal(t, "github_exclusion_invalid", githubErr.Code)
	require.NotContains(t, githubErr.Error(), "docs/[", "invalid rule must not leak into a public error")
	base.Settings["exclude"] = []string{"**/assets/["}
	_, err = documentExcludes(base)
	require.ErrorAs(t, err, &githubErr)
	require.Equal(t, "github_exclusion_invalid", githubErr.Code)
	base.Settings["exclude"] = []string{"**/"}
	_, err = documentExcludes(base)
	require.EqualError(t, err, "invalid exclusion rule")
}

func TestInvalidDocumentExclusionRejectedBeforeRemoteValidationOrPreview(t *testing.T) {
	c, blobs, closeServer := githubPreviewFixture(t, nil, false)
	defer closeServer()
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{
		"repository": "example/repo", "mode": "documents", "exclude": []string{"docs/["},
	}}
	var githubErr *Error
	err := c.Validate(context.Background(), cfg)
	require.ErrorAs(t, err, &githubErr)
	require.Equal(t, "github_exclusion_invalid", githubErr.Code)
	_, err = c.PreviewDocumentTree(context.Background(), cfg)
	require.ErrorAs(t, err, &githubErr)
	require.Equal(t, "github_exclusion_invalid", githubErr.Code)
	require.Zero(t, blobs.Load())
}

func TestDocumentScopeAppliesDirectoryGlobImageAndMandatorySafety(t *testing.T) {
	s := selection{Repository: "example/repo"}
	rules := []string{"docs/drafts", "images/*.png"}
	file := func(name string) entry {
		return entry{Path: name, Type: "blob", Mode: "100644", SHA: strings.Repeat("c", 40), Size: 10}
	}
	for _, p := range []string{"README.md", "docs/live.md", "images/logo.jpg"} {
		require.True(t, documentInCurrentScope(file(p), s, rules), p)
	}
	for _, p := range []string{"docs/drafts/old.md", "images/logo.png", "secrets/notes.md"} {
		require.False(t, documentInCurrentScope(file(p), s, rules), p)
	}
	require.True(t, documentHistoricalOutOfScope("docs/drafts/old.md", s, rules))
	require.True(t, documentHistoricalOutOfScope("images/logo.png", s, rules))
	require.True(t, documentHistoricalOutOfScope("secrets/notes.md", s, rules))
}

func TestGitHubDocumentExcludePreservesOldIndexAndCursorOnRemoteRemoval(t *testing.T) {
	sha := strings.Repeat("c", 40)
	draft := entry{Path: "docs/drafts/old.md", Type: "blob", Mode: "100644", SHA: sha, Size: 100}
	image := entry{Path: "images/icons/logo.png", Type: "blob", Mode: "100644", SHA: sha, Size: 200}
	directory := entry{Path: "docs/drafts", Type: "tree", Mode: "040000", SHA: sha}
	entries := []entry{directory, draft, image}
	c, blobs, closeServer := githubPreviewFixture(t, entries, false)
	defer closeServer()
	config := &types.DataSourceConfig{Settings: map[string]interface{}{
		"repository": "example/repo", "paths": []string{"docs/drafts", "images/icons/logo.png"},
	}}
	s, err := parseSelection(config)
	require.NoError(t, err)
	old := &types.SyncCursor{ConnectorCursor: map[string]interface{}{
		"selection": documentSelectionKey(s, nil), "commit": strings.Repeat("d", 40),
		"files": map[string]entry{draft.Path: draft, image.Path: image},
	}}
	config.Settings["exclude"] = []string{"docs/drafts", "**/*.png"}
	items, next, err := c.FetchIncremental(context.Background(), config, old)
	require.NoError(t, err)
	require.Empty(t, items, "excluded old files must not be fetched or deleted")
	require.Equal(t, 2, config.SkippedExcluded)
	require.Zero(t, blobs.Load())
	var parsed cursor
	b, err := json.Marshal(next.ConnectorCursor)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &parsed))
	require.Equal(t, documentSelectionKey(s, []string{"**/*.png", "docs/drafts"}), parsed.Selection)
	require.Contains(t, parsed.Files, draft.Path)
	require.Contains(t, parsed.Files, image.Path)

	// The remote draft disappears while still excluded. The new cursor has the
	// new selection key, yet must not turn that historical indexed row into a
	// deletion; explicit review and cleanup remain separate operations.
	removedConnector, removedBlobs, closeRemoved := githubPreviewFixture(t, []entry{directory, image}, false)
	defer closeRemoved()
	items, afterRemoval, err := removedConnector.FetchIncremental(context.Background(), config, next)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Equal(t, 1, config.SkippedExcluded)
	require.Zero(t, removedBlobs.Load())
	b, err = json.Marshal(afterRemoval.ConnectorCursor)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &parsed))
	require.Contains(t, parsed.Files, draft.Path)
}

func TestRecursiveDocumentRuleAgreesWithPreviewAndSync(t *testing.T) {
	sha := strings.Repeat("c", 40)
	entries := []entry{
		{Path: "root.png", Type: "blob", Mode: "100644", SHA: sha, Size: 10},
		{Path: "docs/assets/icons/nested.png", Type: "blob", Mode: "100644", SHA: sha, Size: 20},
	}
	c, blobs, closeServer := githubPreviewFixture(t, entries, false)
	defer closeServer()
	rule := "**/*.png"
	config := &types.DataSourceConfig{Settings: map[string]interface{}{
		"repository": "example/repo", "exclude": []string{rule},
	}}
	preview, err := c.PreviewDocumentTree(context.Background(), config)
	require.NoError(t, err)
	require.Len(t, preview.Files, 2)
	for _, file := range preview.Files {
		require.True(t, snapshot.Excluded(file.Path, []string{rule}), file.Path)
	}
	items, _, err := c.FetchIncremental(context.Background(), config, nil)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Equal(t, 2, config.SkippedExcluded)
	require.Zero(t, blobs.Load(), "neither preview nor sync should read excluded blobs")
}
