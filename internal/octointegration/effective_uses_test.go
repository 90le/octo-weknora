package octointegration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEffectiveUsesShowsFinalReadAndExactManagementWithoutLeakingGroups(t *testing.T) {
	s, ctx := testStore(t), context.Background()
	parent := createScope(t, s, 1, "bot", "group", "", false)
	child := createScope(t, s, 1, "bot", "group", "topic", true)
	grantOnly := createScope(t, s, 1, "bot", "other-group", "", false)
	otherAccount := createScope(t, s, 1, "other-bot", "group", "", false)
	otherTenant := createScope(t, s, 2, "bot", "group", "", false)
	verifiedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.db.Model(&Scope{}).Where("id = ?", parent.ID).Updates(map[string]any{"name_source": "octo", "sync_status": "verified", "verified_at": verifiedAt}).Error)
	require.NoError(t, s.SetBinding(ctx, 1, parent.ID, "kb-a", true, true))
	require.NoError(t, s.SetBinding(ctx, 1, grantOnly.ID, "kb-a", true, true))
	require.NoError(t, s.SetBinding(ctx, 1, grantOnly.ID, "kb-a", false))

	uses, err := s.EffectiveUses(ctx, 1, "kb-a")
	require.NoError(t, err)
	require.Len(t, uses, 3)
	byID := make(map[string]EffectiveUse, len(uses))
	for _, use := range uses {
		byID[use.ScopeID] = use
	}
	require.Equal(t, parent.DisplayName, byID[parent.ID].DisplayName)
	require.Equal(t, "bot", byID[parent.ID].AccountID)
	require.Equal(t, "group", byID[parent.ID].GroupID)
	require.Equal(t, "direct", byID[parent.ID].QueryMode)
	require.Equal(t, parent.ID, byID[parent.ID].FromScopeID)
	require.True(t, byID[parent.ID].CanManage)
	require.Equal(t, "octo", byID[parent.ID].NameSource)
	require.Equal(t, "verified", byID[parent.ID].SyncStatus)
	require.NotNil(t, byID[parent.ID].VerifiedAt)
	require.WithinDuration(t, verifiedAt, *byID[parent.ID].VerifiedAt, time.Second)
	require.Equal(t, "inherited", byID[child.ID].QueryMode)
	require.Equal(t, parent.ID, byID[child.ID].FromScopeID)
	require.False(t, byID[child.ID].CanManage, "parent management must not be inherited")
	require.Equal(t, "none", byID[grantOnly.ID].QueryMode)
	require.Empty(t, byID[grantOnly.ID].FromScopeID)
	require.True(t, byID[grantOnly.ID].CanManage)
	require.NotContains(t, byID, otherAccount.ID, "same group in another Bot account cannot inherit")
	require.NotContains(t, byID, otherTenant.ID, "same group in another tenant cannot inherit")

	require.NoError(t, s.SetBinding(ctx, 1, child.ID, "kb-a", true, true))
	uses, err = s.EffectiveUses(ctx, 1, "kb-a")
	require.NoError(t, err)
	require.Len(t, uses, 3, "direct and inherited paths must not duplicate the child")
	for _, use := range uses {
		if use.ScopeID == child.ID {
			require.Equal(t, "direct", use.QueryMode)
			require.Equal(t, child.ID, use.FromScopeID)
			require.True(t, use.CanManage)
		}
	}
	require.ErrorIs(t, func() error { _, err := s.EffectiveUses(ctx, 2, "kb-a"); return err }(), gorm.ErrRecordNotFound)
	require.ErrorIs(t, func() error { _, err := s.EffectiveUses(ctx, 1, "kb-b"); return err }(), gorm.ErrRecordNotFound)
	require.ErrorIs(t, func() error { _, err := s.EffectiveUses(ctx, 0, "kb-a"); return err }(), ErrInvalid)

	require.NoError(t, s.db.Exec("UPDATE knowledge_bases SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'kb-a'").Error)
	_, err = s.EffectiveUses(ctx, 1, "kb-a")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
