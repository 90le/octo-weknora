package octobusiness

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/stretchr/testify/require"
)

func TestManualReplySelectsTheQuotedMessageAndItsAuthorTogether(t *testing.T) {
	s := testService(t)
	p := testPrincipal()
	now := time.Now()
	scope := octointegration.Scope{
		ID: p.ScopeID, TenantID: p.TenantID, AccountID: p.AccountID, GroupID: p.GroupID,
		VerifiedAt: &now, NameSource: "octo", SyncStatus: "verified",
	}
	kb := p.KnowledgeBaseIDs[0]
	binding := octointegration.Binding{TenantID: p.TenantID, ScopeID: scope.ID, KnowledgeBaseID: kb}
	require.NoError(t, s.db.AutoMigrate(&scope, &binding, &octointegration.KnowledgeManagementGrant{}))
	require.NoError(t, s.db.Create(&scope).Error)
	require.NoError(t, s.db.Create(&binding).Error)
	for _, ddl := range []string{
		"CREATE TABLE custom_agents (id TEXT, tenant_id INT, deleted_at DATETIME, config TEXT)",
		"CREATE TABLE im_channels (id TEXT, tenant_id INT, agent_id TEXT, platform TEXT," +
			" enabled BOOLEAN, deleted_at DATETIME, credentials TEXT)",
	} {
		require.NoError(t, s.db.Exec(ddl).Error)
	}
	require.NoError(t, s.db.Exec("INSERT INTO custom_agents VALUES ('agent',1,NULL,?)",
		`{"agent_mode":"smart-reasoning"}`).Error)
	require.NoError(t, s.db.Exec("INSERT INTO im_channels VALUES (?,1,'agent','octo',TRUE,NULL,?)",
		p.ChannelID, `{"account_id":"bot"}`).Error)

	const insert = `INSERT INTO octo_message_inbox (tenant_id,channel_id,message_id,input,authority,state)
		VALUES (1,?,?,?,?,'delivered')`
	const authority = `{"AccountID":"bot","ScopeID":"scope-A","KnowledgeBaseIDs":["kb-a"]}`
	feedback := map[string]any{
		"UserID": "bob", "Content": "反馈", "Quote": map[string]any{
			"MessageID": "20", "SenderID": "bot", "IsBotMessage": true,
		},
		"Extra": map[string]string{
			"octo_message_id": "30", "octo_group_id": scope.GroupID,
			"octo_original_payload": `{"reply":{"message_id":"20","payload":` +
				`{"reply":{"message_id":"10","from_uid":"alice"}}}}`,
		},
	}
	raw, err := json.Marshal(feedback)
	require.NoError(t, err)
	require.NoError(t, s.db.Exec(insert, p.ChannelID, "octo:bot:30", string(raw), authority).Error)
	const question = `{"UserID":"alice","Content":"原始问题","Extra":{"octo_group_id":"group-A","octo_message_id":"10"}}`
	require.NoError(t, s.db.Exec(insert, p.ChannelID, "octo:bot:10", question, authority).Error)

	issue := Issue{
		TenantID: p.TenantID, KnowledgeBaseID: kb, AccountID: p.AccountID, ScopeID: scope.ID,
		ChannelID: p.ChannelID, GroupID: scope.GroupID, ReporterUID: "bob", MessageID: "octo:bot:30",
	}
	for _, tc := range []struct{ target, id, uid, text string }{
		{"feedback", "30", "bob", "反馈"},
		{"question", "10", "alice", "原始问题"},
	} {
		target, msg, err := s.manualReplyTarget(consoleTestContext(), &issue, tc.target)
		require.NoError(t, err)
		require.Equal(t, "octo:bot:"+tc.id, target.InboxID)
		require.Equal(t, tc.id, msg.Extra["octo_message_id"])
		require.Equal(t, tc.uid, msg.UserID)
		require.Equal(t, tc.text, msg.Content)
	}
	require.NoError(t, s.db.Exec("DELETE FROM octo_message_inbox WHERE message_id = 'octo:bot:10'").Error)
	_, _, err = s.manualReplyTarget(consoleTestContext(), &issue, "question")
	require.ErrorIs(t, err, errReplySource)
}
