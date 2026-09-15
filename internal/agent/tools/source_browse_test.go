package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type sourceToolKB struct {
	interfaces.KnowledgeBaseService
}

func (*sourceToolKB) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return &types.KnowledgeBase{ID: id, TenantID: 7}, nil
}

type sourceToolReader struct {
	interfaces.SourceSnapshotReader
	called bool
}

func (r *sourceToolReader) ListSourceSnapshots(context.Context, string) ([]types.SourceSummary, error) {
	r.called = true
	return []types.SourceSummary{{ID: "source", Name: "repo"}}, nil
}
func TestSourceToolNeverWidensFileOrTagScope(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "fixture")
	for _, target := range []*types.SearchTarget{{Type: types.SearchTargetTypeKnowledge, TenantID: 7, KnowledgeBaseID: "kb", KnowledgeIDs: []string{"file"}}, {Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb", TagIDs: []string{"tag"}}} {
		r := &sourceToolReader{}
		tool := NewSourceBrowseTool(r, &sourceToolKB{}, types.SearchTargets{target})
		result, err := tool.Execute(ctx, json.RawMessage(`{"action":"list","knowledge_base_id":"kb"}`))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.False(t, r.called)
	}
	r := &sourceToolReader{}
	tool := NewSourceBrowseTool(r, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	result, err := tool.Execute(ctx, json.RawMessage(`{"action":"list","knowledge_base_id":"other"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.False(t, r.called)
	result, err = tool.Execute(ctx, json.RawMessage(`{"action":"list","knowledge_base_id":"kb"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, r.called)
}
