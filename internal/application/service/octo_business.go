package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NewOctoBusiness reuses the native knowledge services. No HTTP loopback,
// secondary credentials, or another Agent/runtime is involved.
func NewOctoBusiness(db *gorm.DB, kbs interfaces.KnowledgeBaseService, knowledge interfaces.KnowledgeService) *octobusiness.Service {
	s := octobusiness.NewService(db, kbs, knowledge)
	s.Management = func(ctx context.Context, p octobusiness.Principal, in octobusiness.ManagementInput, preview bool) (map[string]any, error) {
		if !p.CanManageScope || p.IsDirect || p.ScopeID == "" || (in.ScopeID != "" && in.ScopeID != p.ScopeID) {
			return nil, octobusiness.ErrDenied
		}
		store := octointegration.NewStore(db)
		scope, err := store.Get(ctx, p.TenantID, p.ScopeID)
		if err != nil {
			return nil, err
		}
		if scope.AccountID != p.AccountID || scope.GroupID != p.GroupID || scope.SubareaID != p.SubareaID {
			return nil, octobusiness.ErrDenied
		}
		canManage := false
		currentManaged, err := store.ManagedKnowledgeBases(ctx, p.TenantID, p.ScopeID)
		if err != nil {
			return nil, err
		}
		for _, id := range p.ManageKnowledgeBaseIDs {
			if id == in.KnowledgeBaseID {
				for _, currentID := range currentManaged {
					if currentID == id {
						canManage = true
					}
				}
			}
		}
		if in.Action == "create_kb" {
			if !scope.AllowKnowledgeCreation || strings.TrimSpace(in.Name) == "" || len(in.Name) > 300 {
				return nil, octobusiness.ErrDenied
			}
			// A visible KB supplies existing model/parser defaults. No model,
			// credential, or storage path can be supplied by a chat argument.
			templateID := in.KnowledgeBaseID
			if templateID == "" && len(p.KnowledgeBaseIDs) > 0 {
				templateID = p.KnowledgeBaseIDs[0]
			}
			visible := false
			for _, id := range p.KnowledgeBaseIDs {
				if id == templateID {
					visible = true
				}
			}
			if !visible {
				return nil, errors.New("请先在页面完成知识库默认模型配置并绑定一个可用知识库")
			}
			var template types.KnowledgeBase
			if err = db.WithContext(ctx).Where("tenant_id = ? AND id = ?", p.TenantID, templateID).First(&template).Error; err != nil {
				return nil, err
			}
			revision := octoManagementRevision(scope, template.ID, template.UpdatedAt)
			if in.ExpectedRevision != "" && in.ExpectedRevision != revision {
				return nil, octobusiness.ErrConflict
			}
			if preview {
				return map[string]any{"action": in.Action, "name": in.Name, "scope": scope.DisplayName, "template_knowledge_base": template.Name, "revision": revision, "notice": "创建空文档知识库并只绑定当前区域；不复制原资料，不发布内容。"}, nil
			}
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.MessageID+":"+p.ScopeID+":"+in.Name)).String()
			created := &types.KnowledgeBase{ID: id, Name: strings.TrimSpace(in.Name), Type: types.KnowledgeBaseTypeDocument, EmbeddingModelID: template.EmbeddingModelID, SummaryModelID: template.SummaryModelID, ChunkingConfig: template.ChunkingConfig, StorageProviderConfig: template.StorageProviderConfig, StorageBackendID: template.StorageBackendID, VectorStoreID: template.VectorStoreID}
			created, err = kbs.CreateKnowledgeBase(ctx, created)
			if err != nil {
				return nil, err
			}
			if err = store.SetBinding(ctx, p.TenantID, p.ScopeID, created.ID, true, true); err != nil {
				return nil, err
			}
			return map[string]any{"knowledge_base_id": created.ID, "name": created.Name, "bound_scope_id": p.ScopeID}, nil
		}
		if !canManage {
			return nil, octobusiness.ErrDenied
		}
		var kb types.KnowledgeBase
		if err = db.WithContext(ctx).Where("tenant_id = ? AND id = ?", p.TenantID, in.KnowledgeBaseID).First(&kb).Error; err != nil {
			return nil, err
		}
		uses, err := store.Uses(ctx, p.TenantID, kb.ID)
		if err != nil {
			return nil, err
		}
		var inheritedScopes []struct {
			ID        string
			UpdatedAt time.Time
		}
		if scope.SubareaID == "" {
			if err = db.WithContext(ctx).Model(&octointegration.Scope{}).Select("id, updated_at").Where("tenant_id = ? AND account_id = ? AND group_id = ? AND inherit_parent = ?", p.TenantID, p.AccountID, p.GroupID, true).Order("id").Find(&inheritedScopes).Error; err != nil {
				return nil, err
			}
		}
		revision := octoManagementRevision(scope, kb.ID, kb.UpdatedAt, uses, currentManaged, inheritedScopes)
		if in.ExpectedRevision != "" && in.ExpectedRevision != revision {
			return nil, octobusiness.ErrConflict
		}
		if in.Action == "bind" || in.Action == "unbind" {
			if preview {
				return map[string]any{"action": in.Action, "scope": scope.DisplayName, "knowledge_base_id": kb.ID, "knowledge_base_name": kb.Name, "revision": revision, "notice": "只改变当前区域的使用关系，不删除知识库或原始资料。解绑保留已授权的管理权限；撤销管理授权需在管理页面单独执行。"}, nil
			}
			err = store.SetBinding(ctx, p.TenantID, p.ScopeID, kb.ID, in.Action == "bind", true)
			return map[string]any{"knowledge_base_id": kb.ID, "scope_id": p.ScopeID, "bound": in.Action == "bind"}, err
		}
		if in.Action != "delete_kb" {
			return nil, octobusiness.ErrInvalid
		}
		for _, use := range uses {
			if use.ScopeID != p.ScopeID {
				return nil, errors.New("此知识库还被其他区域使用；可解绑本区，删除整个库请在管理页面核对影响")
			}
		}
		// A scoped group grant must not remove a knowledge asset shared with an
		// organization or directly attached to an independent IM channel.
		var shares, channels int64
		if err = db.WithContext(ctx).Model(&types.KnowledgeBaseShare{}).Where("knowledge_base_id = ? AND source_tenant_id = ?", kb.ID, p.TenantID).Count(&shares).Error; err != nil {
			return nil, err
		}
		if err = db.WithContext(ctx).Table("im_channels").Where("knowledge_base_id = ? AND tenant_id = ? AND deleted_at IS NULL", kb.ID, p.TenantID).Count(&channels).Error; err != nil {
			return nil, err
		}
		if shares > 0 || channels > 0 {
			return nil, errors.New("此知识库还被组织共享或直接渠道配置使用；请在管理页面核对所有影响后删除。")
		}
		if preview {
			return map[string]any{"action": in.Action, "knowledge_base_id": kb.ID, "name": kb.Name, "inheriting_subareas": len(inheritedScopes), "revision": revision, "notice": "删除知识库及其生成索引、资料副本；保留服务器原始文件和远程仓库。此操作无法通过聊天撤销。"}, nil
		}
		grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: types.CallerFromContext(ctx)}, &kb, types.OrgRoleEditor, nil, nil)
		if err != nil {
			return nil, err
		}
		err = kbs.DeleteKnowledgeBase(grant.Context(ctx), kb.ID)
		return map[string]any{"knowledge_base_id": kb.ID, "deletion_requested": err == nil}, err
	}
	return s
}

func octoManagementRevision(values ...any) string {
	b, _ := json.Marshal(values)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newOctoBusiness(db *gorm.DB, kbs interfaces.KnowledgeBaseService, knowledge interfaces.KnowledgeService) *octobusiness.Service {
	return NewOctoBusiness(db, kbs, knowledge)
}
