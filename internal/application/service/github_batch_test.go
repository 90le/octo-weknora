package service

import (
	"encoding/json"
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

func TestGitHubBatchSettingsOmitEmptyExclusions(t *testing.T) {
	for _, excludes := range [][]string{nil, {}} {
		settings := githubBatchSettings("Mininglamp-OSS/octo-cli", "main", "source", nil, excludes)
		require.NotContains(t, settings, "exclude")

		blob, err := (&types.DataSourceConfig{
			Type:     types.ConnectorTypeGitHub,
			Settings: settings,
		}).ToJSON()
		require.NoError(t, err)
		var encoded struct {
			Settings map[string]json.RawMessage `json:"settings"`
		}
		require.NoError(t, json.Unmarshal(blob, &encoded))
		_, exists := encoded.Settings["exclude"]
		require.False(t, exists, "empty exclusions must use source defaults rather than JSON null")
	}
}

func TestGitHubBatchSettingsKeepExplicitExclusions(t *testing.T) {
	settings := githubBatchSettings(
		"Mininglamp-OSS/octo-cli", "main", "source", []string{"docs"}, []string{"generated", "dist"},
	)
	require.Equal(t, []string{"docs"}, settings["paths"])
	require.Equal(t, []string{"generated", "dist"}, settings["exclude"])
}
