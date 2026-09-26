package service

import (
	"context"
	"errors"
	"time"

	githubconnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

type githubSyncDecision struct {
	QueueRetry bool
	DeferUntil *time.Time
}

// githubSyncFailureDecision evaluates a single clock for both queue retry and
// durable cooldown. This avoids a split decision around the 90-minute boundary
// if the policy and result persistence happen milliseconds apart.
func githubSyncFailureDecision(sourceType string, cause error, now time.Time) githubSyncDecision {
	if sourceType != types.ConnectorTypeGitHub {
		return githubSyncDecision{QueueRetry: true}
	}
	var gitErr *githubconnector.Error
	if !errors.As(cause, &gitErr) || !gitErr.Retryable() {
		return githubSyncDecision{}
	}
	if retryAt, ok := gitErr.RetryAfterAt(); ok {
		remaining := retryAt.Sub(now)
		if remaining > types.GitHubDeferredRetryHintMax {
			return githubSyncDecision{}
		}
		if remaining > types.GitHubQueuedRetryHintMax {
			return githubSyncDecision{DeferUntil: &retryAt}
		}
	}
	return githubSyncDecision{QueueRetry: true}
}

// Lite's in-process executor retries after fixed 5–30 second sleeps and
// cannot honor a provider's future Retry-After safely. When its retry metadata
// is present (and native Asynq metadata is absent), persist any explicit
// future hint as a terminal cooldown instead of rapidly reissuing GitHub
// requests. Redis/Asynq keeps short hints in its delayed queue.
func githubSyncFailureDecisionForExecution(ctx context.Context, sourceType string, cause error, now time.Time) githubSyncDecision {
	decision := githubSyncFailureDecision(sourceType, cause, now)
	if sourceType != types.ConnectorTypeGitHub || !decision.QueueRetry {
		return decision
	}
	if _, _, lite := types.TaskRetryMetadataFromContext(ctx); !lite {
		return decision
	}
	if _, native := asynq.GetRetryCount(ctx); native {
		return decision
	}
	var gitErr *githubconnector.Error
	if !errors.As(cause, &gitErr) {
		return decision
	}
	retryAt, hinted := gitErr.RetryAfterAt()
	if !hinted || !retryAt.After(now) {
		return decision
	}
	decision.QueueRetry = false
	if retryAt.Sub(now) <= types.GitHubDeferredRetryHintMax {
		decision.DeferUntil = &retryAt
	}
	return decision
}

// githubSyncFailurePolicy is the shared bool contract for GitHub document,
// snapshot and future checkpointed paths. Other connectors retain their
// established retry behavior; unknown GitHub failures do not retry blindly.
func githubSyncFailurePolicy(sourceType string, cause error) bool {
	return githubSyncFailureDecision(sourceType, cause, time.Now().UTC()).QueueRetry
}

// githubDeferredRetryNotBefore persists a long, valid provider cooldown in
// LastSyncResult. The current SyncLog then ends failed (not running), and
// scheduler/manual admission must not fetch until this time. Hints above 24h
// are considered anomalous and require operator review rather than an early
// retry or an unbounded automatic pause.
func githubDeferredRetryNotBefore(sourceType string, cause error) *time.Time {
	return githubSyncFailureDecision(sourceType, cause, time.Now().UTC()).DeferUntil
}

// allFetchedItemsRetryable is deliberately conservative when error counts are
// incomplete or mixed. Only GitHub imports whose *every* failed item has a
// known deterministic code stop queue retries; unclassified ingestion errors
// remain retryable. The aggregate counts are exact even when UI samples cap at
// 100 items.
func allFetchedItemsRetryable(sourceType string, result *types.SyncResult) bool {
	if sourceType != types.ConnectorTypeGitHub || result == nil || result.Failed <= 0 {
		return true
	}
	counted := 0
	for code, count := range result.FailureCodes {
		if code != "unsupported_file_type" && code != "duplicate_other_source" {
			return true
		}
		counted += count
	}
	return counted != result.Failed
}
