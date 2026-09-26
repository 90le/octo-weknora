package github

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func localGitURL(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func sharedGitFixture(t *testing.T) (string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "upstream")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o700))
	documentGitRun(t, repo, "init", "-q")
	documentGitRun(t, repo, "config", "user.email", "test@example.invalid")
	documentGitRun(t, repo, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("readme\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "docs", "guide.md"), []byte("guide\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "app.go"), []byte("package main\n"), 0o600))
	documentGitRun(t, repo, "add", ".")
	documentGitRun(t, repo, "commit", "-qm", "one")
	return repo, documentGitRun(t, repo, "rev-parse", "HEAD")
}

func sharedGitConfig(t *testing.T, sourceID, mode, credential string, tenant uint64) *types.DataSourceConfig {
	t.Helper()
	cfg := &types.DataSourceConfig{
		Settings:    map[string]interface{}{"repository": "test/docs", "ref": "main", "mode": mode},
		Credentials: map[string]interface{}{"access_token": credential},
		SyncSource: &types.DataSourceSyncSource{TenantID: tenant, KnowledgeBaseID: "kb", DataSourceID: sourceID,
			CheckAccess: func(context.Context) error { return nil }},
	}
	scope, err := CredentialScope(cfg)
	require.NoError(t, err)
	cfg.SyncSource.CredentialScope = scope
	return cfg
}

func TestSharedGitTwinSourceAndDocumentFetchOnce(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("DATASOURCE_GITHUB_SHARED_GIT_CACHE", "1")
	root := t.TempDir()
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", root)
	repo, commit := sharedGitFixture(t)
	c, _ := fastPathHeadConnector(t, commit)
	c.testGitRemote = localGitURL(repo)
	var network atomic.Int32
	c.testNetworkGitObserved = func() { network.Add(1) }
	store := &snapshot.Store{Base: root}
	source := sharedGitConfig(t, "source", "source", "same-token", 7)
	builder, err := store.Begin(&types.DataSource{ID: "source", TenantID: 7, KnowledgeBaseID: "kb"}, source)
	require.NoError(t, err)
	require.NoError(t, c.buildGitSnapshot(context.Background(), source, builder, nil))
	snapshot, err := builder.Finish()
	require.NoError(t, err)
	require.Equal(t, commit, snapshot.Revision)
	require.Len(t, snapshot.Files, 3)

	document := sharedGitConfig(t, "document", "documents", "same-token", 7)
	plan, err := c.PlanDocuments(context.Background(), document, nil, false, "")
	require.NoError(t, err)
	require.Len(t, plan.Upserts, 2)
	item, err := c.ReadPlannedDocument(context.Background(), plan, plan.Upserts[0])
	require.NoError(t, err)
	require.Equal(t, "readme\n", string(item.Content))
	require.EqualValues(t, 1, network.Load(), "two projections of one commit must use one Git transfer")
	identity, err := sharedGitIdentityFor(7, "test/docs", source.SyncSource.CredentialScope)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(identity.dir, "HEAD"))
}

func TestSharedGitScopesAndConcurrentAcquisition(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", t.TempDir())
	repo, commit := sharedGitFixture(t)
	c := NewConnector()
	c.testGitRemote = localGitURL(repo)
	var network atomic.Int32
	c.testNetworkGitObserved = func() { network.Add(1) }
	first := sharedGitConfig(t, "a", "documents", "token-a", 7)
	second := sharedGitConfig(t, "b", "source", "token-a", 7)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, cfg := range []*types.DataSourceConfig{first, second} {
		wg.Add(1)
		go func(cfg *types.DataSourceConfig) {
			defer wg.Done()
			cache, err := c.sharedGitCache(context.Background(), cfg, selection{Repository: "test/docs"}, commit)
			if err == nil {
				_, err = cache.tree(context.Background(), commit)
			}
			errs <- err
		}(cfg)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, network.Load())

	otherToken := sharedGitConfig(t, "c", "documents", "token-b", 7)
	_, err := c.sharedGitCache(context.Background(), otherToken, selection{Repository: "test/docs"}, commit)
	require.NoError(t, err)
	otherTenant := sharedGitConfig(t, "d", "documents", "token-a", 8)
	_, err = c.sharedGitCache(context.Background(), otherTenant, selection{Repository: "test/docs"}, commit)
	require.NoError(t, err)
	require.EqualValues(t, 3, network.Load(), "different tokens and tenants must each fetch their own mirror")
	firstID, err := sharedGitIdentityFor(7, "test/docs", first.SyncSource.CredentialScope)
	require.NoError(t, err)
	otherID, err := sharedGitIdentityFor(7, "test/docs", otherToken.SyncSource.CredentialScope)
	require.NoError(t, err)
	require.NotEqual(t, firstID.dir, otherID.dir)
	require.NotContains(t, firstID.dir, "token-a")
	stale := sharedGitConfig(t, "stale", "documents", "token-a", 7)
	stale.Credentials["access_token"] = "token-b"
	_, err = c.sharedGitCache(context.Background(), stale, selection{Repository: "test/docs"}, commit)
	require.ErrorContains(t, err, "credential identity changed")
	require.EqualValues(t, 3, network.Load(), "stale credential identity must fail before Git access")
	stale.Credentials["access_token"] = "token-a"
	stale.SyncSource.CheckAccess = func(context.Context) error { return errors.New("source was rotated") }
	_, err = c.sharedGitCache(context.Background(), stale, selection{Repository: "test/docs"}, commit)
	require.ErrorContains(t, err, "source was rotated")
	require.EqualValues(t, 3, network.Load())
}

func TestSharedGitCleanupRetainsTwinAndRejectsCorruptCache(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", t.TempDir())
	repo, commit := sharedGitFixture(t)
	c := NewConnector()
	c.testGitRemote = localGitURL(repo)
	cfg := sharedGitConfig(t, "a", "documents", "same-token", 7)
	cache, err := c.sharedGitCache(context.Background(), cfg, selection{Repository: "test/docs"}, commit)
	require.NoError(t, err)
	identity, err := sharedGitIdentityFor(7, "test/docs", cfg.SyncSource.CredentialScope)
	require.NoError(t, err)
	// One source rotates its credential while the other still subscribes.
	require.NoError(t, CleanupSharedGitCache(context.Background(), 7, "TEST/DOCS", cfg.SyncSource.CredentialScope,
		func(context.Context) (bool, error) { return true, nil }))
	require.FileExists(t, filepath.Join(identity.dir, "HEAD"))
	_, err = cache.tree(context.Background(), commit)
	require.NoError(t, err)
	// An identity mismatch is an error, never an invitation to reuse or delete.
	require.NoError(t, os.WriteFile(filepath.Join(identity.dir, sharedGitCacheMarker), []byte("wrong"), 0o600))
	_, err = cache.tree(context.Background(), commit)
	require.ErrorContains(t, err, "identity")
	err = CleanupSharedGitCache(context.Background(), 7, "test/docs", cfg.SyncSource.CredentialScope,
		func(context.Context) (bool, error) { return false, nil })
	require.Error(t, err)
	require.DirExists(t, identity.dir)
	require.NoError(t, os.WriteFile(filepath.Join(identity.dir, sharedGitCacheMarker), []byte(identity.marker), 0o600))
	require.NoError(t, CleanupSharedGitCache(context.Background(), 7, "test/docs", cfg.SyncSource.CredentialScope,
		func(context.Context) (bool, error) { return false, nil }))
	_, err = os.Stat(identity.dir)
	require.True(t, os.IsNotExist(err))
	require.FileExists(t, identity.lockPath, "lock inode remains stable after mirror removal")
	// A stale plan must fail rather than reading another source's objects.
	_, err = cache.tree(context.Background(), commit)
	require.Error(t, err)
	require.NotContains(t, err.Error(), strings.TrimSpace(cfg.Credentials["access_token"].(string)))
}

func TestSharedGitCapPreservesPinnedObjectsAndOrphanStage(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("DATASOURCE_GITHUB_SHARED_GIT_CACHE", "1")
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", t.TempDir())
	t.Setenv("DATASOURCE_GITHUB_GIT_CACHE_MAX_BYTES", "67108864")
	repo, commit := sharedGitFixture(t)
	c, _ := fastPathHeadConnector(t, commit)
	c.testGitRemote = localGitURL(repo)
	var network atomic.Int32
	c.testNetworkGitObserved = func() { network.Add(1) }
	cfg := sharedGitConfig(t, "a", "documents", "same-token", 7)
	cache, err := c.sharedGitCache(context.Background(), cfg, selection{Repository: "test/docs"}, commit)
	require.NoError(t, err)
	plan, err := c.PlanDocuments(context.Background(), cfg, nil, false, "")
	require.NoError(t, err)
	require.NotEmpty(t, plan.Upserts)
	identity, err := sharedGitIdentityFor(7, "test/docs", cfg.SyncSource.CredentialScope)
	require.NoError(t, err)
	abandoned := identity.dir + ".stage-" + uuid.NewString()
	require.NoError(t, os.MkdirAll(abandoned, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(abandoned, "partial-pack"), []byte("interrupted clone"), 0o600))
	_, err = c.sharedGitCache(context.Background(), cfg, selection{Repository: "test/docs"}, commit)
	require.NoError(t, err)
	require.DirExists(t, abandoned, "a Git child can outlive its parent; an old stage is not automatically safe to delete")
	require.EqualValues(t, 1, network.Load())
	// Simulate retained packs crossing the per-mirror cap. Stop new syncs
	// without evicting objects still needed by an active fixed-commit plan.
	padding := filepath.Join(identity.dir, "old-unreachable-pack")
	require.NoError(t, os.WriteFile(padding, nil, 0o600))
	require.NoError(t, os.Truncate(padding, 65<<20))
	_, err = c.sharedGitCache(context.Background(), cfg, selection{Repository: "test/docs"}, commit)
	require.ErrorContains(t, err, "exceeds its limit")
	require.EqualValues(t, 1, network.Load())
	require.FileExists(t, padding)
	_, err = cache.tree(context.Background(), commit)
	require.NoError(t, err)
	item, err := c.ReadPlannedDocument(context.Background(), plan, plan.Upserts[0])
	require.NoError(t, err, "an older fixed-commit plan must remain readable after capacity failure")
	require.NotEmpty(t, item.Content)
	// Structural corruption also fails closed rather than silently replacing
	// the mirror while a different source holds a fixed-commit plan.
	require.NoError(t, os.Remove(padding))
	require.NoError(t, os.Remove(filepath.Join(identity.dir, "HEAD")))
	_, err = c.sharedGitCache(context.Background(), cfg, selection{Repository: "test/docs"}, commit)
	require.Error(t, err)
	require.EqualValues(t, 1, network.Load())
}
