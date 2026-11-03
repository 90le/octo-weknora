package im

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIMSandboxGrantRechecksIdentitySessionAndAgent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "grant.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	for _, statement := range []string{
		`CREATE TABLE im_channels (id TEXT PRIMARY KEY, tenant_id INTEGER, agent_id TEXT, platform TEXT, enabled BOOLEAN, mode TEXT, session_mode TEXT, credentials BLOB, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE im_channel_sessions (id TEXT PRIMARY KEY, im_channel_id TEXT, tenant_id INTEGER, agent_id TEXT, platform TEXT, user_id TEXT, chat_id TEXT, session_id TEXT, status TEXT, metadata BLOB, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, tenant_id INTEGER, deleted_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	scope := &ExecutionScope{AccountID: "account", ScopeID: "group", KnowledgeBaseIDs: []string{"kb"}, Revision: "r", AllowSandboxShell: true}
	channel := &IMChannel{ID: "channel", TenantID: 7, AgentID: "agent", Platform: "octo", Enabled: true, Mode: "websocket", SessionMode: "user", Credentials: types.JSON(`{"account_id":"account","bot_uid":"bot"}`)}
	msg := &IncomingMessage{Platform: "octo", UserID: "user", ChatID: "group"}
	session := &types.Session{ID: "session", TenantID: 7}
	mapping := &ChannelSession{ID: "mapping", IMChannelID: "channel", TenantID: 7, AgentID: "agent", Platform: "octo", UserID: "user", ChatID: "group", SessionID: "session", Status: "active", Metadata: sessionScopeMetadata(scope)}
	agent := &types.CustomAgent{ID: "agent", TenantID: 7, Config: types.CustomAgentConfig{SandboxConfigID: "cfg", SkillsSelectionMode: "selected", SelectedSkills: []string{"allowed"}}}
	require.NoError(t, db.Exec(`INSERT INTO im_channels (id,tenant_id,agent_id,platform,enabled,mode,session_mode,credentials) VALUES (?,?,?,?,?,?,?,?)`, channel.ID, channel.TenantID, channel.AgentID, channel.Platform, channel.Enabled, channel.Mode, channel.SessionMode, []byte(channel.Credentials)).Error)
	require.NoError(t, db.Exec(`INSERT INTO sessions (id,tenant_id) VALUES (?,?)`, session.ID, session.TenantID).Error)
	require.NoError(t, db.Exec(`INSERT INTO im_channel_sessions (id,im_channel_id,tenant_id,agent_id,platform,user_id,chat_id,session_id,status,metadata) VALUES (?,?,?,?,?,?,?,?,?,?)`, mapping.ID, mapping.IMChannelID, mapping.TenantID, mapping.AgentID, mapping.Platform, mapping.UserID, mapping.ChatID, mapping.SessionID, mapping.Status, []byte(mapping.Metadata)).Error)
	adapter := &scopeTestAdapter{scope: scope}
	agents := &stubIMAgentService{agent: agent}
	svc := &Service{db: db, agentService: agents}
	ctx := withIMIdentity(context.Background(), channel, msg)
	ctx = types.WithIMKnowledgeScope(ctx, []string{"kb"})
	ctx = types.WithSessionID(ctx, session.ID)
	ctx = svc.withIMSandboxGrant(ctx, adapter, channel, msg, mapping, session, agent, scope)
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "cfg", grant.ConfigID)
	require.NoError(t, grant.Revalidate(ctx))

	// Generic file delivery may validate a matching existing sandbox without
	// enabling shell or installed skills. Delivery remains independently gated.
	fileScope := *scope
	fileScope.AllowSandboxShell = false
	fileScope.AllowFileDelivery = true
	adapter.scope = &fileScope
	require.NoError(t, db.Exec("UPDATE im_channel_sessions SET metadata = ? WHERE id = ?", []byte(sessionScopeMetadata(&fileScope)), mapping.ID).Error)
	fileCtx := svc.withIMSandboxGrant(ctx, adapter, channel, msg, mapping, session, agent, &fileScope)
	fileGrant, ok := types.IMSandboxGrantFromContext(fileCtx)
	require.True(t, ok)
	require.NoError(t, fileGrant.Revalidate(fileCtx))
	fileScope.AllowFileDelivery = false
	require.Error(t, fileGrant.Revalidate(fileCtx))
	fileScope.AllowFileDelivery = true
	adapter.scope = scope
	require.NoError(t, db.Exec("UPDATE im_channel_sessions SET metadata = ? WHERE id = ?", []byte(sessionScopeMetadata(scope)), mapping.ID).Error)

	adapter.err = errors.New("membership revoked")
	require.Error(t, grant.Revalidate(ctx))
	adapter.err = nil
	scope.AllowSandboxShell = false
	require.Error(t, grant.Revalidate(ctx))
	scope.AllowSandboxShell = true
	require.Error(t, grant.Revalidate(types.WithSessionID(ctx, "another")))
	require.Error(t, grant.Revalidate(types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalIMUser, ID: "other"})))
	require.NoError(t, db.Model(&IMChannel{}).Where("id = ?", channel.ID).Update("enabled", false).Error)
	require.Error(t, grant.Revalidate(ctx))
	require.NoError(t, db.Model(&IMChannel{}).Where("id = ?", channel.ID).Update("enabled", true).Error)
	require.NoError(t, db.Model(&ChannelSession{}).Where("id = ?", mapping.ID).Update("chat_id", "other").Error)
	require.Error(t, grant.Revalidate(ctx))
	require.NoError(t, db.Model(&ChannelSession{}).Where("id = ?", mapping.ID).Update("chat_id", "group").Error)
	agent.Config.SelectedSkills = []string{"other"}
	require.Error(t, grant.Revalidate(ctx))
	agent.Config.SelectedSkills = []string{"allowed"}
	agent.Config.SandboxConfigID = "other"
	require.Error(t, grant.Revalidate(ctx))
	agent.Config.SandboxConfigID = "cfg"
	require.NoError(t, grant.Revalidate(ctx))
	require.NoError(t, db.Delete(session).Error)
	require.Error(t, grant.Revalidate(ctx))
}

func TestIMSandboxGrantIsNotCreatedForLegacyOrHost(t *testing.T) {
	scope := &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r", AllowGeneratedFiles: true}
	channel := &IMChannel{ID: "ch", TenantID: 7, AgentID: "a", Platform: "octo"}
	msg := &IncomingMessage{Platform: "octo", UserID: "u", ChatID: "c"}
	session := &types.Session{ID: "s", TenantID: 7}
	mapping := &ChannelSession{ID: "m", SessionID: "s"}
	agent := &types.CustomAgent{ID: "a", TenantID: 7, Config: types.CustomAgentConfig{SandboxConfigID: "host"}}
	svc := &Service{db: &gorm.DB{}, agentService: &stubIMAgentService{agent: agent}}
	for _, allow := range []bool{false, true} {
		scope.AllowSandboxShell = allow
		ctx := withIMIdentity(context.Background(), channel, msg)
		ctx = svc.withIMSandboxGrant(ctx, &scopeTestAdapter{scope: scope}, channel, msg, mapping, session, agent, scope)
		_, ok := types.IMSandboxGrantFromContext(ctx)
		require.False(t, ok)
	}
}
