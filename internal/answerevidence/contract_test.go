package answerevidence

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyKeepsSourceFactsSeparateFromReleaseQuestions(t *testing.T) {
	cases := []struct {
		query string
		want  Intent
	}{
		{"apiSecretsForRequest 在哪个文件、如何实现？", IntentSource},
		{"octo-android 最新版本更新了什么？", IntentRelease},
		{"Octo 支持 Codex 接入 IM Bot 吗？", IntentIntegration},
		{"codex-channel-octo、cc-channel-octo 和 hermes-channel-octo 项目是干嘛的？", IntentIntegration},
		{"最新版本是否支持新的 IM 接入？", IntentRelease},
		{"帮我解释一下知识库", IntentNone},
		{"这个类明天怎么安排？", IntentNone},
		{"这个方法先讨论一下", IntentNone},
		{"最新版本的源码里这个函数如何实现？", IntentSource},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			require.Equal(t, tc.want, Classify(tc.query))
		})
	}
}

func TestSourceContractRequiresReadAndProducesLocalizedFallback(t *testing.T) {
	ctx := WithContract(context.Background(), "这个函数的源码怎么实现？")
	require.Equal(t, IntentSource, IntentFromContext(ctx))
	require.True(t, ShouldHoldStreamingAnswer(ctx))
	require.True(t, NeedsEvidenceRetry(ctx, "它会读取配置并发送请求。"))
	require.True(t, NeedsSynthesisFallback(ctx))
	require.False(t, AllowsMissingIssue(ctx))
	require.Contains(t, Prompt(ctx), "source_browse")
	require.Contains(t, FallbackReply(ctx), "可核验的源码")

	RecordSourceRead(ctx)
	require.True(t, SourceReadObserved(ctx))
	require.True(t, VerifiedEvidenceObserved(ctx))
	require.False(t, ShouldHoldStreamingAnswer(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "它会读取配置并发送请求。"))
	require.False(t, NeedsSynthesisFallback(ctx))
	require.True(t, AllowsMissingIssue(ctx))
}

func TestReleaseContractRequiresReleaseProvenance(t *testing.T) {
	ctx := WithContract(context.Background(), "octo-android 最新版本更新了什么？")
	require.Equal(t, IntentRelease, IntentFromContext(ctx))
	require.True(t, ShouldHoldStreamingAnswer(ctx), "latest release content must not stream optimistically")
	require.True(t, NeedsEvidenceRetry(ctx, "最新版本是 1.2.3，更新了登录。"))
	require.True(t, NeedsEvidenceRetry(ctx, "v1.2.3"), "a bare tag is still a release conclusion")
	require.True(t, NeedsEvidenceRetry(ctx, "目前没有发布版本。"), "a negative release conclusion also needs provenance")
	require.False(t, NeedsEvidenceRetry(ctx, "当前材料无法确认最新版本。"), "an explicit unknown is not a release claim")
	require.True(t, NeedsSynthesisFallback(ctx))
	require.False(t, AllowsMissingIssue(ctx))
	require.Contains(t, Prompt(ctx), "可信发布查询")
	require.Contains(t, FallbackReply(ctx), "可核验的发布记录")

	RecordDocumentEvidence(ctx)
	require.True(t, VerifiedEvidenceObserved(ctx))
	require.False(t, ReleaseEvidenceObserved(ctx), "a document hit cannot establish the latest release")
	require.True(t, ShouldHoldStreamingAnswer(ctx))
	require.True(t, NeedsEvidenceRetry(ctx, "最新版本是 1.2.3。"))

	RecordReleaseEvidence(ctx)
	require.True(t, ReleaseEvidenceObserved(ctx))
	require.False(t, ShouldHoldStreamingAnswer(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "最新版本是 1.2.3。"))
	require.False(t, NeedsSynthesisFallback(ctx))
	require.True(t, AllowsMissingIssue(ctx))
}

func TestIntegrationContractRequiresDocumentOrSourceForPositiveAndNegativeClaims(t *testing.T) {
	ctx := WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	require.Equal(t, IntentIntegration, IntentFromContext(ctx))
	require.True(t, ShouldHoldStreamingAnswer(ctx), "integration content must not stream optimistically")
	require.True(t, NeedsEvidenceRetry(ctx, "Claude 支持原生接入。"))
	require.True(t, NeedsEvidenceRetry(ctx, "Claude 不支持原生接入。"))
	require.True(t, NeedsEvidenceRetry(ctx, "可以。"), "a terse affirmative is still an integration conclusion")
	require.True(t, NeedsEvidenceRetry(ctx, "不可以。"), "a terse negative is still an integration conclusion")
	require.False(t, NeedsEvidenceRetry(ctx, "当前授权资料无法确认 Claude 是否支持原生接入。"), "an explicit unknown is not an integration claim")
	require.True(t, NeedsSynthesisFallback(ctx))
	require.False(t, AllowsMissingIssue(ctx))
	require.Contains(t, Prompt(ctx), "无论结论是“支持”还是“不支持”")
	require.Contains(t, FallbackReply(ctx), "README、接口文档或源码")

	RecordReleaseEvidence(ctx)
	require.True(t, ReleaseEvidenceObserved(ctx))
	require.True(t, ShouldHoldStreamingAnswer(ctx), "a release tag cannot establish an integration")
	require.True(t, NeedsEvidenceRetry(ctx, "Claude 支持原生接入。"))

	RecordDocumentEvidence(ctx)
	require.True(t, DocumentOrSourceEvidenceObserved(ctx))
	require.False(t, ShouldHoldStreamingAnswer(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "Claude 支持原生接入。"))
	require.False(t, NeedsEvidenceRetry(ctx, "Claude 不支持原生接入。"))
	require.False(t, NeedsSynthesisFallback(ctx))
	require.True(t, AllowsMissingIssue(ctx))
}

func TestEvidenceRetryIsBounded(t *testing.T) {
	ctx := WithContract(context.Background(), "这个函数怎么实现？")
	require.True(t, CanRetryEvidence(ctx))
	require.False(t, CanRetryEvidence(ctx))
}

func TestClaimsUnsupportedAvoidsUncertainLanguage(t *testing.T) {
	for _, answer := range []string{
		"该渠道不支持这个功能。",
		"There is no integration for this channel.",
		"This is not supported.",
	} {
		require.True(t, ClaimsUnsupported(answer), answer)
	}
	for _, answer := range []string{
		"当前资料无法确认是否支持。",
		"The current evidence cannot confirm support.",
	} {
		require.False(t, ClaimsUnsupported(answer), answer)
	}
}
