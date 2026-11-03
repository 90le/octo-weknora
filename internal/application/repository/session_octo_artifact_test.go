package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestOctoArtifactOriginIncludesClearedMappings(t *testing.T) {
	db := newArtifactTestDB(t)
	ctx := context.Background()
	sessionID := createSession(t, db, &types.Session{TenantID: 7})
	otherID := createSession(t, db, &types.Session{TenantID: 7})
	row := &testIMChannelSession{ID: "octo-map", SessionID: sessionID, TenantID: 7, Platform: "octo"}
	require.NoError(t, db.Create(row).Error)
	require.NoError(t, db.Delete(row).Error)
	repo := NewSessionRepository(db).(interface {
		HasOctoSessionMapping(context.Context, uint64, string) (bool, error)
	})
	got, err := repo.HasOctoSessionMapping(ctx, 7, sessionID)
	require.NoError(t, err)
	require.True(t, got)
	got, err = repo.HasOctoSessionMapping(ctx, 7, otherID)
	require.NoError(t, err)
	require.False(t, got)
	got, err = repo.HasOctoSessionMapping(ctx, 8, sessionID)
	require.NoError(t, err)
	require.False(t, got)
}
