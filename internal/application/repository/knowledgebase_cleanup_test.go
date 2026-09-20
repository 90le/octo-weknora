package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func kbCleanupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cleanup.db")), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	for _, sql := range []string{
		"CREATE TABLE knowledge_bases(id TEXT PRIMARY KEY, tenant_id BIGINT, deleted_at TIMESTAMP)",
		"CREATE TABLE knowledges(id TEXT PRIMARY KEY, tenant_id BIGINT, knowledge_base_id TEXT, storage_size BIGINT, deleted_at TIMESTAMP)",
		"CREATE TABLE tenants(id BIGINT PRIMARY KEY, storage_used BIGINT, updated_at TIMESTAMP, deleted_at TIMESTAMP)",
		"CREATE TABLE resources(id TEXT PRIMARY KEY, tenant_id BIGINT, handle TEXT, physical_path TEXT, state TEXT, deleted_at TIMESTAMP)",
		"CREATE TABLE resource_bindings(id TEXT PRIMARY KEY, resource_id TEXT, tenant_id BIGINT, owner_type TEXT, owner_id TEXT)",
		"CREATE TABLE resource_access_grants(id TEXT PRIMARY KEY, resource_id TEXT)",
		"CREATE TABLE knowledge_tag_relations(knowledge_id TEXT, tag_id TEXT)",
		"CREATE TABLE octo_scope_bindings(tenant_id BIGINT, knowledge_base_id TEXT)",
		"CREATE TABLE octo_knowledge_contacts(tenant_id BIGINT, knowledge_base_id TEXT)",
		"CREATE TABLE octo_scope_knowledge_grants(tenant_id BIGINT, knowledge_base_id TEXT)",
		"CREATE TABLE octo_knowledge_proposals(tenant_id BIGINT, knowledge_base_id TEXT,status TEXT,updated_at TIMESTAMP)",
		"CREATE TABLE octo_knowledge_issues(tenant_id BIGINT,knowledge_base_id TEXT)",
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	for _, table := range []string{"wiki_page_revisions", "wiki_page_issues", "wiki_pages", "wiki_folders", "chunk_revisions", "chunks", "knowledge_tags"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+"(id TEXT PRIMARY KEY, tenant_id BIGINT, knowledge_base_id TEXT)").Error)
		require.NoError(t, db.Exec("INSERT INTO "+table+" VALUES ('gone',7,'kb-gone'),('keep',7,'kb-keep'),('other',8,'kb-other')").Error)
	}
	for _, sql := range []string{
		"INSERT INTO knowledge_bases VALUES ('kb-gone',7,CURRENT_TIMESTAMP),('kb-keep',7,NULL),('kb-other',8,NULL)",
		"INSERT INTO knowledges VALUES ('doc-gone',7,'kb-gone',30,NULL),('doc-old',7,'kb-gone',20,CURRENT_TIMESTAMP),('doc-keep',7,'kb-keep',70,NULL)",
		"INSERT INTO tenants(id,storage_used) VALUES (7,100),(8,500)",
		"INSERT INTO resources VALUES ('private',7,'abcdefghijklmnopqrstuv','local/private','active',NULL),('shared',7,'zyxwvutsrqponmlkjihgfe','local/shared','active',NULL)",
		"INSERT INTO resource_bindings VALUES ('p', 'private',7,'knowledge','doc-gone'),('s1','shared',7,'knowledge','doc-gone'),('s2','shared',7,'knowledge','doc-keep')",
		"INSERT INTO resource_access_grants VALUES ('gp','private'),('gs','shared')",
		"INSERT INTO knowledge_tag_relations VALUES ('doc-gone','gone'),('doc-keep','keep')",
		"INSERT INTO octo_scope_bindings VALUES (7,'kb-gone'),(7,'kb-keep')",
		"INSERT INTO octo_knowledge_contacts VALUES (7,'kb-gone'),(7,'kb-keep')",
		"INSERT INTO octo_scope_knowledge_grants VALUES (7,'kb-gone'),(7,'kb-keep')",
		"INSERT INTO octo_knowledge_proposals VALUES (7,'kb-gone','pending',NULL),(7,'kb-keep','pending',NULL)",
		"INSERT INTO octo_knowledge_issues VALUES (7,'kb-gone'),(7,'kb-keep')",
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	return db
}
func TestKBCleanupRemovesWikiAndBindingsPreservesOtherKnowledge(t *testing.T) {
	db := kbCleanupDB(t)
	repo := &knowledgeBaseRepository{db: db}
	ctx := context.Background()
	plan, err := repo.PrepareKnowledgeBaseCleanup(ctx, 7, "kb-gone")
	require.NoError(t, err)
	require.Len(t, plan.Knowledge, 2)
	require.Len(t, plan.Resources, 2)
	shared := map[string]bool{}
	for _, resource := range plan.Resources {
		shared[resource.Resource.ID] = resource.Shared
	}
	require.False(t, shared["private"])
	require.True(t, shared["shared"])
	require.NoError(t, repo.FinalizeKnowledgeBaseCleanup(ctx, 7, "kb-gone"))
	// Replaying the durable task does not decrement storage a second time.
	require.NoError(t, repo.FinalizeKnowledgeBaseCleanup(ctx, 7, "kb-gone"))
	var storage int64
	require.NoError(t, db.Table("tenants").Select("storage_used").Where("id = 7").Scan(&storage).Error)
	require.Equal(t, int64(70), storage)
	for _, table := range []string{"knowledges", "wiki_page_revisions", "wiki_page_issues", "wiki_pages", "wiki_folders", "chunk_revisions", "chunks", "knowledge_tags", "octo_scope_bindings", "octo_knowledge_contacts", "octo_scope_knowledge_grants"} {
		var count int64
		require.NoError(t, db.Table(table).Where("knowledge_base_id = ?", "kb-gone").Count(&count).Error)
		require.Zero(t, count, table)
		require.NoError(t, db.Table(table).Where("knowledge_base_id = ?", "kb-keep").Count(&count).Error)
		require.Equal(t, int64(1), count, table)
	}
	var issueCount int64
	require.NoError(t, db.Table("octo_knowledge_issues").Count(&issueCount).Error)
	require.Equal(t, int64(2), issueCount)
	var proposalStatus string
	require.NoError(t, db.Table("octo_knowledge_proposals").Select("status").Where("knowledge_base_id = 'kb-gone'").Scan(&proposalStatus).Error)
	require.Equal(t, "cancelled", proposalStatus)
	var resources, bindings, grants int64
	require.NoError(t, db.Table("resources").Where("id = 'shared'").Count(&resources).Error)
	require.Equal(t, int64(1), resources)
	require.NoError(t, db.Table("resource_bindings").Count(&bindings).Error)
	require.Equal(t, int64(1), bindings)
	require.NoError(t, db.Table("resource_access_grants").Where("resource_id = 'shared'").Count(&grants).Error)
	require.Equal(t, int64(1), grants)
	require.NoError(t, db.Table("resources").Where("id = 'private'").Count(&resources).Error)
	require.Zero(t, resources)
}
func TestKBCleanupFailureKeepsRetryEvidenceAndAccounting(t *testing.T) {
	db := kbCleanupDB(t)
	repo := &knowledgeBaseRepository{db: db}
	ctx := context.Background()
	require.NoError(t, db.Exec("CREATE TRIGGER fail_cleanup BEFORE DELETE ON wiki_pages BEGIN SELECT RAISE(ABORT, 'simulated storage failure'); END").Error)
	require.Error(t, repo.FinalizeKnowledgeBaseCleanup(ctx, 7, "kb-gone"))
	var count, storage int64
	require.NoError(t, db.Table("resources").Count(&count).Error)
	require.Equal(t, int64(2), count)
	require.NoError(t, db.Table("resource_bindings").Count(&count).Error)
	require.Equal(t, int64(3), count)
	require.NoError(t, db.Table("tenants").Select("storage_used").Where("id = 7").Scan(&storage).Error)
	require.Equal(t, int64(100), storage)
	require.NoError(t, db.Exec("DROP TRIGGER fail_cleanup").Error)
	require.NoError(t, repo.FinalizeKnowledgeBaseCleanup(ctx, 7, "kb-gone"))
}
func TestKBCleanupRejectsLiveOrForeignKnowledgeBase(t *testing.T) {
	repo := &knowledgeBaseRepository{db: kbCleanupDB(t)}
	ctx := context.Background()
	_, err := repo.PrepareKnowledgeBaseCleanup(ctx, 7, "kb-keep")
	require.Error(t, err)
	_, err = repo.PrepareKnowledgeBaseCleanup(ctx, 8, "kb-gone")
	require.Error(t, err)
	require.Error(t, repo.FinalizeKnowledgeBaseCleanup(ctx, 8, "kb-gone"))
	require.Error(t, repo.FinalizeKnowledgeBaseCleanup(ctx, 7, "kb-keep"))
}
