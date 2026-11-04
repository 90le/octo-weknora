package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestArtifactUsageUsesLibraryScopeAndCountsVersions(t *testing.T) {
	db := newArtifactTestDB(t)
	for _, sql := range []string{
		`CREATE TABLE im_channel_web_owners (
			tenant_id integer, im_channel_id text, web_user_id text, deleted_at datetime)`,
		`CREATE TABLE im_channels (id text, tenant_id integer, deleted_at datetime)`,
		`CREATE TABLE tenant_members (tenant_id integer, user_id text, status text, deleted_at datetime)`,
		`CREATE TABLE users (id text, is_active boolean, deleted_at datetime)`,
		`INSERT INTO im_channels VALUES ('channel', 7, NULL)`,
		`INSERT INTO users VALUES ('alice', 1, NULL), ('bob', 1, NULL)`,
		`INSERT INTO tenant_members VALUES (7, 'alice', 'active', NULL), (7, 'bob', 'active', NULL)`,
		`INSERT INTO im_channel_web_owners VALUES (7, 'channel', 'alice', NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	repo := NewMessageRepository(db)
	usage := repo.(interface {
		ArtifactUsage(context.Context, *types.ArtifactLibraryQuery) (int64, int64, error)
	})
	ctx := context.Background()
	sid := createSession(t, db, &types.Session{TenantID: 7})
	require.NoError(t, db.Create(&testIMChannelSession{
		ID: "mapping", SessionID: sid, TenantID: 7, IMChannelID: "channel", Platform: "octo",
	}).Error)
	for range 2 {
		_, err := repo.CreateMessage(ctx, &types.Message{
			SessionID: sid, Role: "assistant",
			Artifacts: types.MessageArtifacts{testArtifact("same.pptx", "same", time.Now())},
		})
		require.NoError(t, err)
	}
	alice := &types.ArtifactLibraryQuery{TenantID: 7, UserID: "alice", WebOwnerID: "alice"}
	count, bytes, err := usage.ArtifactUsage(ctx, alice)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	require.EqualValues(t, 2048, bytes)
	bob := &types.ArtifactLibraryQuery{TenantID: 7, UserID: "bob", WebOwnerID: "bob"}
	count, bytes, err = usage.ArtifactUsage(ctx, bob)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Zero(t, bytes)
	require.NoError(t, db.Exec(`UPDATE im_channel_web_owners SET deleted_at = CURRENT_TIMESTAMP`).Error)
	count, _, err = usage.ArtifactUsage(ctx, alice)
	require.NoError(t, err)
	require.Zero(t, count)
}
