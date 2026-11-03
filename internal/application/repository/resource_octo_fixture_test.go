package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestOctoOriginUsesPersistedSessionAndMessageIDs(t *testing.T) {
	db := newArtifactTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.StoredResource{}, &types.ResourceBinding{}))
	ctx := context.Background()
	session := &types.Session{ID: "requested-session", TenantID: 7}
	require.NoError(t, db.Create(session).Error)
	require.NotEqual(t, "requested-session", session.ID)
	require.NoError(
		t,
		db.Create(
			&testIMChannelSession{
				ID:          "mapping",
				SessionID:   session.ID,
				TenantID:    7,
				Platform:    "octo",
				IMChannelID: "channel",
			},
		).Error,
	)
	resource := &types.StoredResource{
		Handle:       "abcdefghijklmnopqrstuv",
		TenantID:     7,
		Provider:     "local",
		PhysicalPath: "local://7/exports/image.png",
		LocationHash: "location",
	}
	require.NoError(t, db.Create(resource).Error)
	ref := types.BuildResourcePath(resource.Handle)
	message, err := NewMessageRepository(
		db,
	).CreateMessage(ctx, &types.Message{
		ID: "requested-message", SessionID: session.ID, Artifacts: types.MessageArtifacts{{URL: ref}},
	})
	require.NoError(t, err)
	require.NotEqual(t, "requested-message", message.ID)
	require.NoError(
		t,
		db.Create(
			&types.ResourceBinding{
				ResourceID: resource.ID,
				TenantID:   7,
				OwnerType:  types.ResourceOwnerMessage,
				OwnerID:    message.ID,
				Relation:   types.ResourceRelationArtifact,
			},
		).Error,
	)
	repo := NewResourceRepository(db).(*resourceRepository)
	channels, err := repo.OctoArtifactChannels(ctx, 7, resource)
	require.NoError(t, err)
	require.Equal(t, []string{"channel"}, channels)
}
