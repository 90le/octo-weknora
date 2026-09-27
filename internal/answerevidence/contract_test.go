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
		{"Octo 安卓版本是多少？", IntentRelease},
		{"Octo 支持 Codex 接入 IM Bot 吗？", IntentIntegration},
		{"codex-channel-octo、cc-channel-octo 和 hermes-channel-octo 项目是干嘛的？", IntentIntegration},
		{"最新版本是否支持新的 IM 接入？", IntentRelease},
		{"帮我解释一下知识库", IntentNone},
		{"这个类明天怎么安排？", IntentNone},
		{"这个方法先讨论一下", IntentNone},
		{"最新版本的源码里这个函数如何实现？", IntentSource},
		{"知识发布流程是什么？", IntentNone},
		{"在 Node.js 环境如何安装 Octo CLI？请根据知识库回答并给出来源。", IntentNone},
		{"Vue.js 安装方式是什么？", IntentNone},
		{"请读取 src/index.js 文件中的源码实现。", IntentSource},
		{"请给出固定版本来源。", IntentNone},
		{"请结合两个仓库的 README／源码给固定版本来源。", IntentSource},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			require.Equal(t, tc.want, Classify(tc.query))
		})
	}
}

func TestFixedRevisionCitationDoesNotBecomeLatestReleaseRequest(t *testing.T) {
	query := "在 Node.js 如何安装 Octo CLI？openclaw-channel-octo 如何接收 Octo 消息，基础 IM 接入是否依赖 CLI？请分别核对两个仓库的 README／源码后给出固定版本来源。"
	ctx := WithContract(context.Background(), query)
	require.True(t, Requires(ctx, IntentSource))
	require.True(t, Requires(ctx, IntentIntegration))
	require.False(t, Requires(ctx, IntentRelease), "a pinned source citation is not a request for the latest published release")
	require.NotContains(t, Prompt(ctx), "github_release_lookup")
}

func TestBoundedNamedRepositorySearchAndReadCanAnswerIntegration(t *testing.T) {
	query := "在 Node.js 如何安装 Octo CLI？openclaw-channel-octo 如何接收 Octo 消息，基础 IM 接入是否依赖 CLI？请分别核对两个仓库的 README／源码后给出固定版本来源。"
	ctx := WithContract(context.Background(), query)
	require.True(t, Requires(ctx, IntentSource))
	require.True(t, Requires(ctx, IntentIntegration))
	require.False(t, Requires(ctx, IntentRelease))
	require.Equal(t, []string{"openclaw-channel-octo"}, RequiredRepositories(ctx))

	// A large repository may hit its per-source search limit even after the
	// relevant README was found. Incompleteness cannot prove absence, but a
	// trusted read of that repository's actual file can support an answer.
	RecordSourceSearch(ctx, false, true, "Mininglamp-OSS/openclaw-channel-octo")
	require.False(t, SourceSearchComplete(ctx))
	require.False(t, IntegrationEvidenceObserved(ctx), "search alone never proves a source fact")
	RecordSourceRead(ctx, "Mininglamp-OSS/openclaw-channel-octo")
	require.True(t, IntegrationEvidenceObserved(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "基础 IM 接入使用插件 WebSocket，不依赖 CLI。"))
	require.False(t, NeedsSynthesisFallback(ctx))

	zeroHit := WithContract(context.Background(), query)
	RecordSourceSearch(zeroHit, false, false, "Mininglamp-OSS/openclaw-channel-octo")
	require.False(t, IntegrationEvidenceObserved(zeroHit))
	require.True(t, NeedsSynthesisFallback(zeroHit), "an incomplete search without a read still cannot support an answer")

	globalPage := WithContract(context.Background(), query)
	RecordSourceSearch(globalPage, false, true) // a filtered global page has no per-repository audit
	RecordSourceRead(globalPage, "Mininglamp-OSS/openclaw-channel-octo")
	require.False(t, IntegrationEvidenceObserved(globalPage), "a global page cannot stand in for a scoped repository search")
}

func TestScopedZeroHitDoesNotProveGlobalIntegrationAbsence(t *testing.T) {
	ctx := WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	RecordSourceSearch(ctx, true, false, "Mininglamp-OSS/one-repository")
	require.False(t, SourceSearchComplete(ctx), "one repository is not the whole authorized catalog")
	require.True(t, NeedsEvidenceRetry(ctx, "当前授权资料无法确认是否支持。"))
	RecordSourceSearch(ctx, true, false) // exhaustive authorized global search
	require.True(t, SourceSearchComplete(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "当前授权资料无法确认是否支持。"))
}

func TestNamedRepositorySearchAndReadMustHaveSameOwner(t *testing.T) {
	ctx := WithContract(context.Background(), "openclaw-channel-octo 如何接收消息？")
	RecordSourceSearch(ctx, false, true, "OtherOrg/openclaw-channel-octo")
	RecordSourceRead(ctx, "Mininglamp-OSS/openclaw-channel-octo")
	require.False(t, IntegrationEvidenceObserved(ctx), "matching repository leaves from different owners are not one source")
	require.Equal(t, []string{"openclaw-channel-octo"}, MissingRequiredRepositories(ctx), "retry must name the still-unverified project")
	RecordSourceSearch(ctx, false, true, "Mininglamp-OSS/openclaw-channel-octo")
	require.True(t, IntegrationEvidenceObserved(ctx))
	require.Empty(t, MissingRequiredRepositories(ctx))
}

func TestNamedChannelRepositoryPreservesExplicitOwner(t *testing.T) {
	ctx := WithContract(context.Background(), "OtherOrg/openclaw-channel-octo 如何接收消息？")
	require.Equal(t, []string{"otherorg/openclaw-channel-octo"}, RequiredRepositories(ctx))
	RecordSourceSearch(ctx, false, true, "Mininglamp-OSS/openclaw-channel-octo")
	RecordSourceRead(ctx, "Mininglamp-OSS/openclaw-channel-octo")
	require.False(t, IntegrationEvidenceObserved(ctx), "another owner's repo cannot satisfy the user's explicit owner")
	require.Equal(t, []string{"otherorg/openclaw-channel-octo"}, MissingRequiredRepositories(ctx))
	RecordSourceSearch(ctx, false, true, "OtherOrg/openclaw-channel-octo")
	RecordSourceRead(ctx, "OtherOrg/openclaw-channel-octo")
	require.True(t, IntegrationEvidenceObserved(ctx))
}

func TestRuntimeNameDoesNotForceSourceOnlyFallback(t *testing.T) {
	query := "在 Node.js 环境如何安装 Octo CLI？请根据知识库回答并给出来源。"
	ctx := WithContract(context.Background(), query)
	require.Equal(t, IntentNone, IntentFromContext(ctx))
	require.False(t, Requires(ctx, IntentSource))
	require.False(t, ShouldHoldStreamingAnswer(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "npm install -g @mininglamp-oss/octo-cli"))
	require.Empty(t, FallbackReply(ctx))
}

func TestCompoundReleaseAndSourceRequireBothTrustedProofs(t *testing.T) {
	ctx := WithContract(context.Background(), "octo-android 最新版本的源码如何实现？")
	require.Equal(t, IntentSource, IntentFromContext(ctx), "the legacy primary route remains source")
	require.True(t, Requires(ctx, IntentSource))
	require.True(t, Requires(ctx, IntentRelease))
	require.False(t, Requires(ctx, IntentIntegration))
	require.Contains(t, Prompt(ctx), "source_browse")
	require.Contains(t, Prompt(ctx), "github_release_lookup")
	require.True(t, ShouldHoldStreamingAnswer(ctx))
	require.True(t, NeedsEvidenceRetry(ctx, "最新版通过该函数实现。"))
	for n := 0; n < 4; n++ {
		require.True(t, CanRetryEvidence(ctx), "compound catalog/reader pairs need a bounded retry budget")
	}
	require.False(t, CanRetryEvidence(ctx))

	RecordSourceRead(ctx, "ExampleOrg/octo-android")
	require.True(t, NeedsEvidenceRetry(ctx, "最新版通过该函数实现。"), "source alone cannot prove which release is latest")
	require.False(t, AllowsMissingIssue(ctx))
	require.Contains(t, RetryNudge(ctx), "发布记录")
	RecordReleaseLookup(ctx)
	require.True(t, NeedsEvidenceRetry(ctx, "当前无法确认最新版，但源码如此实现。"), "a lookup without release provenance is not enough")
	require.Contains(t, FallbackReply(ctx), "发布记录")

	RecordReleaseEvidence(ctx, "ExampleOrg/octo-android")
	require.True(t, sameRepositoryEvidenceObserved(ctx), "full repository identities can be joined without confusing owners")
	require.True(t, ShouldHoldStreamingAnswer(ctx), "same repository does not prove the release tag matches the snapshot commit")
	require.True(t, NeedsEvidenceRetry(ctx, "最新版通过该函数实现。"))
	require.False(t, CanRetryEvidence(ctx), "the existing tools cannot resolve a release tag to the read snapshot commit")
	require.True(t, NeedsSynthesisFallback(ctx))
	require.False(t, AllowsMissingIssue(ctx))
	require.Contains(t, FallbackReply(ctx), "发布标签对应的提交")
}

func TestCompoundSourceAndReleaseRejectDifferentRepositories(t *testing.T) {
	ctx := WithContract(context.Background(), "octo-android 最新版本的源码如何实现？")
	RecordSourceRead(ctx, "OtherOrg/octo-android")
	RecordReleaseEvidence(ctx, "ExampleOrg/octo-android")
	require.False(t, sameRepositoryEvidenceObserved(ctx), "same leaf under a different owner is not the same repository")
	require.True(t, NeedsEvidenceRetry(ctx, "最新版源码如此实现。"))
	require.False(t, CanRetryEvidence(ctx), "repeating unchanged source/release calls cannot repair an unsupported association")
	require.Contains(t, FallbackReply(ctx), "不同仓库")
}

func TestCompoundReleaseLookupWithoutStableProvenanceStopsPointlessRetries(t *testing.T) {
	ctx := WithContract(context.Background(), "octo-android 最新版本的源码如何实现？")
	RecordSourceRead(ctx, "ExampleOrg/octo-android")
	RecordReleaseLookup(ctx)
	require.True(t, NeedsEvidenceRetry(ctx, "该源码是最新版实现。"))
	require.False(t, CanRetryEvidence(ctx), "trusted latest endpoint already ran; do not spend four rounds repeating it")
	require.Contains(t, FallbackReply(ctx), "已查询官方最新发布来源")
	require.Contains(t, FallbackReply(ctx), "未取得可核验的稳定发布记录")
	require.NotContains(t, FallbackReply(ctx), "没有发布版本")

	unqueried := WithContract(context.Background(), "octo-android 最新版本的源码如何实现？")
	RecordSourceRead(unqueried, "ExampleOrg/octo-android")
	require.NotContains(t, FallbackReply(unqueried), "已查询官方最新发布来源")

	missingSource := WithContract(context.Background(), "octo-android 最新版本的源码如何实现？")
	RecordReleaseLookup(missingSource)
	require.True(t, CanRetryEvidence(missingSource))
	require.True(t, CanRetryEvidence(missingSource))
	require.False(t, CanRetryEvidence(missingSource), "only one search/read pair remains after a failed latest lookup")
}

func TestKnowledgePublicationWorkflowDoesNotRequireGitHubRelease(t *testing.T) {
	query := "知识发布流程是什么？"
	ctx := WithContract(context.Background(), query)
	require.Equal(t, IntentNone, IntentFromContext(ctx))
	require.False(t, Requires(ctx, IntentRelease))
	require.Empty(t, Prompt(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "知识草稿需要审核后发布。"))
	// A later GROUP.md or quoted message is model context, not this turn's
	// question. It cannot introduce a release requirement retroactively.
	again := WithContract(ctx, query+"\nGROUP.md：最新版本请核对发布记录。")
	require.Same(t, ctx, again)
	require.False(t, Requires(again, IntentRelease))
}

func TestCompoundReleaseAndIntegrationRequireIndependentProofs(t *testing.T) {
	ctx := WithContract(context.Background(), "最新版本是否支持新的 IM 接入？")
	require.Equal(t, IntentRelease, IntentFromContext(ctx))
	require.True(t, Requires(ctx, IntentRelease))
	require.True(t, Requires(ctx, IntentIntegration))
	RecordReleaseEvidence(ctx)
	require.True(t, NeedsEvidenceRetry(ctx, "最新版本支持该接入。"), "a release tag is not proof of support")
	RecordDocumentEvidence(ctx)
	require.False(t, NeedsEvidenceRetry(ctx, "最新版本支持该接入。"))
}

func TestWithContractPreservesExistingTurnState(t *testing.T) {
	ctx := WithContract(context.Background(), "octo-android 最新版本更新了什么？")
	RecordReleaseLookup(ctx)

	// A later generic transport wrapper must not replace an ingress-created
	// release contract with IntentNone or discard its recorded lookup state.
	again := WithContract(ctx, "你好")
	require.Equal(t, IntentRelease, IntentFromContext(again))
	require.True(t, ReleaseLookupObserved(again))
	RecordReleaseEvidence(again)
	require.True(t, ReleaseEvidenceObserved(ctx), "both contexts must address the same turn state")
}

func TestWithContractLocksOrdinaryQuestionBeforeModelOnlyContext(t *testing.T) {
	original := "Octo 和 Loop 的关系是什么？"
	// GROUP.md and quoted messages help the model interpret the conversation,
	// but their release/source vocabulary is not part of the user's question.
	for _, tc := range []struct {
		name       string
		modelQuery string
		misIntent  Intent
	}{
		{"release rules", original + "\n\nGROUP.md：版本、发布问题请核对证据。\n引用：最新版是多少？", IntentRelease},
		{"old group rule", original + "\n\nGROUP.md：联系人仅作指引；版本发布问题请核对资料，不主动通知或催办。", IntentRelease},
		{"source rules", original + "\n\nGROUP.md：源码问题请读取文件和行号。", IntentSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithContract(context.Background(), original)
			require.Nil(t, stateFrom(ctx), "ordinary turns must not acquire an evidence ledger")
			require.NotNil(t, ctx.Value(classifiedNoneKey{}), "IntentNone must be recorded as a classified turn")
			require.Equal(t, IntentNone, IntentFromContext(ctx))
			require.Equal(t, tc.misIntent, Classify(tc.modelQuery), "regression requires a genuinely misleading model query")
			again := WithContract(ctx, tc.modelQuery)
			require.Same(t, ctx, again, "a later engine wrapper must retain the ordinary turn marker")
			require.Equal(t, IntentNone, IntentFromContext(again))
			require.Empty(t, Prompt(again))
			require.False(t, ShouldHoldStreamingAnswer(again))
			require.False(t, NeedsEvidenceRetry(again, "Loop 是 Octo 平台的项目协作模块。"))
			require.False(t, NeedsSynthesisFallback(again))
			require.False(t, CanRetryEvidence(again), "ordinary questions must not acquire an evidence nudge budget")
			require.Empty(t, FallbackReply(again))
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
	require.True(t, NeedsEvidenceRetry(ctx, "当前材料无法确认最新版本。"), "an explicit unknown cannot bypass the official latest-release lookup")
	require.True(t, NeedsEvidenceRetry(ctx, "当前材料无法确认最新版本，但最新版本是 1.2.3。"), "an uncertainty prefix cannot excuse a Chinese release conclusion")
	require.True(t, NeedsEvidenceRetry(ctx, "I cannot confirm the latest version, but the current version is 1.2.3."), "an uncertainty prefix cannot excuse an English release conclusion")
	require.True(t, NeedsSynthesisFallback(ctx))
	require.False(t, AllowsMissingIssue(ctx))
	require.Contains(t, Prompt(ctx), "可信发布查询")
	require.Contains(t, FallbackReply(ctx), "可核验的发布记录")

	RecordDocumentEvidence(ctx)
	require.True(t, VerifiedEvidenceObserved(ctx))
	require.False(t, ReleaseEvidenceObserved(ctx), "a document hit cannot establish the latest release")
	require.True(t, ShouldHoldStreamingAnswer(ctx))
	require.True(t, NeedsEvidenceRetry(ctx, "最新版本是 1.2.3。"))

	RecordReleaseLookup(ctx)
	require.True(t, ReleaseLookupObserved(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "当前材料无法确认最新版本。"), "an explicit unknown is safe only after the official latest endpoint ran")

	RecordReleaseEvidence(ctx)
	require.True(t, ReleaseEvidenceObserved(ctx))
	require.False(t, ShouldHoldStreamingAnswer(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "最新版本是 1.2.3。"))
	require.False(t, NeedsSynthesisFallback(ctx))
	require.True(t, AllowsMissingIssue(ctx))
}

func TestIntegrationContractAllowsActualDocumentEvidenceButNotSearchOnly(t *testing.T) {
	ctx := WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	require.Equal(t, IntentIntegration, IntentFromContext(ctx))
	require.True(t, ShouldHoldStreamingAnswer(ctx), "integration content must not stream optimistically")
	require.True(t, NeedsEvidenceRetry(ctx, "Claude 支持原生接入。"))
	require.True(t, NeedsEvidenceRetry(ctx, "Claude 不支持原生接入。"))
	require.True(t, NeedsEvidenceRetry(ctx, "可以。"), "a terse affirmative is still an integration conclusion")
	require.True(t, NeedsEvidenceRetry(ctx, "不可以。"), "a terse negative is still an integration conclusion")
	require.True(t, NeedsEvidenceRetry(ctx, "当前授权资料无法确认是否支持。"), "an explicit unknown cannot bypass source search")
	require.True(t, NeedsEvidenceRetry(ctx, "当前授权资料无法确认是否支持，但 Claude 支持原生接入。"), "an uncertainty prefix cannot excuse a Chinese integration conclusion")
	require.True(t, NeedsEvidenceRetry(ctx, "I cannot confirm support, but Claude supports native Octo IM integration."), "an uncertainty prefix cannot excuse an English integration conclusion")
	require.True(t, NeedsSynthesisFallback(ctx))
	require.False(t, AllowsMissingIssue(ctx))
	require.Contains(t, Prompt(ctx), "无论结论是“支持”还是“不支持”")
	require.Contains(t, FallbackReply(ctx), "源码")

	RecordReleaseEvidence(ctx)
	require.True(t, ReleaseEvidenceObserved(ctx))
	require.True(t, ShouldHoldStreamingAnswer(ctx), "a release tag cannot establish an integration")
	require.True(t, NeedsEvidenceRetry(ctx, "Claude 支持原生接入。"))

	RecordDocumentEvidence(ctx)
	require.True(t, DocumentOrSourceEvidenceObserved(ctx))
	require.True(t, IntegrationEvidenceObserved(ctx))
	require.False(t, ShouldHoldStreamingAnswer(ctx))
	require.False(t, NeedsEvidenceRetry(ctx, "Claude 支持原生接入。"))
	require.False(t, NeedsEvidenceRetry(ctx, "Claude 不支持原生接入。"))
	require.False(t, NeedsSynthesisFallback(ctx))
	require.True(t, AllowsMissingIssue(ctx))

	zeroHitCtx := WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	RecordSourceSearch(zeroHitCtx, true, false)
	require.False(t, IntegrationEvidenceObserved(zeroHitCtx))
	require.False(t, NeedsEvidenceRetry(zeroHitCtx, "当前授权资料无法确认是否支持。"), "only a complete zero-hit source search may support an explicit unknown")
	require.True(t, ShouldHoldStreamingAnswer(zeroHitCtx))
	require.True(t, NeedsSynthesisFallback(zeroHitCtx))
	require.False(t, AllowsMissingIssue(zeroHitCtx), "search-only must not create a knowledge gap")

	incompleteCtx := WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	RecordSourceSearch(incompleteCtx, false, false)
	require.True(t, NeedsEvidenceRetry(incompleteCtx, "当前授权资料无法确认是否支持。"), "a capped or timed-out search is not exhaustive")
}

func TestEvidenceRetryIsBounded(t *testing.T) {
	ctx := WithContract(context.Background(), "这个函数怎么实现？")
	require.True(t, CanRetryEvidence(ctx))
	require.False(t, CanRetryEvidence(ctx))
}

func TestNamedChannelProjectsNeedIndividualSourceReads(t *testing.T) {
	ctx := WithContract(context.Background(), "codex-channel-octo、cc-channel-octo 和 hermes-channel-octo 项目是干嘛的？")
	require.Equal(t, []string{"cc-channel-octo", "codex-channel-octo", "hermes-channel-octo"}, RequiredRepositories(ctx))
	RecordSourceSearch(ctx, true, true, "Mininglamp-OSS/codex-channel-octo")
	RecordSourceRead(ctx, "Mininglamp-OSS/codex-channel-octo")
	RecordSourceSearch(ctx, false, true, "Mininglamp-OSS/cc-channel-octo")
	RecordSourceRead(ctx, "Mininglamp-OSS/cc-channel-octo")
	require.False(t, IntegrationEvidenceObserved(ctx))
	require.Equal(t, []string{"hermes-channel-octo"}, MissingRequiredRepositories(ctx))
	require.True(t, NeedsEvidenceRetry(ctx, "当前授权资料无法确认是否支持。"), "a single unread named project cannot be hidden behind an uncertainty answer")

	RecordSourceSearch(ctx, false, true, "Mininglamp-OSS/hermes-channel-octo")
	RecordSourceRead(ctx, "Mininglamp-OSS/hermes-channel-octo")
	require.True(t, IntegrationEvidenceObserved(ctx))
	require.Empty(t, MissingRequiredRepositories(ctx))
}

func TestPostPreflightSourceBrowseBudgetOnlyConstrainsCompletedNamedChannelEvidence(t *testing.T) {
	ordinary := WithContract(context.Background(), "这个函数怎么实现？")
	require.True(t, ConsumePostPreflightSourceBrowseBudget(ordinary), "ordinary source questions keep unrestricted reads")
	ordinaryActive, _ := PostPreflightSourceBrowseBudget(ordinary)
	require.False(t, ordinaryActive)

	ctx := WithContract(context.Background(), "codex-channel-octo 和 cc-channel-octo 项目是干嘛的？")
	RecordSourceSearch(ctx, true, true, "Mininglamp-OSS/codex-channel-octo")
	RecordSourceRead(ctx, "Mininglamp-OSS/codex-channel-octo")
	RecordSourceSearch(ctx, true, true, "Mininglamp-OSS/cc-channel-octo")
	RecordSourceRead(ctx, "Mininglamp-OSS/cc-channel-octo")
	require.True(t, IntegrationEvidenceObserved(ctx))

	ActivatePostPreflightSourceBrowseBudget(ctx, 2)
	active, remaining := PostPreflightSourceBrowseBudget(ctx)
	require.True(t, active)
	require.Equal(t, 2, remaining)
	require.True(t, ConsumePostPreflightSourceBrowseBudget(ctx))
	require.True(t, ConsumePostPreflightSourceBrowseBudget(ctx))
	require.False(t, ConsumePostPreflightSourceBrowseBudget(ctx))
	_, remaining = PostPreflightSourceBrowseBudget(ctx)
	require.Zero(t, remaining)

	// The activation point is idempotent: a later transport must not silently
	// replenish a budget that the same model turn has already exhausted.
	ActivatePostPreflightSourceBrowseBudget(ctx, 9)
	_, remaining = PostPreflightSourceBrowseBudget(ctx)
	require.Zero(t, remaining)
}

func TestIntegrationCanUseTwoBoundedEvidenceNudges(t *testing.T) {
	ctx := WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	require.True(t, CanRetryEvidence(ctx))
	require.True(t, CanRetryEvidence(ctx), "search and read are separate tool rounds")
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
