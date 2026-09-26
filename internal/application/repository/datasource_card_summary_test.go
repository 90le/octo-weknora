package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestFindCardSummariesBatchesLatestAttemptAndLastFullSuccess(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "source-cards.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.SyncLog{}))
	now := time.Now().UTC().Truncate(time.Second)
	oldSuccess := now.Add(-24 * time.Hour)
	latestPartial := now.Add(-time.Hour)
	latestFailure := now.Add(-30 * time.Minute)
	for _, log := range []types.SyncLog{
		{ID: "a-success", DataSourceID: "a", Status: types.SyncLogStatusSuccess,
			StartedAt: oldSuccess.Add(-time.Minute), FinishedAt: &oldSuccess, ItemsTotal: 10},
		{ID: "a-partial", DataSourceID: "a", Status: types.SyncLogStatusPartial,
			StartedAt: latestPartial.Add(-time.Minute), FinishedAt: &latestPartial, ItemsTotal: 500, ItemsFailed: 212},
		{ID: "b-failed", DataSourceID: "b", Status: types.SyncLogStatusFailed,
			StartedAt: latestFailure.Add(-time.Minute), FinishedAt: &latestFailure, ErrorMessage: "safe failure"},
	} {
		require.NoError(t, db.Create(&log).Error)
	}
	repo := NewSyncLogRepository(db).(*SyncLogRepository)
	latest, successes, err := repo.FindCardSummaries(context.Background(), []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Equal(t, "a-partial", latest["a"].ID)
	require.Equal(t, 212, latest["a"].ItemsFailed)
	require.Equal(t, "b-failed", latest["b"].ID)
	require.NotContains(t, latest, "c")
	require.NotNil(t, successes["a"])
	require.True(t, successes["a"].Equal(oldSuccess))
	require.NotContains(t, successes, "b")
	require.NotContains(t, successes, "c")
}
