package octointegration

import (
	"context"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func adminCaller(tenant uint64) types.Caller {
	return types.Caller{TenantID: tenant, Role: types.TenantRoleAdmin, UserID: "admin"}
}

func configurationFor(s ConfigurationSnapshot) ScopeConfiguration {
	return ScopeConfiguration{
		ScopeACL: s.ScopeACL, InheritParent: &s.Scope.InheritParent,
		AllowKnowledgeCreation: s.Scope.AllowKnowledgeCreation,
		AggregateChildIssues:   s.Scope.AggregateChildIssues,
		AllowPublicWeb:         s.Scope.AllowPublicWeb,
	}
}

// Run the same feature cases on an explicitly supplied disposable PostgreSQL
// instance as well as the default SQLite fixture. Each case owns a fresh schema.
func managementStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("OCTO_MANAGEMENT_TEST_POSTGRES")
	if dsn == "" {
		return testStore(t)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	schema := "octo_management_" + uuid.New().String()[:8]
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { require.NoError(t, db.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })
	require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	require.NoError(t, db.Exec(`CREATE TABLE knowledge_bases (
		id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, deleted_at TIMESTAMP
	)`).Error)
	for _, file := range []string{
		"000119_octo_scopes", "000120_octo_connections", "000125_octo_knowledge_grants",
		"000127_octo_verified_identity", "000130_octo_scope_public_web",
	} {
		sql, err := os.ReadFile("../../migrations/versioned/" + file + ".up.sql")
		require.NoError(t, err)
		require.NoError(t, db.Exec(string(sql)).Error)
	}
	require.NoError(t, db.Exec("INSERT INTO knowledge_bases(id, tenant_id) VALUES ('kb-a', 1), ('kb-b', 2)").Error)
	return NewStore(db)
}

func TestConfigurationPreviewIncludesOnlyAffectedSubareasOfSameBot(t *testing.T) {
	ctx, s := context.Background(), managementStore(t)
	main := createScope(t, s, 1, "bot-a", "group", "", false)
	inheriting := createScope(t, s, 1, "bot-a", "group", "feedback", true)
	independent := createScope(t, s, 1, "bot-a", "group", "docs", false)
	direct := createScope(t, s, 1, "bot-a", "group", "support", true)
	other := createScope(t, s, 1, "bot-b", "group", "", false)
	otherChild := createScope(t, s, 1, "bot-b", "group", "feedback", true)
	for _, row := range []*Scope{main, independent, direct, other} {
		require.NoError(t, s.SetBinding(ctx, 1, row.ID, "kb-a", true, true))
	}
	before, err := s.configuration(ctx, 1, main.ID)
	require.NoError(t, err)
	cfg := configurationFor(before)
	cfg.QueryIDs = []string{}
	p, err := s.changeConfiguration(ctx, adminCaller(1), 1, main.ID, before.Revision, "", cfg, false)
	require.NoError(t, err)
	require.Len(t, p.Impacts, 2)
	require.Equal(t, main.ID, p.Impacts[0].ScopeID)
	require.Equal(t, inheriting.ID, p.Impacts[1].ScopeID)
	require.Equal(t, KnowledgeImpact{
		KnowledgeBaseID: "kb-a", Before: accessState{QueryMode: "inherited"}, After: accessState{QueryMode: "none"},
	}, p.Impacts[1].Changes[0])
	require.True(t, p.Impacts[0].Changes[0].After.Manage, "removing query does not revoke maintenance")

	got, err := s.Effective(ctx, 1, inheriting.ID)
	require.NoError(t, err)
	require.Len(t, got, 1, "preview has no writes")
	_, err = s.changeConfiguration(ctx, adminCaller(1), 1, main.ID, before.Revision, p.Token, cfg, true)
	require.NoError(t, err)
	got, err = s.Effective(ctx, 1, inheriting.ID)
	require.NoError(t, err)
	require.Empty(t, got)
	for _, row := range []*Scope{direct, independent, otherChild} {
		got, err := s.Effective(ctx, 1, row.ID)
		require.NoError(t, err)
		require.Len(t, got, 1)
	}
	managed, err := s.ManagedKnowledgeBases(ctx, 1, main.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"kb-a"}, managed)
}

func TestConfigurationRejectsStaleAndTamperedPreviewAndForeignKB(t *testing.T) {
	ctx, s := context.Background(), managementStore(t)
	main := createScope(t, s, 1, "bot", "group", "", false)
	child := createScope(t, s, 1, "bot", "group", "feedback", false)
	before, err := s.configuration(ctx, 1, child.ID)
	require.NoError(t, err)
	cfg := configurationFor(before)
	inherit := true
	cfg.InheritParent = &inherit
	p, err := s.changeConfiguration(ctx, adminCaller(1), 1, child.ID, before.Revision, "", cfg, false)
	require.NoError(t, err)
	tampered := cfg
	tampered.ManageIDs = []string{"kb-a"}
	_, err = s.changeConfiguration(ctx, adminCaller(1), 1, child.ID, before.Revision, p.Token, tampered, true)
	require.ErrorIs(t, err, errStalePreview)
	tampered.QueryIDs = []string{"kb-b"}
	_, err = s.changeConfiguration(ctx, adminCaller(1), 1, child.ID, before.Revision, "", tampered, false)
	require.ErrorIs(t, err, access.ErrNotFound)
	_, err = s.changeConfiguration(ctx, adminCaller(2), 1, child.ID, before.Revision, "", cfg, false)
	require.ErrorIs(t, err, access.ErrForbidden)

	require.NoError(t, s.SetBinding(ctx, 1, main.ID, "kb-a", true))
	_, err = s.changeConfiguration(ctx, adminCaller(1), 1, child.ID, before.Revision, p.Token, cfg, true)
	require.ErrorIs(t, err, errStalePreview)
	fresh, err := s.configuration(ctx, 1, child.ID)
	require.NoError(t, err)
	require.False(t, fresh.Scope.InheritParent)
	require.Empty(t, fresh.ManageIDs)
}

func TestScopeRemovalKeepsOtherBotAndKBAndBlocksDependencies(t *testing.T) {
	ctx, s := context.Background(), managementStore(t)
	main := createScope(t, s, 1, "bot-a", "group", "", false)
	child := createScope(t, s, 1, "bot-a", "group", "feedback", false)
	other := createScope(t, s, 1, "bot-b", "group", "", false)
	require.NoError(t, s.SetBinding(ctx, 1, child.ID, "kb-a", true, true))
	require.NoError(t, s.SetBinding(ctx, 1, other.ID, "kb-a", true))
	require.NoError(t, s.db.Exec(`CREATE TABLE octo_report_schedules (
		tenant_id BIGINT, scope_id TEXT, enabled BOOLEAN
	)`).Error)
	require.NoError(t, s.db.Exec("INSERT INTO octo_report_schedules VALUES (1, ?, TRUE)", child.ID).Error)

	parentConfig, err := s.configuration(ctx, 1, main.ID)
	require.NoError(t, err)
	require.Len(t, parentConfig.ChildAreas, 1)
	require.ErrorIs(t, s.removeScope(ctx, adminCaller(1), 1, main.ID, parentConfig.Revision), errScopeChildren)
	childConfig, err := s.configuration(ctx, 1, child.ID)
	require.NoError(t, err)
	require.ErrorIs(t, s.removeScope(ctx, adminCaller(1), 1, child.ID, childConfig.Revision), errScopeSchedule)
	require.NoError(t, s.db.Exec("UPDATE octo_report_schedules SET enabled = FALSE").Error)
	require.NoError(t, s.removeScope(ctx, adminCaller(1), 1, child.ID, childConfig.Revision))
	_, err = s.Get(ctx, 1, child.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var removedGrants int64
	require.NoError(t, s.db.Model(&KnowledgeManagementGrant{}).
		Where("scope_id = ?", child.ID).Count(&removedGrants).Error)
	require.Zero(t, removedGrants)
	require.ErrorIs(t, s.removeScope(ctx, adminCaller(1), 1, main.ID, parentConfig.Revision), errStalePreview)
	got, err := s.Effective(ctx, 1, other.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestDirectoryPaginatesGroupsAndComparesTheSameSubarea(t *testing.T) {
	ctx, s := context.Background(), managementStore(t)
	a := createScope(t, s, 1, "bot-a", "g-1", "", false)
	child := createScope(t, s, 1, "bot-a", "g-1", "bug", true)
	require.NoError(t, s.Update(ctx, 1, child.ID, "反馈_100%", true))
	createScope(t, s, 1, "bot-b", "g-1", "", false)
	createScope(t, s, 1, "bot-a", "g-2", "", false)
	createScope(t, s, 2, "bot-c", "g-1", "", false)
	require.NoError(t, s.SetBinding(ctx, 1, a.ID, "kb-a", true, true))
	require.NoError(t, s.db.Exec(`INSERT INTO octo_connections
		(tenant_id, account_id, token, verified_identity, updated_at) VALUES (1, ?, ?, ?, CURRENT_TIMESTAMP)`,
		"bot-a", "unused", `{"bot_uid":"octo-a","name":"小丘"}`).Error)

	groups, err := s.groups(ctx, 1, directoryQuery{limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, groups.Total)
	require.Len(t, groups.Items, 1)
	next, err := s.groups(ctx, 1, directoryQuery{limit: 1, offset: 1})
	require.NoError(t, err)
	require.NotEqual(t, groups.Items[0].GroupID, next.Items[0].GroupID)
	groups, err = s.groups(ctx, 1, directoryQuery{limit: 10, q: "_100%", account: "bot-a", filter: "multiple"})
	require.NoError(t, err)
	require.Len(t, groups.Items, 1)
	require.Equal(t, 2, groups.Items[0].BotCount)
	bots, err := s.bots(ctx, 1, directoryQuery{limit: 10, q: "小丘", filter: "verified"})
	require.NoError(t, err)
	require.Len(t, bots.Items, 1)
	require.Equal(t, "bot-a", bots.Items[0].AccountID)
	require.Equal(t, 2, bots.Items[0].Groups)
	areas, err := s.areas(ctx, 1, directoryQuery{limit: 10, group: "g-1", q: "反馈"})
	require.NoError(t, err)
	require.Len(t, areas.Items, 1)
	require.Equal(t, "bug", areas.Items[0].SubareaID)
	comp, err := s.compare(ctx, 1, directoryQuery{limit: 10, group: "g-1", subarea: "bug"})
	require.NoError(t, err)
	require.Len(t, comp.Items, 2)
	require.Equal(t, child.ID, comp.Items[0].Scope.ID)
	require.Equal(t, 1, comp.Items[0].QueryCount)
	require.Zero(t, comp.Items[0].ManageCount)
	require.Nil(t, comp.Items[1].Scope)
}
