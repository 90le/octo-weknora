package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Exercise the Agent's real tool registration path, not a separately assembled
// source reader: the lightweight reader must receive the same DB-backed registry
// as scheduled synchronization, including revocation of an existing snapshot.
func TestAgentRegisteredLocalSourceBrowseReadsAndHonorsRootRevocation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "agent-source.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.DataSource{}))
	migration, err := os.ReadFile("../../../migrations/sqlite/000020_local_source_roots.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	sourceDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "main.py"), []byte("LOCAL-SOURCE-REGISTRATION-CHECK\n"), 0o600))
	input, _ := json.Marshal([]map[string]any{{"id": "product", "name": "Product source", "path": sourceDir, "tenant_id": 7}})
	t.Setenv("DATASOURCE_LOCAL_SPACES", "")
	t.Setenv("DATASOURCE_LOCAL_ROOTS", string(input))
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", t.TempDir())
	roots, err := localfolder.NewRegistry(db)
	require.NoError(t, err)
	connector := localfolder.NewConnector(roots)
	connectors := datasource.NewConnectorRegistry()
	require.NoError(t, connectors.Register(connector))
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"root_id": "product", "mode": "source"}}
	encoded, err := cfg.ToJSON()
	require.NoError(t, err)
	ds := &types.DataSource{ID: "local-source", TenantID: 7, KnowledgeBaseID: "kb", Type: localfolder.Type, Name: "Product source", Config: encoded}
	ctx := types.WithExecutionTenant(types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer}), 7)
	cache, err := snapshot.FromEnvironment()
	require.NoError(t, err)
	builder, err := cache.Begin(ds, cfg)
	require.NoError(t, err)
	require.NoError(t, connector.BuildSnapshot(ctx, cfg, builder))
	current, err := builder.Finish()
	require.NoError(t, err)
	ds.LastSyncCursor, err = (&types.SyncCursor{ConnectorCursor: map[string]interface{}{"snapshot_id": current.ID}}).ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Create(ds).Error)
	svc := &agentService{db: db, cfg: &config.Config{}, knowledgeBaseService: &sourceKBStub{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7, Name: "Product"}}, connectorRegistry: connectors}
	toolset := tools.NewToolRegistry()
	disabled := false
	agentCfg := &types.AgentConfig{AllowedTools: []string{tools.ToolSourceBrowse}, KnowledgeBases: []string{"kb"}, SearchTargets: types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb", TenantID: 7}}, MemoryEnabled: &disabled}
	require.NoError(t, svc.registerTools(ctx, toolset, agentCfg, nil, nil, "local-source-session"))
	readArgs := json.RawMessage(`{"action":"read","knowledge_base_id":"kb","source_id":"local-source","path":"main.py","start_line":1,"end_line":1}`)
	result, err := toolset.ExecuteTool(ctx, tools.ToolSourceBrowse, readArgs)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Output, "LOCAL-SOURCE-REGISTRATION-CHECK")
	foreign := types.WithExecutionTenant(types.WithCaller(context.Background(), types.Caller{TenantID: 8, UserID: "outsider", Role: types.TenantRoleViewer}), 8)
	result, err = toolset.ExecuteTool(foreign, tools.ToolSourceBrowse, readArgs)
	require.NoError(t, err)
	require.False(t, result.Success, "a guessed source ID must not grant another workspace access")
	_, err = roots.Update(ctx, 7, "product", "Product source", false)
	require.NoError(t, err)
	result, err = toolset.ExecuteTool(ctx, tools.ToolSourceBrowse, readArgs)
	require.NoError(t, err)
	require.False(t, result.Success, "existing source snapshots must respect current root revocation")
	_, err = roots.Update(ctx, 7, "product", "Product source", true)
	require.NoError(t, err)
	result, err = toolset.ExecuteTool(ctx, tools.ToolSourceBrowse, readArgs)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
}
