package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type sharedCacheSubscriberRepo struct {
	interfaces.DataSourceRepository
	sources []*types.DataSource
}

func (r *sharedCacheSubscriberRepo) FindGitHubByTenant(_ context.Context, tenant uint64) ([]*types.DataSource, error) {
	result := make([]*types.DataSource, 0, len(r.sources))
	for _, source := range r.sources {
		if source.TenantID == tenant {
			result = append(result, source)
		}
	}
	return result, nil
}

func sharedCacheSource(t *testing.T, id, kb, token string) *types.DataSource {
	t.Helper()
	cfg := &types.DataSourceConfig{Type: types.ConnectorTypeGitHub,
		Settings:    map[string]interface{}{"repository": "test/docs", "mode": "documents"},
		Credentials: map[string]interface{}{"access_token": token}}
	blob, err := cfg.ToJSON()
	require.NoError(t, err)
	return &types.DataSource{ID: id, TenantID: 7, KnowledgeBaseID: kb, Type: types.ConnectorTypeGitHub, Config: blob}
}

func TestSharedGitCleanupKeepsOtherKnowledgeBaseSubscriber(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	t.Setenv("SYSTEM_AES_KEY", key)
	t.Setenv("DATASOURCE_GITHUB_SHARED_GIT_CACHE", "1")
	root := t.TempDir()
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", root)
	old := sharedCacheSource(t, "source-a", "kb-a", "same-token")
	sibling := sharedCacheSource(t, "source-b", "kb-b", "same-token")
	rotated := sharedCacheSource(t, "source-a", "kb-a", "new-token")
	parsed, err := old.ParseConfig()
	require.NoError(t, err)
	scope, err := githubDocumentCredentialScope(old, parsed)
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = fmt.Fprintf(mac, "github-shared-git-v1\x00%d\x00github.com\x00%s\x00%s", 7, "test/docs", scope)
	id := hex.EncodeToString(mac.Sum(nil))
	dir := filepath.Join(root, "github-shared-git-v1", id)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "weknora-cache-identity"), []byte("v1:"+id), 0o600))
	repo := &sharedCacheSubscriberRepo{sources: []*types.DataSource{rotated, sibling}}
	require.NoError(t, cleanupGitHubSharedCache(context.Background(), repo, old))
	require.DirExists(t, dir, "rotating one source must retain the twin source's mirror")
	repo.sources = []*types.DataSource{rotated}
	require.NoError(t, cleanupGitHubSharedCache(context.Background(), repo, old))
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err), "the old mirror is removed after its final subscriber leaves")
}
