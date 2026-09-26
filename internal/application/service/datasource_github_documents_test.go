package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGitHubDocumentCredentialScopeNeverStoresToken(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "")
	public := &types.DataSource{Config: types.JSON(`{"type":"github","settings":{"repository":"test/docs"}}`)}
	publicConfig := &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "test/docs"}}
	scope, err := githubDocumentCredentialScope(public, publicConfig)
	require.NoError(t, err)
	require.Equal(t, "public", scope)

	private := &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "secret-keep-private"}}
	_, err = githubDocumentCredentialScope(public, private)
	require.ErrorContains(t, err, "SYSTEM_AES_KEY")
	require.NotContains(t, err.Error(), "secret-keep-private")
	t.Setenv("SYSTEM_AES_KEY", "0123456789abcdef0123456789abcdef")
	scope, err = githubDocumentCredentialScope(public, private)
	require.NoError(t, err)
	require.Len(t, scope, 64)
	require.NotContains(t, scope, "secret-keep-private")
	other, err := githubDocumentCredentialScope(public, &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "other-token"}})
	require.NoError(t, err)
	require.NotEqual(t, scope, other)

	// A rotated encryption key makes ParseConfig blank an unreadable encrypted
	// token. Never silently reinterpret such a source as public.
	privateSource := &types.DataSource{Config: types.JSON(`{"credentials":{"access_token":"enc:v1:opaque"}}`)}
	_, err = githubDocumentCredentialScope(privateSource, publicConfig)
	require.ErrorContains(t, err, "cannot be decrypted")
}

func TestGitHubDocumentProgressDistinguishesReadyAndFailedDeletion(t *testing.T) {
	items := []types.GitHubDocumentSyncItem{
		{Path: "guide.md", Operation: types.GitHubDocumentItemUpsert, Status: types.GitHubDocumentItemReady, Outcome: types.GitHubDocumentOutcomeCreated},
		{Path: "removed.md", Operation: types.GitHubDocumentItemDelete, Status: types.GitHubDocumentItemFailed, ErrorCode: "deletion_failed"},
		{Path: "pending.md", Operation: types.GitHubDocumentItemUpsert, Status: types.GitHubDocumentItemPending},
	}
	result := githubDocumentProgressResult(items)
	require.Equal(t, 3, result.Total)
	require.Equal(t, 1, result.Created)
	require.Equal(t, 1, result.Failed)
	require.Equal(t, 1, result.DeletionFailed)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "deletion_failed", result.Errors[0].Code)
}
