package octobusiness

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type qualityWikiTool struct {
	fakeRetrieval
	result *types.ToolResult
}

func (*qualityWikiTool) Name() string { return "wiki_read_page" }

func (t *qualityWikiTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return t.result, nil
}

func TestQualityTurnTracksReadPagesWithoutRoutingNeighbors(t *testing.T) {
	ctx := WithRetrievalTrace(context.Background())
	tool := &qualityWikiTool{result: &types.ToolResult{
		Success: true,
		Data: map[string]any{
			"found_kbs": map[string][]string{"guide": {"kb-a"}, "neighbor": {"kb-a"}},
			"read_kbs":  map[string][]string{"guide": {"kb-a"}},
		},
	}}
	for range 2 {
		_, err := TrackRetrieval(tool).Execute(ctx, nil)
		require.NoError(t, err)
	}
	RecordQualityTurn(ctx, nil, false)
	var turn QualityTurn
	require.NoError(t, json.Unmarshal([]byte(EncodedQualityTurn(ctx)), &turn))
	require.Equal(t, []QualitySource{{Library: "kb-a", WikiSlug: "guide"}}, turn.Sources)
}

func TestQualityInboxRequiresExactChannelAuthorityAndLibraries(t *testing.T) {
	scope := &qualityScope{Scope: &octointegration.Scope{
		ID: "scope", GroupID: "group", SubareaID: "sub", AccountID: "bot",
	}, Libraries: []qualityLibrary{{ID: "kb"}}}
	entry := qualityInbox{
		MessageID: "msg", State: "delivered", Reply: "现有资料没有明确归档期限。",
		Input: `{"MessageType":"text","ChatType":"group","UserID":"user","Content":"归档期限？",` +
			`"Extra":{"octo_group_id":"group","octo_subarea_id":"sub"}}`,
		Authority: `{"AccountID":"bot","ScopeID":"scope","KnowledgeBaseIDs":["kb"]}`,
	}
	row, ok := entry.row(scope)
	require.True(t, ok)
	require.Equal(t, "suspected_gap", row.Outcome)
	entry.State = "failed"
	row, ok = entry.row(scope)
	require.True(t, ok)
	require.Empty(t, row.Answer)
	require.Equal(t, "failed", row.Outcome)
	scope.SubareaID = "other"
	_, ok = entry.row(scope)
	require.False(t, ok)
	scope.SubareaID = "sub"
	scope.Libraries = nil
	_, ok = entry.row(scope)
	require.False(t, ok)
}

func TestQualityReviewKeepsVersionSeparatorsAndRejectsInventedQuotes(t *testing.T) {
	require.Equal(t, qualityQuestionKey("GEO  v1.2？"), qualityQuestionKey("geo v1.2!"))
	require.NotEqual(t, qualityQuestionKey("GEO v1.2？"), qualityQuestionKey("GEO v12？"))
	excerpts := []qualityExcerpt{{ID: "A1", Text: "现有资料没有明确归档期限。"}}
	finding := qualityFinding{
		Category: "missing", Title: "归档期限", Reason: "资料缺口", Suggestion: "请明确归档期限",
		Evidence: []qualityQuote{{Ref: "A1", Quote: "没有明确归档期限"}},
	}
	require.True(t, validQualityFinding(finding, excerpts))
	finding.Evidence[0].Quote = "保留三年"
	require.False(t, validQualityFinding(finding, excerpts))
	finding.Evidence[0] = qualityQuote{Ref: "S1", Quote: "没有明确归档期限"}
	require.False(t, validQualityFinding(finding, excerpts))
}
