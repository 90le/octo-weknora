package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// prepare is read-only. Physical cleanup must succeed before finalization removes
// the only durable records describing those objects. Deleted knowledge rows are
// included to reclaim residue left by older best-effort deletion workers.
func (r *knowledgeBaseRepository) PrepareKnowledgeBaseCleanup(ctx context.Context, tenant uint64, kbID string) (*interfaces.KBDeletionPlan, error) {
	if tenant == 0 || kbID == "" {
		return nil, errors.New("invalid knowledge base cleanup identity")
	}
	var kb types.KnowledgeBase
	if err := r.db.WithContext(ctx).Unscoped().Where("id = ? AND tenant_id = ?", kbID, tenant).First(&kb).Error; err != nil {
		return nil, err
	}
	if !kb.DeletedAt.Valid {
		return nil, errors.New("cannot clean a live knowledge base")
	}
	plan := &interfaces.KBDeletionPlan{}
	if err := r.db.WithContext(ctx).Unscoped().Where("tenant_id = ? AND knowledge_base_id = ?", tenant, kbID).Find(&plan.Knowledge).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(plan.Knowledge))
	handles := []string{}
	for _, k := range plan.Knowledge {
		ids = append(ids, k.ID)
		if handle, ok := types.ParseResourcePath(k.FilePath); ok {
			handles = append(handles, handle)
		}
	}
	if len(ids) == 0 {
		return plan, nil
	}
	var resources []types.StoredResource
	query := r.db.WithContext(ctx).Unscoped().Model(&types.StoredResource{}).
		Where("resources.tenant_id = ? AND (resources.id IN (?) OR resources.handle IN ?)", tenant,
			r.db.Model(&types.ResourceBinding{}).Select("resource_id").Where("tenant_id = ? AND owner_type = ? AND owner_id IN ?", tenant, types.ResourceOwnerKnowledge, ids), handles)
	if err := query.Find(&resources).Error; err != nil {
		return nil, err
	}
	for _, resource := range resources {
		var outside int64
		if err := r.db.WithContext(ctx).Model(&types.ResourceBinding{}).Where("resource_id = ? AND NOT (tenant_id = ? AND owner_type = ? AND owner_id IN ?)", resource.ID, tenant, types.ResourceOwnerKnowledge, ids).Count(&outside).Error; err != nil {
			return nil, err
		}
		plan.Resources = append(plan.Resources, interfaces.KBDeletionResource{Resource: resource, Shared: outside > 0})
	}
	return plan, nil
}

// Metadata and storage accounting retire in one transaction. A failed attempt
// retains its rows and a successful retry cannot decrement storage a second time.
func (r *knowledgeBaseRepository) FinalizeKnowledgeBaseCleanup(ctx context.Context, tenant uint64, kbID string) error {
	if tenant == 0 || kbID == "" {
		return errors.New("invalid knowledge base cleanup identity")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var kb types.KnowledgeBase
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ?", kbID, tenant).First(&kb).Error; err != nil {
			return err
		}
		if !kb.DeletedAt.Valid {
			return errors.New("cannot clean a live knowledge base")
		}
		var knowledge []*types.Knowledge
		if err := tx.Unscoped().Where("tenant_id = ? AND knowledge_base_id = ?", tenant, kbID).Find(&knowledge).Error; err != nil {
			return err
		}
		ids := make([]string, 0, len(knowledge))
		var storage int64
		for _, k := range knowledge {
			ids = append(ids, k.ID)
			if !k.DeletedAt.Valid {
				storage += k.StorageSize
			}
		}
		if len(ids) > 0 {
			plan, err := (&knowledgeBaseRepository{db: tx}).PrepareKnowledgeBaseCleanup(ctx, tenant, kbID)
			if err != nil {
				return err
			}
			resourceIDs := make([]string, 0, len(plan.Resources))
			for _, entry := range plan.Resources {
				resourceIDs = append(resourceIDs, entry.Resource.ID)
			}
			if err := tx.Where("tenant_id = ? AND owner_type = ? AND owner_id IN ?", tenant, types.ResourceOwnerKnowledge, ids).Delete(&types.ResourceBinding{}).Error; err != nil {
				return err
			}
			for _, resourceID := range resourceIDs {
				var count int64
				if err := tx.Model(&types.ResourceBinding{}).Where("resource_id = ?", resourceID).Count(&count).Error; err != nil {
					return err
				}
				if count > 0 {
					continue
				}
				if err := tx.Where("resource_id = ?", resourceID).Delete(&types.ResourceAccessGrant{}).Error; err != nil {
					return err
				}
				if err := tx.Unscoped().Where("id = ? AND tenant_id = ?", resourceID, tenant).Delete(&types.StoredResource{}).Error; err != nil {
					return err
				}
			}
		}
		for _, model := range []any{&types.WikiPageRevision{}, &types.WikiPageIssue{}, &types.WikiPage{}, &types.WikiFolder{}, &types.ChunkRevision{}, &types.Chunk{}, &types.KnowledgeTag{}} {
			if err := tx.Unscoped().Where("tenant_id = ? AND knowledge_base_id = ?", tenant, kbID).Delete(model).Error; err != nil {
				return err
			}
		}
		if len(ids) > 0 {
			// Join tables have no tenant column; IDs came exclusively from this KB.
			if err := tx.Where("knowledge_id IN ?", ids).Delete(&types.KnowledgeTagRelation{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("tenant_id = ? AND knowledge_base_id = ?", tenant, kbID).Delete(&types.Knowledge{}).Error; err != nil {
				return err
			}
		}
		if storage != 0 {
			var owner types.Tenant
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&owner, "id = ?", tenant).Error; err != nil {
				return err
			}
			remaining := owner.StorageUsed - storage
			if remaining < 0 {
				remaining = 0
			}
			if err := tx.Model(&owner).Updates(map[string]any{"storage_used": remaining, "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		// Access bindings are derived configuration; the original source directory is
		// never touched. The KB tombstone and activity audit remain for diagnosis.
		if tx.Migrator().HasTable("octo_scope_bindings") {
			if err := tx.Table("octo_scope_bindings").Where("tenant_id = ? AND knowledge_base_id = ?", tenant, kbID).Delete(nil).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable("octo_scope_knowledge_grants") {
			if err := tx.Table("octo_scope_knowledge_grants").Where("tenant_id = ? AND knowledge_base_id = ?", tenant, kbID).Delete(nil).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable("octo_knowledge_contacts") {
			if err := tx.Table("octo_knowledge_contacts").Where("tenant_id = ? AND knowledge_base_id = ?", tenant, kbID).Delete(nil).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable("octo_knowledge_proposals") {
			if err := tx.Table("octo_knowledge_proposals").Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", tenant, kbID, "pending").Updates(map[string]any{"status": "cancelled", "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		// Submitted issues and their event history are audit records. They stay.
		return nil
	})
}
