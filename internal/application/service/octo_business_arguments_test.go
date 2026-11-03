package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestOctoToolCreationPreviewNeedsScopeOptInNotTemplateManagement(t *testing.T) {
	service, store, db, ctx, principal := octoManagementFixture(t)
	require.NoError(t, db.AutoMigrate(&octobusiness.Proposal{}))
	require.NoError(t, store.RevokeKnowledgeManagement(ctx, principal.TenantID, principal.ScopeID, "kb"))
	principal.ManageKnowledgeBaseIDs = nil
	principal.MessageID = "create-request"
	principal.MessageText = "请创建新产品知识库，先给我预览。"
	principal.Validate = func(context.Context) (octobusiness.Principal, error) { return principal, nil }
	ctx = octobusiness.WithPrincipal(ctx, principal)
	tool := octobusiness.NewTool(service)
	payload := json.RawMessage(`{"operation":"propose","action":"create_kb","name":"新产品知识库"}`)
	denied, err := tool.Execute(ctx, payload)
	require.NoError(t, err)
	require.False(t, denied.Success)
	require.Equal(t, octobusiness.ErrDenied.Error(), denied.Error, "argument validation does not grant creation rights")
	require.NoError(t, db.Model(&octointegration.Scope{}).Where("id = ?", principal.ScopeID).Update("allow_knowledge_creation", true).Error)
	result, err := tool.Execute(ctx, payload)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var proposal octobusiness.Proposal
	require.NoError(t, json.Unmarshal([]byte(result.Output), &proposal))
	require.Equal(t, "create_kb", proposal.Action)
	require.Equal(t, "pending", proposal.Status)
	var preview map[string]any
	require.NoError(t, json.Unmarshal(proposal.Result, &preview))
	require.Equal(t, "Product", preview["template_knowledge_base"])
	require.Equal(t, "新产品知识库", preview["name"])
	var knowledgeBases int64
	require.NoError(t, db.Table("knowledge_bases").Count(&knowledgeBases).Error)
	require.Equal(t, int64(1), knowledgeBases, "preview must not create the actual knowledge base")
}

func TestOctoFocusedPreviewCorrectsInventedScopeWithoutCreatingKB(t *testing.T) {
	service, _, db, ctx, principal := octoManagementFixture(t)
	require.NoError(t, db.AutoMigrate(&octobusiness.Proposal{}))
	require.NoError(t, db.Model(&octointegration.Scope{}).Where("id = ?", principal.ScopeID).
		Update("allow_knowledge_creation", true).Error)
	principal.CanCreateKnowledgeBase = true
	principal.MessageID = "synthetic-preview"
	principal.Validate = func(context.Context) (octobusiness.Principal, error) { return principal, nil }
	ctx = octobusiness.WithPrincipal(ctx, principal)
	var focused types.Tool
	for _, candidate := range octobusiness.NewTools(service) {
		if candidate.Name() == octobusiness.ToolKnowledgePreview {
			focused = candidate
		}
	}
	require.NotNil(t, focused)
	bad, err := focused.Execute(ctx, []byte(`{"action":"create_kb","name":"synthetic","scope_id":"invented"}`))
	require.NoError(t, err)
	require.False(t, bad.Success)
	require.Equal(t, "invalid_arguments", bad.Data["error_code"])
	require.Contains(t, bad.Error, "Remove scope_id")
	good, err := focused.Execute(ctx, []byte(`{"action":"create_kb","name":"synthetic"}`))
	require.NoError(t, err)
	require.True(t, good.Success, good.Error)
	var proposal octobusiness.Proposal
	require.NoError(t, json.Unmarshal([]byte(good.Output), &proposal))
	require.Equal(t, "pending", proposal.Status)
	var saved octobusiness.Proposal
	require.NoError(t, db.First(&saved, "id = ?", proposal.ID).Error)
	require.Equal(t, principal.ScopeID, saved.ScopeID)
	var knowledgeBases int64
	require.NoError(t, db.Table("knowledge_bases").Count(&knowledgeBases).Error)
	require.EqualValues(t, 1, knowledgeBases, "preview must not create a knowledge base")
}
