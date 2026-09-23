package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestBuildAgentModelInputKeepsPolicyQuerySeparateFromQuotedContext(t *testing.T) {
	original := "Octo 和 Loop 的关系是什么？"
	req := &types.QARequest{
		Query:         original,
		QuotedContext: "GROUP.md：联系人仅作指引，不主动通知、催办或发布。",
	}
	ctx, modelQuery, imageURLs := buildAgentModelInput(context.Background(), req, false, nil)
	require.Empty(t, imageURLs)
	require.Equal(t, original, req.Query, "stored user question must remain unchanged")
	require.Contains(t, modelQuery, req.QuotedContext, "group rules must still reach the model")
	require.Equal(t, answerevidence.IntentRelease, answerevidence.Classify(modelQuery), "the model query must exercise the prior misclassification")
	require.Equal(t, answerevidence.IntentNone, answerevidence.IntentFromContext(ctx))
	require.Same(t, ctx, answerevidence.WithContract(ctx, modelQuery), "engine must not reclassify model context")
	require.False(t, answerevidence.NeedsEvidenceRetry(ctx, "Loop 是 Octo 平台的项目协作模块。"))
	require.Empty(t, answerevidence.FallbackReply(ctx))
}

func TestBuildAgentModelInputPreservesRealReleaseQuestion(t *testing.T) {
	req := &types.QARequest{Query: "Octo 安卓最新版本是什么？", QuotedContext: "GROUP.md: 请简洁回答问题。"}
	ctx, modelQuery, _ := buildAgentModelInput(context.Background(), req, false, nil)
	require.Contains(t, modelQuery, req.QuotedContext)
	require.Equal(t, answerevidence.IntentRelease, answerevidence.IntentFromContext(ctx))
	require.True(t, answerevidence.NeedsEvidenceRetry(ctx, "最新版本是 v1.3.7。"), "real release question still requires release provenance")
}
