package repository

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func octoDeleteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "octo-delete.db")), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	raw.SetMaxOpenConns(1) // SQLite serializes writers; PostgreSQL uses the row lock.
	t.Cleanup(func() { _ = raw.Close() })
	require.NoError(t, db.Exec(knowledgeBasesTestDDL).Error)
	require.NoError(t, db.AutoMigrate(&octointegration.Scope{}, &octointegration.Binding{}, &octointegration.KnowledgeManagementGrant{}, &types.AuditLog{}))
	return db
}

func octoDeleteContext(tenant uint64) context.Context {
	return types.WithExecutionTenant(context.Background(), tenant)
}

func TestKnowledgeBaseDeleteSerializesOctoReferencesAndPreservesOtherTenants(t *testing.T) {
	db := octoDeleteDB(t)
	repo := NewKnowledgeBaseRepository(db)
	store := octointegration.NewStore(db)
	ctx := octoDeleteContext(1)
	kb := makeKB(nil)
	require.NoError(t, db.Create(kb).Error)
	scope, err := store.Create(ctx, octointegration.Scope{TenantID: 1, AccountID: "bot", GroupID: "group", DisplayName: "Group"})
	require.NoError(t, err)
	require.NoError(t, store.SetBinding(ctx, 1, scope.ID, kb.ID, true, true))
	require.ErrorIs(t, repo.DeleteKnowledgeBase(ctx, kb.ID), ErrKnowledgeBaseInUse)
	require.NoError(t, store.SetBinding(ctx, 1, scope.ID, kb.ID, false))
	require.ErrorIs(t, repo.DeleteKnowledgeBase(ctx, kb.ID), ErrKnowledgeBaseInUse, "management-only grant still blocks deletion")
	require.NoError(t, store.RevokeKnowledgeManagement(ctx, 1, scope.ID, kb.ID))
	require.NoError(t, repo.DeleteKnowledgeBase(ctx, kb.ID))
	require.ErrorIs(t, store.SetBinding(ctx, 1, scope.ID, kb.ID, true), gorm.ErrRecordNotFound, "soft-deleted KB cannot be rebound")
	var deleted types.KnowledgeBase
	require.NoError(t, db.Unscoped().Where("id = ?", kb.ID).First(&deleted).Error)
	require.True(t, deleted.DeletedAt.Valid)

	foreign := makeKB(nil)
	foreign.TenantID = 2
	require.NoError(t, db.Create(foreign).Error)
	require.ErrorIs(t, repo.DeleteKnowledgeBase(ctx, foreign.ID), ErrKnowledgeBaseNotFound)
	require.NoError(t, db.Where("id = ?", foreign.ID).First(&types.KnowledgeBase{}).Error)
}

func TestVerifiedOctoScopeDeleteRemovesOnlyOwnReferencesAndAuditsAtomically(t *testing.T) {
	db := octoDeleteDB(t)
	repo := NewKnowledgeBaseRepository(db)
	store := octointegration.NewStore(db)
	ctx := octoDeleteContext(1)
	kb := makeKB(nil)
	require.NoError(t, db.Create(kb).Error)
	owner, err := store.Create(ctx, octointegration.Scope{TenantID: 1, AccountID: "bot", GroupID: "owner", DisplayName: "Owner"})
	require.NoError(t, err)
	other, err := store.Create(ctx, octointegration.Scope{TenantID: 1, AccountID: "bot", GroupID: "other", DisplayName: "Other"})
	require.NoError(t, err)
	require.NoError(t, store.SetBinding(ctx, 1, owner.ID, kb.ID, true, true))
	deleteCtx := WithVerifiedOctoScopeDeletion(ctx, 1, owner.ID, "native-user")
	require.ErrorIs(t, repo.DeleteKnowledgeBase(WithVerifiedOctoScopeDeletion(ctx, 2, owner.ID, "native-user"), kb.ID), ErrKnowledgeBaseInUse)
	require.ErrorIs(t, repo.DeleteKnowledgeBase(WithVerifiedOctoScopeDeletion(ctx, 1, other.ID, "native-user"), kb.ID), ErrKnowledgeBaseInUse, "a scope without its own management grant cannot bypass dependencies")
	require.NoError(t, store.SetBinding(ctx, 1, other.ID, kb.ID, true, true))
	require.NoError(t, store.SetBinding(ctx, 1, other.ID, kb.ID, false))
	require.ErrorIs(t, repo.DeleteKnowledgeBase(deleteCtx, kb.ID), ErrKnowledgeBaseInUse, "another scope's maintenance-only grant must block")
	require.NoError(t, store.RevokeKnowledgeManagement(ctx, 1, other.ID, kb.ID))
	require.NoError(t, repo.DeleteKnowledgeBase(deleteCtx, kb.ID))
	var bindings, grants, audits int64
	require.NoError(t, db.Table("octo_scope_bindings").Where("knowledge_base_id = ?", kb.ID).Count(&bindings).Error)
	require.NoError(t, db.Table("octo_scope_knowledge_grants").Where("knowledge_base_id = ?", kb.ID).Count(&grants).Error)
	require.Zero(t, bindings)
	require.Zero(t, grants)
	require.NoError(t, db.Model(&types.AuditLog{}).Where("action = ? AND target_id = ? AND scope_id = ?", "octo.scope.authorization_removed_for_kb_delete", kb.ID, owner.ID).Count(&audits).Error)
	require.Equal(t, int64(1), audits)
}

func TestVerifiedOctoScopeDeleteRollsBackReferencesWhenAuditFails(t *testing.T) {
	db := octoDeleteDB(t)
	repo := NewKnowledgeBaseRepository(db)
	store := octointegration.NewStore(db)
	ctx := octoDeleteContext(1)
	kb := makeKB(nil)
	require.NoError(t, db.Create(kb).Error)
	scope, err := store.Create(ctx, octointegration.Scope{TenantID: 1, AccountID: "bot", GroupID: "group", DisplayName: "Group"})
	require.NoError(t, err)
	require.NoError(t, store.SetBinding(ctx, 1, scope.ID, kb.ID, true, true))
	require.NoError(t, db.Exec("DROP TABLE audit_logs").Error)
	require.Error(t, repo.DeleteKnowledgeBase(WithVerifiedOctoScopeDeletion(ctx, 1, scope.ID, "native-user"), kb.ID))
	var count int64
	require.NoError(t, db.Table("octo_scope_bindings").Where("knowledge_base_id = ?", kb.ID).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.NoError(t, db.Table("octo_scope_knowledge_grants").Where("knowledge_base_id = ?", kb.ID).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.NoError(t, db.Where("id = ?", kb.ID).First(&types.KnowledgeBase{}).Error)
}

func TestConcurrentOctoBindAndKBDeleteNeverLeaveDeletedKBReferenced(t *testing.T) {
	db := octoDeleteDB(t)
	repo := NewKnowledgeBaseRepository(db)
	store := octointegration.NewStore(db)
	ctx := octoDeleteContext(1)
	scope, err := store.Create(ctx, octointegration.Scope{TenantID: 1, AccountID: "bot", GroupID: "group", DisplayName: "Group"})
	require.NoError(t, err)
	for i := 0; i < 12; i++ {
		kb := makeKB(nil)
		require.NoError(t, db.Create(kb).Error)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var bindErr, deleteErr error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; bindErr = store.SetBinding(ctx, 1, scope.ID, kb.ID, true) }()
		go func() { defer wg.Done(); <-start; deleteErr = repo.DeleteKnowledgeBase(ctx, kb.ID) }()
		close(start)
		wg.Wait()
		var state types.KnowledgeBase
		require.NoError(t, db.Unscoped().Where("id = ?", kb.ID).First(&state).Error)
		var bindings int64
		require.NoError(t, db.Table("octo_scope_bindings").Where("knowledge_base_id = ?", kb.ID).Count(&bindings).Error)
		if state.DeletedAt.Valid {
			require.NoError(t, deleteErr)
			require.ErrorIs(t, bindErr, gorm.ErrRecordNotFound)
			require.Zero(t, bindings)
		} else {
			require.NoError(t, bindErr)
			require.True(t, errors.Is(deleteErr, ErrKnowledgeBaseInUse), "delete outcome: %v", deleteErr)
			require.Equal(t, int64(1), bindings)
		}
	}
}
