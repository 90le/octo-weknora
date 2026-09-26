package github

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func documentGitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

func documentGitFixture(t *testing.T, files map[string]string) (string, string, *types.DataSourceConfig, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	documentGitRun(t, repo, "init", "-q")
	documentGitRun(t, repo, "config", "user.email", "test@example.invalid")
	documentGitRun(t, repo, "config", "user.name", "Test")
	for name, body := range files {
		p := filepath.Join(repo, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	}
	documentGitRun(t, repo, "add", ".")
	documentGitRun(t, repo, "commit", "-qm", "first")
	commit := documentGitRun(t, repo, "rev-parse", "HEAD")
	root := t.TempDir()
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", root)
	cfg := &types.DataSourceConfig{
		Settings:   map[string]interface{}{"repository": "test/docs", "ref": "main", "mode": "documents"},
		SyncSource: &types.DataSourceSyncSource{TenantID: 7, KnowledgeBaseID: "kb", DataSourceID: "ds"},
	}
	store := &snapshot.Store{Base: root}
	ds := &types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb"}
	cacheRoot, err := store.PrivateDirectory(ds, "git")
	require.NoError(t, err)
	cacheDir := filepath.Join(cacheRoot, repositoryCacheKey("test/docs"))
	documentGitRun(t, "", "clone", "--bare", "--", repo, cacheDir)
	documentGitRun(t, "", "--git-dir="+cacheDir, "remote", "set-url", "origin", "https://github.com/test/docs.git")
	return repo, cacheDir, cfg, commit
}

func TestGitDocumentsFixedCommitAndUnchangedFastPath(t *testing.T) {
	_, cacheDir, cfg, commit := documentGitFixture(t, map[string]string{
		"docs/guide.md": "guide v1\n", "README.md": "readme\n", "src/app.go": "package app\n",
	})
	c, requests := fastPathHeadConnector(t, commit)
	items, first, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "README.md", items[0].Title)
	require.Equal(t, "readme\n", string(items[0].Content))
	require.Equal(t, "docs/guide.md", items[1].Title)
	require.Equal(t, "guide v1\n", string(items[1].Content))
	require.Equal(t, "github:test/docs:main:docs/guide.md", items[1].ExternalID)
	require.Equal(t, "test/docs/docs/guide.md", items[1].FileName)
	require.Equal(t, "https://github.com/test/docs/blob/"+commit+"/docs/guide.md", items[1].Metadata["github_url"])
	require.Equal(t, commit, items[1].Metadata["github_commit"])
	require.Equal(t, "github", items[1].Metadata["source_type"])
	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests,
		"document Git sync must never issue REST tree or blob requests")
	require.Equal(t, commit, first.ConnectorCursor["commit"])
	require.NotNil(t, first.ConnectorCursor["files"], "keep the legacy cursor schema")

	// A healthy acknowledged cursor should not touch the tree or cache when
	// the remote commit is unchanged. Remove the cache to prove this path.
	require.NoError(t, os.RemoveAll(cacheDir))
	items, next, err := c.FetchIncremental(context.Background(), cfg, first)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Same(t, first, next)
	require.Len(t, *requests, 4)
}

func TestGitDocumentsOnlyChangedBlobAndOldCursor(t *testing.T) {
	repo, cacheDir, cfg, firstCommit := documentGitFixture(t, map[string]string{
		"docs/guide.md": "guide v1\n", "docs/other.md": "other\n", "src/app.py": "pass\n",
	})
	c, _ := fastPathHeadConnector(t, firstCommit)
	_, old, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "docs", "guide.md"), []byte("guide v2\n"), 0o600))
	documentGitRun(t, repo, "add", "docs/guide.md")
	documentGitRun(t, repo, "commit", "-qm", "second")
	secondCommit := documentGitRun(t, repo, "rev-parse", "HEAD")
	documentGitRun(t, "", "--git-dir="+cacheDir, "fetch", "--no-tags", "--", repo, "+"+secondCommit+":refs/weknora/test")
	c, requests := fastPathHeadConnector(t, secondCommit)
	items, next, err := c.FetchIncremental(context.Background(), cfg, old)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "docs/guide.md", items[0].Title)
	require.Equal(t, "guide v2\n", string(items[0].Content))
	require.Equal(t, "https://github.com/test/docs/blob/"+secondCommit+"/docs/guide.md", items[0].Metadata["github_url"])
	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests)
	require.Equal(t, secondCommit, next.ConnectorCursor["commit"])
	require.Len(t, next.ConnectorCursor["files"], 2)
}

func TestGitDocumentsSelectedFileAndDirectoryPreserveScope(t *testing.T) {
	_, _, cfg, commit := documentGitFixture(t, map[string]string{
		"README.md": "root\n", "docs/guide.md": "guide\n", "docs/other.md": "other\n", "src/app.go": "package app\n",
	})
	cfg.Settings["paths"] = []string{"README.md"}
	c, requests := fastPathHeadConnector(t, commit)
	items, old, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "README.md", items[0].Title)
	// Changing selection is not a remote deletion, even at the same commit.
	cfg.Settings["paths"] = []string{"docs"}
	items, _, err = c.FetchIncremental(context.Background(), cfg, old)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "docs/guide.md", items[0].Title)
	require.Equal(t, "docs/other.md", items[1].Title)
	for _, item := range items {
		require.False(t, item.IsDeleted)
	}
	require.Len(t, *requests, 4)
	// An explicit empty path in legacy settings means the whole repository.
	// Passing it to git ls-tree as an empty pathspec would return nothing and
	// could falsely look like every previously indexed document was deleted.
	cfg.Settings["paths"] = []string{""}
	items, _, err = c.FetchIncremental(context.Background(), cfg, old)
	require.NoError(t, err)
	require.Len(t, items, 3)
	for _, item := range items {
		require.False(t, item.IsDeleted)
	}
}

func TestGitDocumentsLegacyCursorAndLostCacheRemainSafe(t *testing.T) {
	_, cacheDir, cfg, commit := documentGitFixture(t, map[string]string{"README.md": "hello\n"})
	c, _ := fastPathHeadConnector(t, commit)
	_, old, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.NoError(t, err)
	// Round-trip through the persisted JSON form used by existing REST cursors.
	cursorJSON, err := old.ToJSON()
	require.NoError(t, err)
	var persisted types.SyncCursor
	require.NoError(t, json.Unmarshal(cursorJSON, &persisted))
	items, next, err := c.FetchIncremental(context.Background(), cfg, &persisted)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Same(t, &persisted, next)
	// A future changed commit with a lost cache must fail clearly; the previous
	// indexed content and acknowledged cursor do not imply remote deletion.
	require.NoError(t, os.RemoveAll(cacheDir))
	t.Setenv("PATH", t.TempDir())
	c, requests := fastPathHeadConnector(t, strings.Repeat("f", 40))
	items, next, err = c.FetchIncremental(context.Background(), cfg, &persisted)
	require.Error(t, err)
	require.Nil(t, items)
	require.Nil(t, next)
	var apiErr *Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "github_git_cache_unavailable", apiErr.Code)
	require.Equal(t, commit, persisted.ConnectorCursor["commit"])
	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests)
}

func TestGitDocumentsCacheFailureIsClosedAndRedacted(t *testing.T) {
	_, cacheDir, cfg, commit := documentGitFixture(t, map[string]string{"README.md": "hello\n"})
	cfg.Credentials = map[string]interface{}{"access_token": "secret-token-must-not-appear"}
	c, requests := fastPathHeadConnector(t, commit)
	// An explicit trusted identity with unavailable persistent storage must not
	// downgrade to REST blob fetching or write a cache under the system temp dir.
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", "")
	items, next, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.Error(t, err)
	require.Nil(t, items)
	require.Nil(t, next)
	require.ErrorContains(t, err, "private persistent cache")
	require.NotContains(t, err.Error(), "secret-token-must-not-appear")
	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests)
	require.DirExists(t, cacheDir, "an unavailable mount must not erase prior private cache")
}

func TestGitDocumentsBadRefAndMissingGitObjectPreserveCursor(t *testing.T) {
	_, cacheDir, cfg, commit := documentGitFixture(t, map[string]string{"README.md": "hello\n"})
	c, _ := fastPathHeadConnector(t, commit)
	_, old, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.NoError(t, err)
	// A bad or inaccessible new revision never acknowledges a new manifest.
	badCommit := strings.Repeat("f", 40)
	c, _ = fastPathHeadConnector(t, "not-a-git-commit")
	items, next, err := c.FetchIncremental(context.Background(), cfg, old)
	require.Error(t, err)
	require.Nil(t, items)
	require.Nil(t, next)
	require.Equal(t, commit, old.ConnectorCursor["commit"])
	// A corrupt/missing object cannot turn into a false deletion.
	cache := &gitCache{dir: cacheDir, remote: "https://github.com/test/docs.git"}
	err = cache.readBlobs(context.Background(), []gitTreeEntry{{Path: "README.md", SHA: badCommit, Size: 6}}, maxFileBytes,
		func(gitTreeEntry, []byte) error { t.Fatal("missing object must never emit a document"); return nil })
	require.Error(t, err)
	require.NotContains(t, err.Error(), "README.md")
}

func TestGitDocumentsLargeRepositoryFailsBeforeBlobReads(t *testing.T) {
	files := make(map[string]string, 2001)
	for i := 0; i < 2001; i++ {
		files["docs/page-"+strconv.Itoa(i)+".md"] = "text\n"
	}
	_, _, cfg, commit := documentGitFixture(t, files)
	c, requests := fastPathHeadConnector(t, commit)
	items, next, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.Error(t, err)
	require.Nil(t, items)
	require.Nil(t, next)
	var apiErr *Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "github_documents_limit", apiErr.Code)
	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests)
}

func TestGitDocumentsBatchLimitRemainsExplicit(t *testing.T) {
	large := strings.Repeat("x", 14<<20)
	files := make(map[string]string, 5)
	for i := 0; i < 5; i++ {
		files["docs/part-"+strconv.Itoa(i)+".md"] = large
	}
	_, _, cfg, commit := documentGitFixture(t, files)
	c, requests := fastPathHeadConnector(t, commit)
	items, next, err := c.FetchIncremental(context.Background(), cfg, nil)
	require.Error(t, err)
	require.Nil(t, items)
	require.Nil(t, next)
	var apiErr *Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "github_documents_batch_limit", apiErr.Code)
	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests)
}

func TestGitDocumentBlobEOFDoesNotEmit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git subprocess is a POSIX test fixture")
	}
	dir := t.TempDir()
	sha := strings.Repeat("a", 40)
	gitPath := filepath.Join(dir, "git")
	require.NoError(t, os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf '%s blob 5\\nxy' '"+sha+"'\n"), 0o700))
	t.Setenv("PATH", dir)
	cache := &gitCache{dir: dir}
	err := cache.readBlobs(context.Background(), []gitTreeEntry{{Path: "README.md", SHA: sha, Size: 5}}, maxFileBytes,
		func(gitTreeEntry, []byte) error { t.Fatal("truncated Git output must never emit"); return nil })
	require.Error(t, err)
	require.ErrorContains(t, err, "previous snapshot remains available")
}
