package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChannelArtifactGrantAndRevocation(t *testing.T) {
	db := newArtifactTestDB(t)
	for _, sql := range []string{
		`CREATE TABLE im_channel_web_owners (tenant_id integer, im_channel_id text,
			web_user_id text, deleted_at datetime)`,
		`CREATE TABLE im_channels (id text, tenant_id integer, platform text, deleted_at datetime)`,
		`CREATE TABLE tenant_members (tenant_id integer, user_id text, status text, deleted_at datetime)`,
		`CREATE TABLE users (id text, is_active boolean, deleted_at datetime)`,
		`INSERT INTO im_channels VALUES ('channel', 7, 'octo', NULL), ('other', 7, 'octo', NULL)`,
		`INSERT INTO users VALUES ('alice', 1, NULL), ('bob', 1, NULL)`,
		`INSERT INTO tenant_members VALUES (7, 'alice', 'active', NULL), (7, 'bob', 'active', NULL)`,
		`INSERT INTO im_channel_web_owners VALUES (7, 'channel', 'alice', NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	repo := NewMessageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	var ids []string
	for _, tc := range []struct {
		name, channel string
		deleted       bool
	}{{"private", "channel", false}, {"group", "channel", true}, {"other", "other", false}} {
		sid := createSession(t, db, &types.Session{TenantID: 7, Title: tc.name})
		mapping := &testIMChannelSession{
			ID:          tc.name,
			SessionID:   sid,
			TenantID:    7,
			IMChannelID: tc.channel,
			Platform:    "octo",
		}
		require.NoError(t, db.Create(mapping).Error)
		if tc.deleted {
			require.NoError(t, db.Delete(mapping).Error)
		}
		msg, err := repo.CreateMessage(
			ctx,
			&types.Message{
				SessionID: sid,
				Role:      "assistant",
				Artifacts: types.MessageArtifacts{testArtifact(tc.name+".pptx", tc.name, now)},
			},
		)
		require.NoError(t, err)
		ids = append(ids, sid, msg.ID)
	}
	list := func(user string) int64 {
		items, total, err := repo.ListArtifactLibrary(
			ctx,
			&types.ArtifactLibraryQuery{TenantID: 7, UserID: user, WebOwnerID: user, Page: 1, PageSize: 20},
		)
		require.NoError(t, err)
		for _, item := range items {
			require.True(t, item.IMChannelArtifact)
		}
		return total
	}
	require.EqualValues(t, 2, list("alice"))
	require.Zero(t, list("bob"))
	lookup := repo.(interface {
		GetChannelArtifactMessage(context.Context, uint64, string, string, string, int) (*types.Message, error)
	})
	_, err := lookup.GetChannelArtifactMessage(ctx, 7, "alice", ids[0], ids[1], 0)
	require.NoError(t, err)
	for _, tc := range []struct {
		tenant                 uint64
		user, session, message string
		index                  int
	}{
		{7, "bob", ids[0], ids[1], 0},
		{8, "alice", ids[0], ids[1], 0},
		{7, "alice", ids[4], ids[5], 0},
		{7, "alice", ids[0], ids[3], 0},
		{7, "alice", ids[0], ids[1], 1},
	} {
		_, err = lookup.GetChannelArtifactMessage(ctx, tc.tenant, tc.user, tc.session, tc.message, tc.index)
		require.Error(t, err)
	}
	for _, mutations := range [][2]string{
		{`UPDATE users SET is_active = 0 WHERE id = 'alice'`, `UPDATE users SET is_active = 1 WHERE id = 'alice'`},
		{
			`UPDATE tenant_members SET status = 'inactive' WHERE user_id = 'alice'`,
			`UPDATE tenant_members SET status = 'active' WHERE user_id = 'alice'`,
		},
		{
			`UPDATE im_channels SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'channel'`,
			`UPDATE im_channels SET deleted_at = NULL WHERE id = 'channel'`,
		},
	} {
		require.NoError(t, db.Exec(mutations[0]).Error)
		require.Zero(t, list("alice"))
		_, err = lookup.GetChannelArtifactMessage(ctx, 7, "alice", ids[0], ids[1], 0)
		require.Error(t, err)
		require.NoError(t, db.Exec(mutations[1]).Error)
	}
	require.NoError(t, db.Exec(`UPDATE im_channel_web_owners SET deleted_at = CURRENT_TIMESTAMP`).Error)
	require.Zero(t, list("alice"))
	_, err = lookup.GetChannelArtifactMessage(ctx, 7, "alice", ids[0], ids[1], 0)
	require.Error(t, err)
}

func TestWebArtifactLibraryWorksBeforeChannelOwnerMigration(t *testing.T) {
	db := newArtifactTestDB(t)
	repo := NewMessageRepository(db)
	ctx := context.Background()
	sid := createSession(t, db, &types.Session{TenantID: 7, UserID: "alice"})
	_, err := repo.CreateMessage(
		ctx,
		&types.Message{
			SessionID: sid,
			Role:      "assistant",
			Artifacts: types.MessageArtifacts{testArtifact("web.pptx", "web", time.Now())},
		},
	)
	require.NoError(t, err)
	items, total, err := repo.ListArtifactLibrary(
		ctx,
		&types.ArtifactLibraryQuery{TenantID: 7, UserID: "alice", WebOwnerID: "alice", Page: 1, PageSize: 20},
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	require.False(t, items[0].IMChannelArtifact)
}
