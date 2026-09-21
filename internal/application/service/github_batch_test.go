package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestGitHubBatchExistingPairsAreModeScoped(t *testing.T) {
	makeSource := func(id, repository, mode string) *types.DataSource {
		blob, err := (&types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: map[string]interface{}{"repository": repository, "mode": mode}}).ToJSON()
		require.NoError(t, err)
		return &types.DataSource{ID: id, Type: types.ConnectorTypeGitHub, Config: blob}
	}
	pairs := githubDataSourcePairs([]*types.DataSource{
		makeSource("source", "Example/Repo.git", "source"),
		makeSource("documents", "example/repo", "documents"),
	})
	require.Equal(t, "source", pairs["example/repo\x00source"])
	require.Equal(t, "documents", pairs["example/repo\x00documents"])
	require.True(t, belongsToOwner("Example/Repo", "example"))
	require.False(t, belongsToOwner("other/repo", "example"))
}
