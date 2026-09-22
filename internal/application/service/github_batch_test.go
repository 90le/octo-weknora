package service

import (
	"context"
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

func TestGitHubDataSourcePairsCanonicalizeLegacyDocumentRepositories(t *testing.T) {
	makeSource := func(id string, settings map[string]interface{}) *types.DataSource {
		blob, err := (&types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: settings}).ToJSON()
		require.NoError(t, err)
		return &types.DataSource{ID: id, Type: types.ConnectorTypeGitHub, Config: blob}
	}

	pairs := githubDataSourcePairs([]*types.DataSource{
		// Legacy sources predate the explicit mode and must remain visible to
		// document-mode batch retries.
		makeSource("legacy-documents", map[string]interface{}{
			"repository": "https://GitHub.com/Mininglamp-OSS/Octo-CLI.git",
		}),
		makeSource("empty-mode-documents", map[string]interface{}{
			"repository": "MININGLAMP-OSS/octo-server",
			"mode":       "",
		}),
	})

	require.Equal(t, "legacy-documents", pairs["mininglamp-oss/octo-cli\x00documents"])
	require.Equal(t, "empty-mode-documents", pairs["mininglamp-oss/octo-server\x00documents"])
	key, ok := canonicalGitHubDataSourcePair("Mininglamp-OSS/OCTO-CLI.git", "documents")
	require.True(t, ok)
	require.Equal(t, "legacy-documents", pairs[key])
}

func TestGitHubDataSourcePairsKeepDocumentAndSourceModesDistinct(t *testing.T) {
	makeSource := func(id, repository, mode string) *types.DataSource {
		blob, err := (&types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: map[string]interface{}{
			"repository": repository,
			"mode":       mode,
		}}).ToJSON()
		require.NoError(t, err)
		return &types.DataSource{ID: id, Type: types.ConnectorTypeGitHub, Config: blob}
	}
	pairs := githubDataSourcePairs([]*types.DataSource{
		makeSource("documents", "https://github.com/Mininglamp-OSS/octo-cli.git", "documents"),
		makeSource("source", "mininglamp-oss/OCTO-CLI", "source"),
	})

	documentKey, documentOK := canonicalGitHubDataSourcePair("Mininglamp-OSS/octo-cli", "documents")
	sourceKey, sourceOK := canonicalGitHubDataSourcePair("https://github.com/MININGLAMP-OSS/octo-cli.git", "source")
	require.True(t, documentOK)
	require.True(t, sourceOK)
	require.NotEqual(t, documentKey, sourceKey)
	require.Equal(t, "documents", pairs[documentKey])
	require.Equal(t, "source", pairs[sourceKey])
}

func TestCanonicalGitHubDataSourcePairDefaultsEmptyModeToDocuments(t *testing.T) {
	key, ok := canonicalGitHubDataSourcePair("https://github.com/Mininglamp-OSS/octo-cli.git", "")
	require.True(t, ok)
	require.Equal(t, "mininglamp-oss/octo-cli\x00documents", key)
	require.Empty(t, requestedGitHubBatchMode(""), "new batch requests must choose a mode explicitly")

	_, ok = canonicalGitHubDataSourcePair("https://github.com/Mininglamp-OSS/octo-cli/tree/main", "documents")
	require.False(t, ok, "a GitHub file/tree URL is not a repository identity")
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

func TestCreateGitHubBatchRejectsTooManyExclusionsBeforeCreating(t *testing.T) {
	service := &DataSourceService{}
	response, err := service.CreateGitHubBatch(context.Background(), &types.GitHubBatchRequest{
		TenantID:        1,
		KnowledgeBaseID: "kb",
		Owner:           "Mininglamp-OSS",
		Repositories: []types.GitHubRepositoryCandidate{{
			Repository:    "Mininglamp-OSS/octo-cli",
			DefaultBranch: "main",
		}},
		Mode:    "source",
		Exclude: make([]string, maxGitHubBatchExclusions+1),
	})
	require.Nil(t, response)
	require.EqualError(t, err, "GitHub batch exclusions must contain at most 100 paths")
}
