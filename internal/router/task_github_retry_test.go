package router

import (
	"testing"
	"time"

	githubconnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestGitHubQueueDelayHonorsOneHourResetWithoutSleepingWorker(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	reset := now.Add(time.Hour)
	err := &githubconnector.Error{Code: "github_rate_limit", Message: "rate limited", RetryAfter: &reset}
	task := asynq.NewTask(types.TypeDataSourceSync, []byte(`{"data_source_id":"one"}`))
	wait, ok := sourceRetryDelay(1, err, task, now)
	require.True(t, ok)
	require.Greater(t, wait, time.Hour, "jitter must be positive and never retry before reset")
	require.LessOrEqual(t, wait, time.Hour+30*time.Second+time.Millisecond)
	again, ok := sourceRetryDelay(1, err, task, now)
	require.True(t, ok)
	require.Equal(t, wait, again, "same datasource task spreads stably across retries")
}

func TestGitHubQueueDelaySecondaryAndPermanent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	secondaryAt := now.Add(time.Minute)
	task := asynq.NewTask(types.TypeDataSourceSync, []byte(`{"data_source_id":"two"}`))
	wait, ok := sourceRetryDelay(1, &githubconnector.Error{
		Code: "github_secondary_rate_limit", RetryAfter: &secondaryAt,
	}, task, now)
	require.True(t, ok)
	require.Greater(t, wait, time.Minute)
	require.LessOrEqual(t, wait, time.Minute+3*time.Second+time.Millisecond)
	_, ok = sourceRetryDelay(1, &githubconnector.Error{Code: "github_auth"}, task, now)
	require.False(t, ok)
	_, ok = sourceRetryDelay(1, &githubconnector.Error{Code: "github_http", StatusCode: 503},
		asynq.NewTask("unrelated", nil), now)
	require.False(t, ok)
}
