package github

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGitHubDocumentPlanChangedSHADeletionAndLegacyCursor(t *testing.T) {
	repo, cacheDir, cfg, firstCommit := documentGitFixture(t, map[string]string{
		"docs/guide.md": "v1\n", "docs/removed.md": "old\n", "src/app.go": "package app\n",
	})
	cfg.Settings["paths"] = []string{"docs"}
	c, _ := fastPathHeadConnector(t, firstCommit)
	first, err := c.PlanDocuments(context.Background(), cfg, nil, false, "")
	require.NoError(t, err)
	require.Len(t, first.Upserts, 2)
	require.Empty(t, first.Deletions)
	item, err := c.ReadPlannedDocument(context.Background(), first, first.Upserts[0])
	require.NoError(t, err)
	require.Equal(t, "github:test/docs:main:docs/guide.md", item.ExternalID)
	require.Contains(t, item.Metadata["github_url"], "/blob/"+firstCommit+"/")
	encoded, err := first.Cursor().ToJSON()
	require.NoError(t, err)
	var old types.SyncCursor
	require.NoError(t, json.Unmarshal(encoded, &old))
	require.Contains(t, old.ConnectorCursor, "selection")
	require.Contains(t, old.ConnectorCursor, "commit")
	require.Contains(t, old.ConnectorCursor, "files")
	unchanged, err := c.PlanDocuments(context.Background(), cfg, &old, false, "")
	require.NoError(t, err)
	require.True(t, unchanged.Complete)
	force, err := c.PlanDocuments(context.Background(), cfg, &old, true, "")
	require.NoError(t, err)
	require.Len(t, force.Upserts, 2, "manual full sync must revisit the selected documents")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "docs", "guide.md"), []byte("v2\n"), 0o600))
	require.NoError(t, os.Remove(filepath.Join(repo, "docs", "removed.md")))
	documentGitRun(t, repo, "add", "-A")
	documentGitRun(t, repo, "commit", "-qm", "second")
	secondCommit := documentGitRun(t, repo, "rev-parse", "HEAD")
	documentGitRun(t, "", "--git-dir="+cacheDir, "fetch", "--no-tags", "--", repo, "+"+secondCommit+":refs/weknora/test")
	c, _ = fastPathHeadConnector(t, secondCommit)
	next, err := c.PlanDocuments(context.Background(), cfg, &old, false, "")
	require.NoError(t, err)
	require.Len(t, next.Upserts, 1)
	require.Equal(t, "docs/guide.md", next.Upserts[0].Path)
	require.Len(t, next.Deletions, 1)
	require.Equal(t, "docs/removed.md", next.Deletions[0].Path)
	require.True(t, next.DeletedItem(next.Deletions[0]).IsDeleted)
	require.NotEqual(t, first.Digest, next.Digest)
	// A retry stays on the original fixed commit even if the branch moved.
	pinned, err := c.PlanDocuments(context.Background(), cfg, &old, false, firstCommit)
	require.NoError(t, err)
	require.Equal(t, firstCommit, pinned.Commit)
	require.Empty(t, pinned.Upserts)
	require.Empty(t, pinned.Deletions)
}

func TestGitHubDocumentPlanCanSpanMoreThanOneBoundedWorkerChunk(t *testing.T) {
	large := strings.Repeat("x", 14<<20)
	files := map[string]string{}
	for _, path := range []string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md", "docs/e.md"} {
		files[path] = large
	}
	_, _, cfg, commit := documentGitFixture(t, files)
	c, _ := fastPathHeadConnector(t, commit)
	plan, err := c.PlanDocuments(context.Background(), cfg, nil, false, "")
	require.NoError(t, err)
	require.Len(t, plan.Upserts, 5)
	var bytes int64
	for _, item := range plan.Upserts {
		bytes += item.Size
	}
	require.Greater(t, bytes, int64(64<<20))
}

func TestGitHubDocumentPlanSkipsSensitiveBlobWithoutLosingLegacyCursor(t *testing.T) {
	_, _, cfg, commit := documentGitFixture(t, map[string]string{
		"docs/guide.md": "safe\n", "secrets/notes.md": "do-not-ingest\n",
	})
	cfg.Settings["paths"] = []string{""}
	selection, err := DocumentSelection(cfg)
	require.NoError(t, err)
	legacy := &types.SyncCursor{ConnectorCursor: map[string]interface{}{
		"selection": selection, "commit": strings.Repeat("a", 40),
		"files": map[string]entry{"secrets/notes.md": {
			Path: "secrets/notes.md", SHA: strings.Repeat("b", 40), Size: 14, Type: "blob", Mode: "100644",
		}},
	}}
	c, requests := fastPathHeadConnector(t, commit)
	plan, err := c.PlanDocuments(context.Background(), cfg, legacy, false, "")
	require.NoError(t, err)
	require.Len(t, plan.Upserts, 1)
	require.Equal(t, "docs/guide.md", plan.Upserts[0].Path)
	require.Empty(t, plan.Deletions)
	require.Equal(t, 1, cfg.SkippedSensitive)
	encoded, err := plan.Cursor().ToJSON()
	require.NoError(t, err)
	require.Contains(t, string(encoded), "secrets/notes.md", "historical sensitive cursor lineage is retained")
	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests,
		"planning must never fetch a sensitive blob over REST")
	// Changing the selection to docs must not silently retire the old sensitive
	// canonical or drop its cursor lineage.
	cfg.Settings["paths"] = []string{"docs"}
	plan, err = c.PlanDocuments(context.Background(), cfg, legacy, false, "")
	require.NoError(t, err)
	require.Empty(t, plan.Deletions)
	encoded, err = plan.Cursor().ToJSON()
	require.NoError(t, err)
	require.Contains(t, string(encoded), "secrets/notes.md")
}

func TestGitHubDocumentCheckpointCacheLossKeepsOldCursor(t *testing.T) {
	_, cacheDir, cfg, commit := documentGitFixture(t, map[string]string{"docs/guide.md": "current\n"})
	c, _ := fastPathHeadConnector(t, commit)
	first, err := c.PlanDocuments(context.Background(), cfg, nil, false, "")
	require.NoError(t, err)
	old := first.Cursor()
	encoded, err := old.ToJSON()
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(cacheDir))
	t.Setenv("PATH", t.TempDir())
	_, err = c.PlanDocuments(context.Background(), cfg, old, true, "")
	require.ErrorContains(t, err, "requires Git")
	after, err := old.ToJSON()
	require.NoError(t, err)
	require.Equal(t, string(encoded), string(after), "cache failure cannot alter the previous complete cursor")
}

// Existing partial GitHub imports never acknowledged a next cursor when any
// file failed. Production's partial sources therefore enter this nil-cursor
// branch and must plan candidates even when the remote commit is unchanged.
func TestGitHubDocumentLegacyPartialWithNilCursorPlansCandidates(t *testing.T) {
	_, _, cfg, commit := documentGitFixture(t, map[string]string{"docs/guide.md": "needs-index\n"})
	c, _ := fastPathHeadConnector(t, commit)
	plan, err := c.PlanDocuments(context.Background(), cfg, nil, false, "")
	require.NoError(t, err)
	require.False(t, plan.Complete)
	require.Len(t, plan.Upserts, 1)
	require.Equal(t, "docs/guide.md", plan.Upserts[0].Path)
}

func TestGitHubDocumentPlanSharesExclusionFingerprintAndRetainsOldIndex(t *testing.T) {
	_, _, cfg, commit := documentGitFixture(t, map[string]string{
		"docs/keep.md": "keep\n", "docs/skip.md": "skip\n",
	})
	cfg.Settings["paths"] = []string{"docs"}
	oldSelection, err := DocumentSelection(cfg)
	require.NoError(t, err)
	c, _ := fastPathHeadConnector(t, commit)
	oldPlan, err := c.PlanDocuments(context.Background(), cfg, nil, false, "")
	require.NoError(t, err)
	require.Len(t, oldPlan.Upserts, 2)
	oldCursor := oldPlan.Cursor()
	cfg.Settings["exclude"] = []string{"docs/skip.md"}
	newSelection, err := DocumentSelection(cfg)
	require.NoError(t, err)
	require.NotEqual(t, oldSelection, newSelection)
	plan, err := c.PlanDocuments(context.Background(), cfg, oldCursor, false, "")
	require.NoError(t, err)
	require.Equal(t, newSelection, plan.Selection)
	require.Len(t, plan.Upserts, 1)
	require.Equal(t, "docs/keep.md", plan.Upserts[0].Path)
	require.Empty(t, plan.Deletions)
	require.Equal(t, 1, cfg.SkippedExcluded)
	encoded, err := plan.Cursor().ToJSON()
	require.NoError(t, err)
	require.Contains(t, string(encoded), "docs/skip.md", "excluded historical index requires explicit review, not silent retirement")
	cfg.Settings["exclude"] = []string{}
	legacyKey, err := DocumentSelection(cfg)
	require.NoError(t, err)
	require.Equal(t, oldSelection, legacyKey, "empty excludes must retain existing 60-source cursor identity")
	cfg.Settings["exclude"] = []string{"docs/["}
	_, err = DocumentSelection(cfg)
	require.Error(t, err, "invalid glob cannot silently remove documents")
}
