package octointegration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadBindingAndManagementGrantHaveIndependentLifecycles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := createScope(t, s, 1, "bot", "group", "", false)
	child := createScope(t, s, 1, "bot", "group", "child", true)
	require.NoError(t, s.SetBinding(ctx, 1, scope.ID, "kb-a", true, true))
	managed, err := s.ManagedKnowledgeBases(ctx, 1, scope.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"kb-a"}, managed)
	inherited, err := s.Effective(ctx, 1, child.ID)
	require.NoError(t, err)
	require.Len(t, inherited, 1)
	require.False(t, inherited[0].CanManage, "read inheritance never copies management")
	require.NoError(t, s.SetBinding(ctx, 1, scope.ID, "kb-a", false))
	effective, err := s.Effective(ctx, 1, scope.ID)
	require.NoError(t, err)
	require.Empty(t, effective)
	managed, err = s.ManagedKnowledgeBases(ctx, 1, scope.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"kb-a"}, managed, "unbind preserves explicit asset delegation")
	require.NoError(t, s.SetBinding(ctx, 1, scope.ID, "kb-a", true))
	effective, err = s.Effective(ctx, 1, scope.ID)
	require.NoError(t, err)
	require.True(t, effective[0].CanManage)
	require.NoError(t, s.RevokeKnowledgeManagement(ctx, 1, scope.ID, "kb-a"))
	managed, err = s.ManagedKnowledgeBases(ctx, 1, scope.ID)
	require.NoError(t, err)
	require.Empty(t, managed)
	effective, err = s.Effective(ctx, 1, scope.ID)
	require.NoError(t, err)
	require.Len(t, effective, 1)
	require.False(t, effective[0].CanManage, "revoking management leaves ordinary knowledge reading enabled")
}
func TestExplicitReadOnlyBindingRevokesManagementAndDeletedAssetsHide(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := createScope(t, s, 1, "bot", "group", "", false)
	require.NoError(t, s.SetBinding(ctx, 1, scope.ID, "kb-a", true, true))
	require.NoError(t, s.SetBinding(ctx, 1, scope.ID, "kb-a", true, false))
	ids, err := s.ManagedKnowledgeBases(ctx, 1, scope.ID)
	require.NoError(t, err)
	require.Empty(t, ids)
	require.NoError(t, s.SetBinding(ctx, 1, scope.ID, "kb-a", true, true))
	require.NoError(t, s.db.Exec("UPDATE knowledge_bases SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'kb-a'").Error)
	ids, err = s.ManagedKnowledgeBases(ctx, 1, scope.ID)
	require.NoError(t, err)
	require.Empty(t, ids)
	_, err = s.ManagedKnowledgeBases(ctx, 2, scope.ID)
	require.Error(t, err)
}
