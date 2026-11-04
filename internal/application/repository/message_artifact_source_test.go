package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestArtifactLibrarySourceFiltersAndOptions(t *testing.T) {
	db := newArtifactTestDB(t)
	for _, sql := range []string{
		`CREATE TABLE im_channel_web_owners (
			tenant_id integer, im_channel_id text, web_user_id text, deleted_at datetime)`,
		`CREATE TABLE im_channels (id text, tenant_id integer, platform text, name text, deleted_at datetime)`,
		`CREATE TABLE tenant_members (tenant_id integer, user_id text, status text, deleted_at datetime)`,
		`CREATE TABLE users (id text, is_active boolean, deleted_at datetime)`,
		`INSERT INTO im_channels VALUES ('a',7,'octo','A',NULL),('b',7,'octo','B',NULL),
			('secret',7,'octo','Secret',NULL),('other',8,'octo','Other',NULL),
			('empty',7,'octo','Empty',NULL),('unsupported',7,'feishu','Feishu',NULL)`,
		`INSERT INTO users VALUES ('alice',1,NULL),('bob',1,NULL)`,
		`INSERT INTO tenant_members VALUES (7,'alice','active',NULL),(7,'bob','active',NULL)`,
		`INSERT INTO im_channel_web_owners VALUES (7,'a','alice',NULL),(7,'b','alice',NULL),
			(7,'empty','alice',NULL),(7,'unsupported','alice',NULL),
			(7,'secret','bob',NULL),(8,'other','alice',NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	repo := NewMessageRepository(db)
	ctx := context.Background()
	for _, channel := range []string{"", "a", "b", "secret"} {
		sid := createSession(t, db, &types.Session{TenantID: 7, UserID: "alice", Title: channel})
		if channel != "" {
			mapping := &testIMChannelSession{
				ID: channel, SessionID: sid, TenantID: 7, IMChannelID: channel, Platform: "octo",
			}
			require.NoError(t, db.Create(mapping).Error)
			if channel == "b" {
				require.NoError(t, db.Delete(mapping).Error)
			}
			if channel == "a" {
				// A second, unauthorized mapping must not expose its ID or duplicate the row.
				require.NoError(t, db.Create(&testIMChannelSession{
					ID: "foreign-mapping", SessionID: sid, TenantID: 7, IMChannelID: "secret", Platform: "octo",
				}).Error)
			}
		}
		_, err := repo.CreateMessage(ctx, &types.Message{
			SessionID: sid, Role: "assistant",
			Artifacts: types.MessageArtifacts{testArtifact(channel+".pptx", channel, time.Now())},
		})
		require.NoError(t, err)
	}
	list := func(source, channel string) ([]*types.ArtifactLibraryItem, int64, []types.ArtifactSourceOption) {
		t.Helper()
		q := &types.ArtifactLibraryQuery{
			TenantID: 7, UserID: "alice", WebOwnerID: "alice", Source: source,
			ChannelID: channel, PageSize: 20, IncludeSourceOptions: true,
		}
		items, total, err := repo.ListArtifactLibrary(ctx, q)
		require.NoError(t, err)
		return items, total, q.SourceOptions
	}
	items, total, options := list("", "")
	require.EqualValues(t, 3, total)
	require.Len(t, items, 3)
	usage := repo.(interface {
		ArtifactUsage(context.Context, *types.ArtifactLibraryQuery) (int64, int64, error)
	})
	count, bytes, err := usage.ArtifactUsage(ctx, &types.ArtifactLibraryQuery{
		TenantID: 7, UserID: "alice", WebOwnerID: "alice",
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, count)
	require.EqualValues(t, 3*1024, bytes)
	require.Equal(t, []types.ArtifactSourceOption{
		{Source: "web"},
		{Source: "channel", ChannelID: "a", Platform: "octo", Name: "A"},
		{Source: "channel", ChannelID: "b", Platform: "octo", Name: "B"},
		{Source: "channel", ChannelID: "empty", Platform: "octo", Name: "Empty"},
	}, options)
	for _, item := range items {
		if item.Source == "web" {
			require.Empty(t, item.ChannelID)
		} else {
			require.Contains(t, []string{"a", "b"}, item.ChannelID)
			require.Equal(t, "octo", item.ChannelPlatform)
		}
	}
	_, total, _ = list("web", "")
	require.EqualValues(t, 1, total)
	_, total, _ = list("channel", "")
	require.EqualValues(t, 2, total)
	items, total, _ = list("channel", "b")
	require.EqualValues(t, 1, total)
	require.Equal(t, "b", items[0].ChannelID)
	_, total, _ = list("channel", "secret")
	require.Zero(t, total)
	_, total, _ = list("web", "a")
	require.Zero(t, total)
	require.NoError(t, db.Exec(`UPDATE im_channel_web_owners SET deleted_at=CURRENT_TIMESTAMP
		WHERE im_channel_id='b'`).Error)
	_, total, options = list("channel", "b")
	require.Zero(t, total)
	require.Len(t, options, 3)
	require.NoError(t, db.Exec(`UPDATE users SET is_active=0 WHERE id='alice'`).Error)
	_, total, options = list("channel", "")
	require.Zero(t, total)
	require.Len(t, options, 1)

	// An option lookup failure must fail the request, not silently omit channels.
	require.NoError(t, db.Exec(`DROP TABLE im_channels`).Error)
	_, _, err = repo.ListArtifactLibrary(ctx, &types.ArtifactLibraryQuery{
		TenantID: 7, UserID: "alice", WebOwnerID: "alice", IncludeSourceOptions: true,
	})
	require.Error(t, err)
}
