package im

import (
	"context"
	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOctoCrossRepositoryCitationMismatchUsesExplicitSectionOwnership(t *testing.T) {
	sha := strings.Repeat("a", 40)
	cc := "https://github.com/Mininglamp-OSS/cc-channel-octo/blob/" + sha + "/src/index.ts#L740-L748"
	codex := "https://github.com/Mininglamp-OSS/codex-channel-octo/blob/" + sha + "/src/index.ts#L615-L625"
	trusted := map[string]string{cc: "Mininglamp-OSS/cc-channel-octo", codex: "Mininglamp-OSS/codex-channel-octo"}
	for _, tc := range []struct {
		name, answer string
		mismatch     bool
	}{
		{"wrong section", "## 1. cc-channel-octo\n恢复入口。\n\n[来源](<" + codex + ">)", true},
		{"adjacent bullets keep separate ownership", "- cc-channel-octo 恢复。<web url=\"" + codex + "\" />\n- codex-channel-octo 恢复。<web url=\"" + codex + "\" />", true},
		{"adjacent matching bullets remain valid", "- cc-channel-octo 保存。<web url=\"" + cc + "\" />\n- codex-channel-octo 恢复。<web url=\"" + codex + "\" />", false},
		{"table row ownership is separate", "| 仓库 | 说明 |\n|---|---|\n| cc-channel-octo | [来源](<" + codex + ">) |\n| codex-channel-octo | [来源](<" + codex + ">) |", true},
		{"table without leading pipe still separates rows", "仓库 | 说明\n--- | ---\ncc-channel-octo | [来源](<" + codex + ">)\ncodex-channel-octo | [来源](<" + codex + ">)", true},
		{"correct table rows are kept", "仓库 | 说明\n--- | ---\ncc-channel-octo | [来源](<" + cc + ">)\ncodex-channel-octo | [来源](<" + codex + ">)", false},
		{"repository-root link label counts as list owner", "- [cc-channel-octo](<https://github.com/Mininglamp-OSS/cc-channel-octo>) 恢复。<web url=\"" + codex + "\" />", true},
		{"repository-root link label with correct source passes", "- [cc-channel-octo](<https://github.com/Mininglamp-OSS/cc-channel-octo>) 恢复。<web url=\"" + cc + "\" />", false},
		{"nested heading inherits", "## cc-channel-octo\n### 保存与恢复\n结论。<web url=\"" + codex + "\" />", true},
		{"matching section", "## cc-channel-octo\n恢复入口。<web url=\"" + cc + "\" />", false},
		{"new sibling section resets", "## cc-channel-octo\n正确。<web url=\"" + cc + "\" />\n\n## codex-channel-octo\n正确。<web url=\"" + codex + "\" />", false},
		{"explicit paragraph overrides section", "## cc-channel-octo\ncodex-channel-octo 的恢复是这样。<web url=\"" + codex + "\" />", false},
		{"two repos in comparison are ambiguous", "## 两仓对比\ncc-channel-octo 与 codex-channel-octo 各有实现。<web url=\"" + codex + "\" />", false},
		{"source footer resets ownership", "## cc-channel-octo\n概述。\n\n来源：\n- [codex 文件](<" + codex + ">)", false},
		{"markdown source heading resets ownership", "## cc-channel-octo\n概述。\n\n### 来源\n- [codex 文件](<" + codex + ">)", false},
		{"markdown references heading resets ownership", "## cc-channel-octo\n概述。\n\n## References\n- [codex file](<" + codex + ">)", false},
		{"linked heading keeps visible repository", "## [cc-channel-octo](<https://github.com/Mininglamp-OSS/cc-channel-octo>)\n恢复入口。<web url=\"" + codex + "\" />", true},
		{"standalone bold heading persists across blank line", "**cc-channel-octo**\n\n恢复入口。<web url=\"" + codex + "\" />", true},
		{"standalone colon heading persists across blank line", "cc-channel-octo：\n\n恢复入口。<web url=\"" + codex + "\" />", true},
		{"plain list item does not set section ownership", "- cc-channel-octo\n\n恢复入口。<web url=\"" + codex + "\" />", false},
		{"reference-style code link fails closed", "## cc-channel-octo\n恢复入口。[来源][ref]\n\n[ref]: <" + codex + ">", true},
		{"reference-style blob without line fails closed", "## cc-channel-octo\n恢复入口。[来源][ref]\n\n[ref]: <" + strings.Split(codex, "#")[0] + ">", true},
		{"no named repo is unclassified", "该实现会保存会话。<web url=\"" + codex + "\" />", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.mismatch, octoCrossRepositoryCitationMismatch(tc.answer, trusted))
		})
	}
}

func TestOctoCrossRepositoryCitationMismatchUsesRequestedRepositoryWithoutSecondNarrowRead(t *testing.T) {
	sha := strings.Repeat("e", 40)
	codex := "https://github.com/Mininglamp-OSS/codex-channel-octo/blob/" + sha + "/src/index.ts#L615-L625"
	trusted := map[string]string{codex: "Mininglamp-OSS/codex-channel-octo"}
	answer := "## cc-channel-octo\n恢复入口。<web url=\"" + codex + "\" />"
	require.False(t, octoCrossRepositoryCitationMismatch(answer, trusted), "one observed repository alone does not infer an unobserved target")
	require.True(t, octoCrossRepositoryCitationMismatch(answer, trusted, "cc-channel-octo", "codex-channel-octo"),
		"the user's second named project provides a conservative section target")
	require.True(t, octoCrossRepositoryCitationMismatch(answer, trusted, "Mininglamp-OSS/cc-channel-octo"),
		"a full requested identity should also be recognized")
	require.False(t, octoCrossRepositoryCitationMismatch("## cc-channel-octo\n未找到可引用片段。", trusted, "cc-channel-octo"),
		"the ownership guard does not invent a citation requirement")
}

func TestOctoRepositoryMainWorkerProseDoesNotOverrideExplicitSection(t *testing.T) {
	sha := strings.Repeat("f", 40)
	main := "https://github.com/Example/main/blob/" + sha + "/src/main.go#L1-L3"
	worker := "https://github.com/Example/worker/blob/" + sha + "/src/worker.go#L1-L3"
	trusted := map[string]string{main: "Example/main", worker: "Example/worker"}
	answer := "## Example/main\nThe main worker flow runs here. <web url=\"" + worker + "\" />"
	require.True(t, octoCrossRepositoryCitationMismatch(answer, trusted), "main/worker in ordinary prose are not repository headings")
}

func TestOctoCrossRepositoryCitationMismatchRequiresTrustedShortSubset(t *testing.T) {
	sha := strings.Repeat("b", 40)
	cc := "https://github.com/Mininglamp-OSS/cc-channel-octo/blob/" + sha + "/src/index.ts#L740-L748"
	codex := "https://github.com/Mininglamp-OSS/codex-channel-octo/blob/" + sha + "/src/index.ts#L610-L621"
	subset := "https://github.com/Mininglamp-OSS/codex-channel-octo/blob/" + sha + "/src/index.ts#L615-L620"
	trusted := map[string]string{cc: "Mininglamp-OSS/cc-channel-octo", codex: "Mininglamp-OSS/codex-channel-octo"}
	require.True(t, octoCrossRepositoryCitationMismatch("## cc-channel-octo\n[来源](<"+subset+">)", trusted))
	require.False(t, octoCrossRepositoryCitationMismatch("## cc-channel-octo\n[来源](<"+subset+">)", map[string]string{cc: "Mininglamp-OSS/cc-channel-octo"}))
	require.False(t, octoCrossRepositoryCitationMismatch("## cc-channel-octo\n[来源](<"+subset+">)",
		map[string]string{cc: "Mininglamp-OSS/cc-channel-octo", codex: "WrongOwner/codex-channel-octo"}))
}

func TestOctoCrossRepositoryCitationMismatchDoesNotGuessAmbiguousLeaf(t *testing.T) {
	sha := strings.Repeat("c", 40)
	a := "https://github.com/OrgA/project/blob/" + sha + "/a.go#L1-L3"
	b := "https://github.com/OrgB/project/blob/" + sha + "/b.go#L1-L3"
	trusted := map[string]string{a: "OrgA/project", b: "OrgB/project"}
	require.False(t, octoCrossRepositoryCitationMismatch("## project\n详情。<web url=\""+b+"\" />", trusted))
	require.True(t, octoCrossRepositoryCitationMismatch("## OrgA/project\n详情。<web url=\""+b+"\" />", trusted))
}

func TestOctoRepositoryAlignmentFallbackUsesCurrentTurnReadOwnership(t *testing.T) {
	sha := strings.Repeat("d", 40)
	cc := "https://github.com/Mininglamp-OSS/cc-channel-octo/blob/" + sha + "/src/index.ts#L740-L748"
	codex := "https://github.com/Mininglamp-OSS/codex-channel-octo/blob/" + sha + "/src/index.ts#L615-L625"
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	for _, marker := range []types.SourceBrowseCitation{
		{KnowledgeBaseID: "kb", Repository: "Mininglamp-OSS/cc-channel-octo", URL: cc, Path: "src/index.ts", Revision: sha, Citable: true},
		{KnowledgeBaseID: "kb", Repository: "Mininglamp-OSS/codex-channel-octo", URL: codex, Path: "src/index.ts", Revision: sha, Citable: true},
	} {
		_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{data: map[string]interface{}{
			types.SourceBrowseCitationDataKey: marker,
		}}).Execute(ctx, []byte("{\"action\":\"read\",\"source_ref\":\"s1\"}"))
		require.NoError(t, err)
	}
	wrong := "## cc-channel-octo\n恢复入口。<web url=\"" + codex + "\" />"
	require.True(t, octoRepositoryAlignmentMismatch(ctx, wrong))
	assistant := &types.Message{KnowledgeReferences: types.References{&types.SearchResult{ID: "old-ref"}}}
	require.Equal(t, imRepositoryMismatchFallback, octoGuardStoredAnswer(ctx, wrong, assistant))
	require.True(t, assistant.IsFallback)
	require.Nil(t, assistant.KnowledgeReferences)
	service := &Service{}
	require.Equal(t, imRepositoryMismatchFallback, service.appendOctoSources(ctx, wrong, nil))
	require.Equal(t, imRepositoryMismatchFallback, strings.TrimSpace(service.formatIMStreamFinal(ctx, IMStreamParts{Mode: IMStreamModeAgent, Answer: wrong}, nil, nil)))
	good := "## cc-channel-octo\n保存。<web url=\"" + cc + "\" />\n\n## codex-channel-octo\n恢复。<web url=\"" + codex + "\" />"
	require.False(t, octoRepositoryAlignmentMismatch(ctx, good))
	visible := service.appendOctoSources(ctx, good, nil)
	require.Contains(t, visible, cc)
	require.Contains(t, visible, codex)
	require.NotContains(t, visible, imRepositoryMismatchFallback)
	// Disabling citation display does not authorize a known wrong repository claim.
	octobusiness.SetCitationRenderingEnabled(ctx, false)
	require.Equal(t, imRepositoryMismatchFallback, service.appendOctoSources(ctx, wrong, nil))
}

func TestOctoRepositoryAlignmentUsesAuthorizedBroadReadForExpectedOwnership(t *testing.T) {
	sha := strings.Repeat("1", 40)
	ccWide := "https://github.com/Mininglamp-OSS/cc-channel-octo/blob/" + sha + "/src/index.ts#L700-L739"
	codexNarrow := "https://github.com/Mininglamp-OSS/codex-channel-octo/blob/" + sha + "/src/index.ts#L615-L625"
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	for _, marker := range []types.SourceBrowseCitation{
		{KnowledgeBaseID: "kb", Repository: "Mininglamp-OSS/cc-channel-octo", URL: ccWide, Path: "src/index.ts", Revision: sha, Citable: false},
		{KnowledgeBaseID: "kb", Repository: "Mininglamp-OSS/codex-channel-octo", URL: codexNarrow, Path: "src/index.ts", Revision: sha, Citable: true},
	} {
		_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{data: map[string]interface{}{
			types.SourceBrowseCitationDataKey: marker,
		}}).Execute(ctx, []byte(`{"action":"read","source_ref":"s1"}`))
		require.NoError(t, err)
	}
	wrong := "## cc-channel-octo\n恢复入口。<web url=\"" + codexNarrow + "\" />"
	require.True(t, octoRepositoryAlignmentMismatch(ctx, wrong), "authorized broad read identifies cc even without a citable cc handle")
	require.False(t, octoRepositoryAlignmentMismatch(ctx, "## cc-channel-octo\n当前资料不足，无法确认。"))
	// Outside Octo, no principal means this guard never rewrites general Agent answers.
	require.False(t, octoRepositoryAlignmentMismatch(context.Background(), wrong))
}

func TestOctoNamedRepositoryWithoutItsOwnNarrowReadStillRejectsWrongCitation(t *testing.T) {
	sha := strings.Repeat("e", 40)
	codex := "https://github.com/Mininglamp-OSS/codex-channel-octo/blob/" + sha + "/src/index.ts#L615-L625"
	ctx := answerevidence.WithContract(octobusiness.WithRetrievalTrace(octoCitationContext()),
		"codex-channel-octo 与 cc-channel-octo 的源码有何不同？")
	_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{data: map[string]interface{}{
		types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{
			KnowledgeBaseID: "kb", Repository: "Mininglamp-OSS/codex-channel-octo",
			URL: codex, Path: "src/index.ts", Revision: sha, Citable: true,
		},
	}}).Execute(ctx, []byte("{\"action\":\"read\",\"source_ref\":\"s1\"}"))
	require.NoError(t, err)
	answer := "## cc-channel-octo\n恢复入口。<web url=\"" + codex + "\" />"
	require.True(t, octoRepositoryAlignmentMismatch(ctx, answer))
}
