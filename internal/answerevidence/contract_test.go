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
		{"Octo 支持 Codex 接入 IM Bot 吗？", IntentRelease},
		{"帮我解释一下知识库", IntentNone},
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
	require.Contains(t, Prompt(ctx), "source_browse")
	require.Contains(t, FallbackReply(ctx), "可核验的源码")

	RecordSourceRead(ctx)
	require.True(t, SourceReadObserved(ctx))
	require.True(t, VerifiedEvidenceObserved(ctx))
	require.False(t, ShouldHoldStreamingAnswer(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "它会读取配置并发送请求。"))
	require.False(t, NeedsSynthesisFallback(ctx))
}

func TestReleaseContractNeverEquatesMissingRAGWithUnsupported(t *testing.T) {
	ctx := WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	require.Equal(t, IntentRelease, IntentFromContext(ctx))
	require.True(t, ShouldHoldStreamingAnswer(ctx), "unverified negative content must not stream optimistically")
	require.True(t, NeedsEvidenceRetry(ctx, "Claude 不支持原生接入。"))
	require.False(t, NeedsEvidenceRetry(ctx, "当前授权资料无法确认 Claude 是否支持原生接入。"), "an explicit unknown is not an unsupported claim")
	require.True(t, NeedsSynthesisFallback(ctx))
	require.Contains(t, Prompt(ctx), "未命中")
	require.Contains(t, FallbackReply(ctx), "不能仅因资料未命中")

	RecordDocumentEvidence(ctx)
	require.True(t, VerifiedEvidenceObserved(ctx))
	require.False(t, ShouldHoldStreamingAnswer(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "Claude 不支持原生接入。"))
	require.False(t, NeedsSynthesisFallback(ctx))
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
