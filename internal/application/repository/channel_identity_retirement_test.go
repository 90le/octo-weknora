package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChannelIdentityRetirementKeepsCurrentHistoryOnly(t *testing.T) {
	db := newArtifactTestDB(t)
	for _, sql := range []string{
		`CREATE TABLE im_channels(id TEXT PRIMARY KEY, tenant_id INTEGER, platform TEXT,
			credentials TEXT, deleted_at DATETIME)`,
		`CREATE TABLE audit_logs(id INTEGER PRIMARY KEY, tenant_id INTEGER, action TEXT, target_type TEXT,
			target_id TEXT, target_user_id TEXT, outcome TEXT)`,
		`CREATE TABLE tenant_members(tenant_id INTEGER, user_id TEXT, status TEXT, deleted_at DATETIME)`,
		`CREATE TABLE users(id TEXT, is_active BOOLEAN, deleted_at DATETIME)`,
		`INSERT INTO im_channels VALUES ('channel',7,'octo','{"account_id":"a","bot_uid":"b"}',NULL)`,
		`INSERT INTO tenant_members VALUES (7,'alice','active',NULL)`,
		`INSERT INTO users VALUES ('alice',1,NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	migration, err := os.ReadFile("../../../migrations/sqlite/000052_im_channel_web_owners.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	bind := func(id string) {
		require.NoError(
			t,
			db.Exec(
				`INSERT INTO im_channel_web_owners(id,tenant_id,im_channel_id,web_user_id)
				 VALUES (?,7,'channel','alice')`,
				id,
			).Error,
		)
	}
	bind("owner-old")
	ctx := context.Background()
	repo := NewMessageRepository(db)
	add := func(id string, deleted bool) (string, string) {
		sid := createSession(t, db, &types.Session{TenantID: 7})
		mapping := &testIMChannelSession{
			ID:          id,
			SessionID:   sid,
			TenantID:    7,
			IMChannelID: "channel",
			Platform:    "octo",
		}
		require.NoError(t, db.Create(mapping).Error)
		if deleted {
			require.NoError(t, db.Delete(mapping).Error)
		}
		msg, err := repo.CreateMessage(
			ctx,
			&types.Message{
				SessionID: sid,
				Role:      "assistant",
				Artifacts: types.MessageArtifacts{testArtifact(id+".txt", id, time.Now())},
			},
		)
		require.NoError(t, err)
		return sid, msg.ID
	}
	oldSession, oldMessage := add("active-old", false)
	clearedSession, clearedMessage := add("cleared-old", true)
	lookup := repo.(interface {
		GetChannelArtifactMessage(context.Context, uint64, string, string, string, int) (*types.Message, error)
	})
	list := func() int64 {
		_, total, err := repo.ListArtifactLibrary(
			ctx,
			&types.ArtifactLibraryQuery{TenantID: 7, UserID: "alice", WebOwnerID: "alice", Page: 1, PageSize: 20},
		)
		require.NoError(t, err)
		return total
	}
	require.EqualValues(t, 2, list())
	require.NoError(
		t,
		db.Exec(`UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"b","option":true}'`).Error,
	)
	require.EqualValues(t, 2, list())
	require.NoError(t, db.Exec(`UPDATE im_channels SET credentials='{"account_id":"new","bot_uid":"b"}'`).Error)
	bind("owner-new")
	newSession, newMessage := add("new", false)
	require.EqualValues(t, 1, list())
	for _, pair := range [][2]string{{oldSession, oldMessage}, {clearedSession, clearedMessage}} {
		_, err := lookup.GetChannelArtifactMessage(ctx, 7, "alice", pair[0], pair[1], 0)
		require.Error(t, err)
	}
	_, err = lookup.GetChannelArtifactMessage(ctx, 7, "alice", newSession, newMessage, 0)
	require.NoError(t, err)
	var count int64
	require.NoError(
		t,
		db.Table("im_channel_session_identity_retirements").
			Where("im_channel_id = ?", "channel").
			Count(&count).
			Error,
	)
	require.EqualValues(t, 2, count)
}
