package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func octoManagementFixture(t *testing.T) (*octobusiness.Service, *octointegration.Store, *gorm.DB, context.Context, octobusiness.Principal) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "management.db")), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	require.NoError(t, db.AutoMigrate(&octointegration.Scope{}, &octointegration.Binding{}, &octointegration.KnowledgeManagementGrant{}, &types.AuditLog{}))
	require.NoError(t, db.Exec("CREATE TABLE knowledge_bases(id TEXT PRIMARY KEY,tenant_id BIGINT,name TEXT,description TEXT,updated_at TIMESTAMP,deleted_at TIMESTAMP)").Error)
	require.NoError(t, db.Exec("INSERT INTO knowledge_bases(id,tenant_id,name,description,updated_at) VALUES ('kb',1,'Product','',CURRENT_TIMESTAMP)").Error)
	require.NoError(t, db.Exec("CREATE TABLE kb_shares(id TEXT PRIMARY KEY,knowledge_base_id TEXT,source_tenant_id BIGINT,deleted_at TIMESTAMP)").Error)
	require.NoError(t, db.Exec("CREATE TABLE im_channels(id TEXT PRIMARY KEY,tenant_id BIGINT,knowledge_base_id TEXT,deleted_at TIMESTAMP)").Error)
	ctx := types.WithExecutionTenant(types.WithCaller(context.Background(), types.Caller{TenantID: 1, UserID: "native-user", Role: types.TenantRoleViewer}), 1)
	store := octointegration.NewStore(db)
	scope, err := store.Create(ctx, octointegration.Scope{TenantID: 1, AccountID: "bot", GroupID: "group", DisplayName: "真实群"})
	require.NoError(t, err)
	require.NoError(t, store.SetBinding(ctx, 1, scope.ID, "kb", true, true))
	p := octobusiness.Principal{TenantID: 1, AccountID: "bot", ChannelID: "group", GroupID: "group", ScopeID: scope.ID, UserID: "native-user", CanManageScope: true, KnowledgeBaseIDs: []string{"kb"}, ManageKnowledgeBaseIDs: []string{"kb"}}
	return NewOctoBusiness(db, nil, nil), store, db, ctx, p
}
func TestOctoManagementRebindNeedsExplicitCurrentGrant(t *testing.T) {
	s, store, _, ctx, p := octoManagementFixture(t)
	require.NoError(t, store.SetBinding(ctx, 1, p.ScopeID, "kb", false))
	p.KnowledgeBaseIDs = nil
	in := octobusiness.ManagementInput{Action: "bind", KnowledgeBaseID: "kb"}
	preview, err := s.Management(ctx, p, in, true)
	require.NoError(t, err)
	in.ExpectedRevision = preview["revision"].(string)
	_, err = s.Management(ctx, p, in, false)
	require.NoError(t, err)
	effective, err := store.Effective(ctx, 1, p.ScopeID)
	require.NoError(t, err)
	require.Len(t, effective, 1)
	require.NoError(t, store.RevokeKnowledgeManagement(ctx, 1, p.ScopeID, "kb"))
	_, err = s.Management(ctx, p, in, true)
	require.ErrorIs(t, err, octobusiness.ErrDenied, "stale principal cannot retain revoked management")
}
func TestOctoManagementPreviewDetectsBindingAndScopeChanges(t *testing.T) {
	s, store, db, ctx, p := octoManagementFixture(t)
	in := octobusiness.ManagementInput{Action: "unbind", KnowledgeBaseID: "kb"}
	preview, err := s.Management(ctx, p, in, true)
	require.NoError(t, err)
	in.ExpectedRevision = preview["revision"].(string)
	require.NoError(t, db.Model(&octointegration.Scope{}).Where("id = ?", p.ScopeID).Update("updated_at", time.Now().Add(time.Minute)).Error)
	_, err = s.Management(ctx, p, in, false)
	require.ErrorIs(t, err, octobusiness.ErrConflict)
	in.ExpectedRevision = ""
	preview, err = s.Management(ctx, p, in, true)
	require.NoError(t, err)
	in.ExpectedRevision = preview["revision"].(string)
	require.NoError(t, store.SetBinding(ctx, 1, p.ScopeID, "kb", false))
	_, err = s.Management(ctx, p, in, false)
	require.ErrorIs(t, err, octobusiness.ErrConflict)
	other := in
	other.ScopeID = "another-group"
	_, err = s.Management(ctx, p, other, true)
	require.ErrorIs(t, err, octobusiness.ErrDenied)
}
func TestOctoChatCannotDeleteNativeSharedAssets(t *testing.T) {
	s, _, db, ctx, p := octoManagementFixture(t)
	require.NoError(t, db.Exec("INSERT INTO kb_shares(id,knowledge_base_id,source_tenant_id) VALUES ('share','kb',1)").Error)
	_, err := s.Management(ctx, p, octobusiness.ManagementInput{Action: "delete_kb", KnowledgeBaseID: "kb"}, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "组织共享")
	require.NoError(t, db.Exec("DELETE FROM kb_shares").Error)
	require.NoError(t, db.Exec("INSERT INTO im_channels(id,tenant_id,knowledge_base_id) VALUES ('channel',1,'kb')").Error)
	_, err = s.Management(ctx, p, octobusiness.ManagementInput{Action: "delete_kb", KnowledgeBaseID: "kb"}, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "直接渠道")
}
