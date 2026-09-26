package service

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestGitHubDataSourcePairsCanonicalizeLegacySSHRepositories(t *testing.T) {
	makeSource := func(id, repository string) *types.DataSource {
		blob, err := (&types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: map[string]interface{}{
			"repository": repository,
			"mode":       "source",
		}}).ToJSON()
		require.NoError(t, err)
		return &types.DataSource{ID: id, Type: types.ConnectorTypeGitHub, Config: blob}
	}

	pairs := githubDataSourcePairs([]*types.DataSource{
		makeSource("ssh-colon", "git@github.com:Mininglamp-OSS/octo-cli.git"),
		makeSource("ssh-url", "ssh://git@github.com/Mininglamp-OSS/octo-server.git"),
	})

	require.Equal(t, "ssh-colon", pairs["mininglamp-oss/octo-cli\x00source"])
	require.Equal(t, "ssh-url", pairs["mininglamp-oss/octo-server\x00source"])
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

func TestResolveGitHubBatchSyncPlanManualPersistsNoScheduleAndQueuesNothing(t *testing.T) {
	plan, err := resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{SyncPolicy: "manual"})
	require.NoError(t, err)
	require.Empty(t, plan.Schedule)
	require.False(t, plan.StartSync)
}

func TestResolveGitHubBatchSyncPlanManualRejectsAmbiguousAutoQueue(t *testing.T) {
	_, err := resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{SyncPolicy: "manual", StartSync: true})
	require.EqualError(t, err, "manual GitHub batch sync policy cannot queue an initial sync")

	_, err = resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{SyncPolicy: "manual", SyncSchedule: "0 0 */6 * * *"})
	require.EqualError(t, err, "manual GitHub batch sync policy cannot include a schedule")
}

func TestResolveGitHubBatchSyncPlanScheduledKeepsAndValidatesSchedule(t *testing.T) {
	plan, err := resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{
		SyncPolicy:   "scheduled",
		SyncSchedule: "0 0 */6 * * *",
		StartSync:    true,
	})
	require.NoError(t, err)
	require.Equal(t, "0 0 */6 * * *", plan.Schedule)
	require.True(t, plan.StartSync)

	_, err = resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{SyncPolicy: "scheduled", SyncSchedule: "not a cron"})
	require.ErrorContains(t, err, "GitHub batch sync schedule is invalid")

	_, err = resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{SyncPolicy: "scheduled"})
	require.EqualError(t, err, "scheduled GitHub batch sync policy requires a schedule")
}

func TestResolveGitHubBatchSyncPlanLegacyRequestsKeepSixHourDefault(t *testing.T) {
	plan, err := resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{})
	require.NoError(t, err)
	require.Equal(t, defaultGitHubBatchSchedule, plan.Schedule)
	require.False(t, plan.StartSync)
}

func TestGitHubStaggeredScheduleStableAcrossBatchesAndModes(t *testing.T) {
	plan, err := resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{SyncPolicy: "staggered"})
	require.NoError(t, err)
	require.True(t, plan.Staggered)
	require.Equal(t, "0 54 5,11,17,23 * * *", plan.scheduleFor("Mininglamp-OSS/octo-cli", "source"))
	require.Equal(t, plan.scheduleFor("Mininglamp-OSS/octo-cli", "source"), plan.scheduleFor("MININGLAMP-OSS/OCTO-CLI.git", "source"))
	require.Equal(t, "0 33 2,8,14,20 * * *", plan.scheduleFor("Mininglamp-OSS/octo-cli", "documents"))
	require.NotEqual(t, plan.scheduleFor("Mininglamp-OSS/octo-cli", "source"), plan.scheduleFor("Mininglamp-OSS/octo-cli", "documents"))
	require.NoError(t, validateGitHubBatchSchedule(plan.scheduleFor("Mininglamp-OSS/octo-cli", "source")))
	_, err = resolveGitHubBatchSyncPlan(&types.GitHubBatchRequest{SyncPolicy: "staggered", SyncSchedule: defaultGitHubBatchSchedule})
	require.EqualError(t, err, "staggered GitHub batch sync policy cannot include a schedule")
}

func TestGitHubStaggeredScheduleDistributesSixtySourcesAcrossWindow(t *testing.T) {
	seen := map[string]struct{}{}
	for index := 0; index < 60; index++ {
		schedule := githubStaggeredSixHourSchedule(fmt.Sprintf("Mininglamp-OSS/repo-%02d", index), "source")
		require.NoError(t, validateGitHubBatchSchedule(schedule))
		seen[schedule] = struct{}{}
	}
	require.GreaterOrEqual(t, len(seen), 50, "stable hash must not concentrate sequential repositories")
}

func TestPreviewGitHubLegacySchedulesOnlyExactActiveDefault(t *testing.T) {
	makeSource := func(id, schedule, status, repository, mode string) *types.DataSource {
		blob, err := (&types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: map[string]interface{}{
			"repository": repository, "mode": mode,
		}}).ToJSON()
		require.NoError(t, err)
		return &types.DataSource{ID: id, Type: types.ConnectorTypeGitHub, Status: status, SyncSchedule: schedule, Config: blob}
	}
	rows := []*types.DataSource{
		makeSource("eligible", defaultGitHubBatchSchedule, types.DataSourceStatusActive, "Mininglamp-OSS/octo-cli", "source"),
		makeSource("custom", "0 30 */6 * * *", types.DataSourceStatusActive, "Mininglamp-OSS/octo-web", "documents"),
		makeSource("manual", "", types.DataSourceStatusActive, "Mininglamp-OSS/octo-im", "source"),
		makeSource("paused", defaultGitHubBatchSchedule, types.DataSourceStatusPaused, "Mininglamp-OSS/octo-server", "source"),
		makeSource("error", defaultGitHubBatchSchedule, types.DataSourceStatusError, "Mininglamp-OSS/octo-android", "documents"),
	}
	rows = append(rows, &types.DataSource{ID: "other", Type: "local_folder", Status: types.DataSourceStatusActive})
	preview := PreviewGitHubLegacySchedules(rows)
	require.Len(t, preview, 5)
	require.True(t, preview[0].Eligible)
	require.Equal(t, "0 54 5,11,17,23 * * *", preview[0].Proposed)
	for index, reason := range []string{"custom_schedule", "manual_schedule", "not_active", "not_active"} {
		require.False(t, preview[index+1].Eligible)
		require.Empty(t, preview[index+1].Proposed)
		require.Equal(t, reason, preview[index+1].Reason)
	}
	require.Equal(t, defaultGitHubBatchSchedule, rows[0].SyncSchedule, "preview must not write an existing source")
}
