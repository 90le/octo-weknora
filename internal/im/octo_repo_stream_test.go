package im

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestOctoMultiRepositoryIntermediateKeepsOnlyToolProgress(t *testing.T) {
	ctx := answerevidence.WithContract(octoCitationContext(), "codex-channel-octo 与 cc-channel-octo 各自怎样保存会话？")
	require.True(t, answerevidence.ShouldHoldStreamingAnswer(ctx))
	parts := IMStreamParts{
		Mode:           IMStreamModeAgent,
		AgentInner:     "尚未验证的模型思考和早期断言",
		LiveAnswer:     "cc 项目误引了 codex 来源",
		Answer:         "未审核的完整答案",
		AgentToolSteps: []IMToolStep{{ToolName: "source_browse", Pending: true}},
	}
	octoHoldUnverifiedIntermediate(ctx, true, &parts)
	require.Empty(t, parts.AgentInner)
	require.Empty(t, parts.LiveAnswer)
	require.Empty(t, parts.Answer)
	rendered := FormatIMIntermediateFromParts(parts, true)
	require.NotContains(t, rendered, "误引")
	require.NotContains(t, rendered, "模型思考")
	require.NotContains(t, rendered, "未审核")
}

func TestOctoIntermediateSingleRepositoryAndNonOctoRemainLive(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		agent bool
	}{
		{"single Octo repo", answerevidence.WithContract(octoCitationContext(), "cc-channel-octo 源码如何实现？"), true},
		{"non Octo multi repo", answerevidence.WithContract(context.Background(), "codex-channel-octo 与 cc-channel-octo 如何实现？"), true},
		{"Octo quick QA", answerevidence.WithContract(octoCitationContext(), "codex-channel-octo 与 cc-channel-octo 如何实现？"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "single Octo repo" {
				answerevidence.RecordSourceSearch(tc.ctx, true, true, "Mininglamp-OSS/cc-channel-octo")
				answerevidence.RecordSourceRead(tc.ctx, "Mininglamp-OSS/cc-channel-octo")
			}
			parts := IMStreamParts{Mode: IMStreamModeAgent, AgentInner: "思考", LiveAnswer: "答复", Answer: "最终"}
			octoHoldUnverifiedIntermediate(tc.ctx, tc.agent, &parts)
			require.Equal(t, "思考", parts.AgentInner)
			require.Equal(t, "答复", parts.LiveAnswer)
			require.Equal(t, "最终", parts.Answer)
		})
	}
}

func TestOctoRevokedPrincipalAndRepositoryFallbackHaveNoTailSource(t *testing.T) {
	p := octobusiness.Principal{TenantID: 1, AccountID: "bot", ChannelID: "group", ScopeID: "scope", UserID: "native", KnowledgeBaseIDs: []string{"kb"}}
	p.Validate = func(context.Context) (octobusiness.Principal, error) {
		return octobusiness.Principal{}, errors.New("revoked")
	}
	revoked := octobusiness.WithPrincipal(context.Background(), p)
	require.True(t, octoRepositoryAlignmentMismatch(revoked, "任何答案"))
	require.Equal(t, imRepositoryMismatchFallback, (&Service{}).appendOctoSources(revoked, "任何答案", nil))
	normal := octobusiness.WithRetrievalTrace(octoCitationContext())
	// A verified official release must not be appended to an attribution fallback.
	url := "https://github.com/example/repo/releases/tag/v1"
	_, err := octobusiness.TrackRetrieval(&octoCitationReleaseTool{data: map[string]interface{}{
		types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{
			KnowledgeBaseID: "kb", DataSourceID: "source", Repository: "example/repo",
			TagName: "v1", URL: url, PublishedAt: time.Now().UTC(), CheckedAt: time.Now().UTC(),
		},
	}}).Execute(normal, []byte("{\"action\":\"latest\",\"release_ref\":\"r1\"}"))
	require.NoError(t, err)
	visible := (&Service{}).appendOctoSources(normal, strings.TrimSpace(imRepositoryMismatchFallback), nil)
	require.Equal(t, imRepositoryMismatchFallback, visible)
	require.NotContains(t, visible, url)
}
