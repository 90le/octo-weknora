package service

import (
	"context"
	"errors"
	"testing"
	"time"

	githubconnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type githubRetryTestEnqueuer struct{}

func (githubRetryTestEnqueuer) Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	return &asynq.TaskInfo{ID: "github-test-task"}, nil
}

type githubRunningStateReadErrorRepo struct{ interfaces.SyncLogRepository }

func (githubRunningStateReadErrorRepo) HasRunningSync(context.Context, string) (bool, error) {
	return false, errors.New("database read failed")
}

func TestGitHubSyncFailurePolicySeparatesPermanentAndDeferred(t *testing.T) {
	for _, code := range []string{"github_auth", "github_not_found", "github_selected_path_missing",
		"github_tree_truncated", "github_document_file_limit", "github_documents_batch_limit"} {
		err := &githubconnector.Error{Code: code, Message: "safe failure"}
		require.False(t, githubSyncFailurePolicy(types.ConnectorTypeGitHub, err), code)
		require.Nil(t, githubDeferredRetryNotBefore(types.ConnectorTypeGitHub, err))
	}
	require.False(t, githubSyncFailurePolicy(types.ConnectorTypeGitHub, context.Canceled))
	require.True(t, githubSyncFailurePolicy(types.ConnectorTypeRSS, errors.New("existing connector behavior")))
	for _, tc := range []struct {
		wait          time.Duration
		queueRetry    bool
		deferredRetry bool
	}{
		{time.Hour, true, false},
		{6 * time.Hour, false, true},
		{24*time.Hour + time.Hour, false, false},
	} {
		at := time.Now().UTC().Add(tc.wait)
		err := &githubconnector.Error{Code: "github_rate_limit", Message: "rate limited", RetryAfter: &at}
		require.Equal(t, tc.queueRetry, githubSyncFailurePolicy(types.ConnectorTypeGitHub, err))
		deferred := githubDeferredRetryNotBefore(types.ConnectorTypeGitHub, err)
		require.Equal(t, tc.deferredRetry, deferred != nil)
		if deferred != nil {
			require.True(t, deferred.Equal(at))
		}
	}
}

func TestGitHubTransientRetryExhaustionKeepsSourceScheduledAndSameLog(t *testing.T) {
	f := newMigrationFixture(t)
	ds := f.source(t, "transient-retry", "kb-one", "0 0 */6 * * *", types.DataSourceStatusActive, 1)
	log := &types.SyncLog{ID: "transient-log", DataSourceID: ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning}
	require.NoError(t, f.logs.Create(context.Background(), log))
	retryAt := time.Now().UTC().Add(time.Hour)
	cause := &githubconnector.Error{Code: "github_rate_limit", Message: "GitHub API rate limit is exhausted", RetryAfter: &retryAt}
	policy := githubSyncFailureDecision(ds.Type, cause, time.Now().UTC())
	require.True(t, policy.QueueRetry)
	result := &types.SyncResult{}
	resultJSON, err := result.ToJSON()
	require.NoError(t, err)
	first := types.WithTaskRetryMetadata(context.Background(), 0, 1)
	require.NoError(t, f.service.updateSyncRunResult(first, ds, log, result, resultJSON,
		types.SyncLogStatusFailed, cause.Error(), false, policy.QueueRetry))
	storedLog, err := f.logs.FindByID(context.Background(), log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusRunning, storedLog.Status)
	require.Nil(t, storedLog.FinishedAt)
	secondDS, err := f.repo.FindByID(context.Background(), ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusActive, secondDS.Status)
	second := types.WithTaskRetryMetadata(context.Background(), 1, 1)
	require.NoError(t, f.service.updateSyncRunResult(second, secondDS, storedLog, result, resultJSON,
		types.SyncLogStatusFailed, cause.Error(), false, policy.QueueRetry))
	storedLog, err = f.logs.FindByID(context.Background(), log.ID)
	require.NoError(t, err)
	require.Equal(t, "transient-log", storedLog.ID)
	require.Equal(t, types.SyncLogStatusFailed, storedLog.Status)
	require.NotNil(t, storedLog.FinishedAt)
	storedDS, err := f.repo.FindByID(context.Background(), ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusActive, storedDS.Status)
	require.Contains(t, storedDS.ErrorMessage, "rate limit")
	require.Nil(t, func() *time.Time { result, _ := storedDS.ParseSyncResult(); return result.RetryNotBefore }())
	// A later scheduler startup includes this failed-run source again.
	require.NoError(t, f.scheduler.Start(context.Background()))
	defer f.scheduler.Stop()
	require.Equal(t, 1, f.scheduler.EntryCount())
}

func TestLiteGitHubFutureHintsBecomeDurableCooldownWithoutRapidRetry(t *testing.T) {
	for _, wait := range []time.Duration{time.Hour, 24 * time.Hour} {
		t.Run(wait.String(), func(t *testing.T) {
			f := newMigrationFixture(t)
			ds := f.source(t, "lite-hint", "kb-one", "0 0 */6 * * *", types.DataSourceStatusActive, 1)
			log := &types.SyncLog{ID: "lite-log", DataSourceID: ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning}
			require.NoError(t, f.logs.Create(context.Background(), log))
			at := time.Now().UTC().Add(wait)
			cause := &githubconnector.Error{Code: "github_secondary_rate_limit", Message: "temporarily limited", RetryAfter: &at}
			lite := types.WithTaskRetryMetadata(context.Background(), 0, 5)
			err := f.service.failSyncRun(lite, ds, log, nil, cause.Error(), false, cause, true)
			require.ErrorIs(t, err, asynq.SkipRetry)
			storedLog, err := f.logs.FindByID(context.Background(), log.ID)
			require.NoError(t, err)
			require.Equal(t, types.SyncLogStatusFailed, storedLog.Status)
			storedDS, err := f.repo.FindByID(context.Background(), ds.ID)
			require.NoError(t, err)
			require.Equal(t, types.DataSourceStatusActive, storedDS.Status)
			until, err := storedDS.ActiveGitHubRetryCooldown(time.Now().UTC())
			require.NoError(t, err)
			require.NotNil(t, until)
			require.True(t, until.Equal(at))
		})
	}
}

func TestGitHubLongRetryHintEndsLogAndPersistsCooldown(t *testing.T) {
	f := newMigrationFixture(t)
	ds := f.source(t, "long-cooldown", "kb-one", "0 0 */6 * * *", types.DataSourceStatusActive, 1)
	log := &types.SyncLog{ID: "long-log", DataSourceID: ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning}
	require.NoError(t, f.logs.Create(context.Background(), log))
	retryAt := time.Now().UTC().Add(6 * time.Hour)
	cause := &githubconnector.Error{Code: "github_rate_limit", Message: "GitHub API rate limit is exhausted", RetryAfter: &retryAt}
	require.False(t, githubSyncFailurePolicy(ds.Type, cause))
	err := f.service.failSyncRun(context.Background(), ds, log, nil, cause.Error(), false, cause, false)
	require.ErrorIs(t, err, asynq.SkipRetry)
	storedLog, err := f.logs.FindByID(context.Background(), log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, storedLog.Status)
	storedDS, err := f.repo.FindByID(context.Background(), ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusActive, storedDS.Status)
	until, err := storedDS.ActiveGitHubRetryCooldown(time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, until)
	require.True(t, until.Equal(retryAt))
	_, err = f.service.ManualSync(context.Background(), ds.ID)
	require.ErrorContains(t, err, "deferred until")
	// The cool-down is durable but not permanent: a later manual request may
	// queue a new run after the provider's time without reconfiguring the source.
	past := time.Now().UTC().Add(-time.Minute)
	result := &types.SyncResult{RetryNotBefore: &past}
	storedDS.LastSyncResult, err = result.ToJSON()
	require.NoError(t, err)
	require.NoError(t, f.repo.UpdateSyncState(context.Background(), storedDS))
	f.service.taskEnqueuer = githubRetryTestEnqueuer{}
	newLog, err := f.service.ManualSync(context.Background(), ds.ID)
	require.NoError(t, err)
	require.NotNil(t, newLog)
	require.NotEqual(t, log.ID, newLog.ID)
}

func TestGitHubAllFailedDeterministicCodesDoNotRetryBeyondSampleCap(t *testing.T) {
	result := &types.SyncResult{}
	for index := 0; index < 136; index++ {
		result.Failed++
		recordDeterministicSyncError(result, types.SyncItemError{Code: "unsupported_file_type", Message: "unsupported"})
	}
	require.Len(t, result.Errors, maxSyncResultErrors)
	require.Equal(t, 136, result.FailureCodes["unsupported_file_type"])
	require.False(t, allFetchedItemsRetryable(types.ConnectorTypeGitHub, result))
	require.True(t, allFetchedItemsRetryable(types.ConnectorTypeRSS, result))
	result.Failed++
	recordSyncError(result, types.SyncItemError{Code: "ingest_failed", Message: "transient or unknown"})
	require.Equal(t, 1, result.FailureCodes["other"])
	require.True(t, allFetchedItemsRetryable(types.ConnectorTypeGitHub, result))
	delete(result.FailureCodes, "other")
	require.True(t, allFetchedItemsRetryable(types.ConnectorTypeGitHub, result), "incomplete aggregate must not imply a permanent failure")
	spoofed := &types.SyncResult{Failed: 1}
	recordSyncError(spoofed, types.SyncItemError{Code: "unsupported_file_type", Message: "external error metadata"})
	require.Equal(t, 1, spoofed.FailureCodes["other"])
	require.True(t, allFetchedItemsRetryable(types.ConnectorTypeGitHub, spoofed), "connector metadata cannot declare an error permanent")
}

func TestGitHubAnomalousRetryHintOverDayFailsVisiblyWithoutEarlyRetry(t *testing.T) {
	f := newMigrationFixture(t)
	ds := f.source(t, "over-day", "kb-one", "0 0 */6 * * *", types.DataSourceStatusActive, 1)
	log := &types.SyncLog{ID: "over-day-log", DataSourceID: ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning}
	require.NoError(t, f.logs.Create(context.Background(), log))
	at := time.Now().UTC().Add(25 * time.Hour)
	cause := &githubconnector.Error{Code: "github_rate_limit", Message: "rate limited", RetryAfter: &at}
	require.False(t, githubSyncFailurePolicy(ds.Type, cause))
	require.Nil(t, githubDeferredRetryNotBefore(ds.Type, cause))
	err := f.service.failSyncRun(context.Background(), ds, log, nil, cause.Error(), false, cause, false)
	require.ErrorIs(t, err, asynq.SkipRetry)
	stored, err := f.repo.FindByID(context.Background(), ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusError, stored.Status)
	require.Contains(t, stored.ErrorMessage, "retry after")
}

func TestManualSyncFailsClosedWhenRunningStateCannotBeRead(t *testing.T) {
	f := newMigrationFixture(t)
	ds := f.source(t, "manual-state-error", "kb-one", "0 0 */6 * * *", types.DataSourceStatusActive, 1)
	f.service.syncLogRepo = githubRunningStateReadErrorRepo{f.logs}
	_, err := f.service.ManualSync(context.Background(), ds.ID)
	require.ErrorContains(t, err, "cannot verify active sync state")
	logs, err := f.logs.FindByDataSource(context.Background(), ds.ID, 10, 0)
	require.NoError(t, err)
	require.Empty(t, logs)
}

func TestGitHubPartialDeterministicFailureRemainsVisibleWithoutWholeRunRetry(t *testing.T) {
	f := newMigrationFixture(t)
	ds := f.source(t, "partial-webp", "kb-one", "0 0 */6 * * *", types.DataSourceStatusActive, 1)
	log := &types.SyncLog{ID: "partial-webp-log", DataSourceID: ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning}
	require.NoError(t, f.logs.Create(context.Background(), log))
	result := &types.SyncResult{Total: 2, Created: 1, Failed: 1}
	recordDeterministicSyncError(result, types.SyncItemError{Code: "unsupported_file_type", Title: "image.webp"})
	resultJSON, err := result.ToJSON()
	require.NoError(t, err)
	require.NoError(t, f.service.updateSyncRunResult(context.Background(), ds, log, result, resultJSON,
		types.SyncLogStatusPartial, "1 document(s) failed to sync", false, false))
	storedLog, err := f.logs.FindByID(context.Background(), log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusPartial, storedLog.Status)
	require.Equal(t, 1, storedLog.ItemsFailed)
	storedDS, err := f.repo.FindByID(context.Background(), ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusActive, storedDS.Status)
	require.Contains(t, storedDS.ErrorMessage, "1 document")
}
