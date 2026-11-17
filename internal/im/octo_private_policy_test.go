package im

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestOctoPrivatePreviewAndHotSave(t *testing.T) {
	ctx := context.Background()
	db := newLifecycleTestDB(t)
	require.NoError(t, db.AutoMigrate(&octointegration.Connection{}))
	require.NoError(t, db.Exec("CREATE TABLE knowledge_bases (id TEXT, tenant_id INTEGER, deleted_at DATETIME)").Error)
	require.NoError(t, db.Exec("INSERT INTO knowledge_bases VALUES ('kb', 1, NULL), ('foreign', 2, NULL)").Error)
	require.NoError(t, db.Create(&octointegration.Connection{
		TenantID: 1, AccountID: "account", Token: "encrypted-fixture",
		VerifiedIdentity: types.JSON(`{"bot_uid":"bot","owner_uid":"owner"}`),
	}).Error)
	channel := &IMChannel{
		ID: "channel", TenantID: 1, AgentID: "agent", Platform: "octo", Enabled: true, KnowledgeBaseID: "kb",
		Mode: "websocket", SessionMode: "user",
		Credentials: types.JSON(`{"account_id":"account","bot_uid":"bot","allowed_dm_uids":["peer","owner"],
			"allowed_bot_uids":["group-bot"],"allow_sandbox_shell":true}`),
	}
	require.NoError(t, db.Create(channel).Error)
	counters := &lifecycleFactoryCounters{}
	svc := newLifecycleTestService(db, nil, "octo-private-test")
	svc.RegisterAdapterFactory("octo", counters.factory())
	t.Cleanup(svc.Stop)
	require.NoError(t, svc.StartChannel(channel))
	original, err := svc.GetChannelByIDAndTenant(channel.ID, 1)
	require.NoError(t, err)
	legacyConfig, err := ParseCredentials(original.Credentials)
	require.NoError(t, err)
	legacy, err := ReadOctoPrivatePolicy(original, legacyConfig)
	require.NoError(t, err)
	require.Equal(t, "allowed", legacy.Decision("peer", ""))
	require.True(t, legacy.Tools.Shell)

	next := *original
	next.Credentials = types.JSON(`{"account_id":"account","bot_uid":"bot","allowed_dm_uids":[],
		"dm_access_mode":"creator_only","dm_allowlist_uids":["peer","owner"],"blocked_dm_uids":["owner"],
		"allowed_bot_uids":["group-bot"],"allow_sandbox_shell":true,"dm_knowledge_base_ids":["kb"],
		"dm_tools":{"allow_file_delivery":false,"allow_sandbox_shell":false,
			"allow_sandbox_skills":false,"allow_generated_files":false}}`)
	preview, err := svc.OctoPrivatePolicyPreview(ctx, original, &next, "owner")
	require.NoError(t, err)
	require.Equal(t, "blocked", preview.Decision)
	require.Equal(t, "not_creator", preview.Policy.Decision("peer", preview.OwnerUID))
	require.Equal(t, "allowed", legacy.Decision("owner", preview.OwnerUID))
	require.Empty(t, preview.Problem)
	require.NotEmpty(t, preview.Token)
	require.ErrorIs(t, svc.PrepareOctoPrivateSave(ctx, original, &next, ""), ErrOctoPrivatePreview)

	tampered := next
	tampered.Credentials = types.JSON(`{"account_id":"account","bot_uid":"bot","dm_access_mode":"allowlist",
		"dm_allowlist_uids":["peer"],"dm_knowledge_base_ids":["kb"]}`)
	require.ErrorIs(t, svc.PrepareOctoPrivateSave(ctx, original, &tampered, preview.Token), ErrOctoPrivatePreview)
	tampered.Credentials = types.JSON(`{"account_id":"account","bot_uid":"bot","dm_access_mode":"creator_only",
		"dm_knowledge_base_ids":["foreign"]}`)
	foreign, err := svc.OctoPrivatePolicyPreview(ctx, original, &tampered, "owner")
	require.NoError(t, err)
	require.Equal(t, "invalid_knowledge", foreign.Decision)
	require.Empty(t, foreign.Token)

	require.NoError(t, svc.PrepareOctoPrivateSave(ctx, original, &next, preview.Token))
	require.NoError(t, svc.UpdateChannel(&next))
	svc.reloadChannelFromDB(channel.ID, "test replica reload")
	require.EqualValues(t, 1, counters.starts.Load())
	require.Zero(t, counters.stops.Load())
	saved, err := svc.GetChannelByIDAndTenant(channel.ID, 1)
	require.NoError(t, err)
	stored, err := ParseCredentials(saved.Credentials)
	require.NoError(t, err)
	require.Empty(t, stored["allowed_dm_uids"], "rollback readers fail closed")
	require.Equal(t, []any{"group-bot"}, stored["allowed_bot_uids"])
	require.True(t, GetExplicitBool(stored, "allow_sandbox_shell"), "group tool grant retained")
	after, err := ReadOctoPrivatePolicy(saved, stored)
	require.NoError(t, err)
	require.Equal(t, []string{"owner", "peer"}, after.AllowedUIDs)
	require.False(t, after.Tools.Shell)
	require.ErrorIs(t, svc.PrepareOctoPrivateSave(ctx, saved, &next, preview.Token), ErrOctoPrivatePreview)
	require.ErrorIs(t, svc.UpdateChannel(original), ErrOctoPrivatePreview, "stale row cannot overwrite the policy")
}
