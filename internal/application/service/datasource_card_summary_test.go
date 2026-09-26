package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type cardSourceRepo struct {
	interfaces.DataSourceRepository
	rows []*types.DataSource
}

func (r cardSourceRepo) FindByKnowledgeBase(context.Context, string) ([]*types.DataSource, error) {
	return r.rows, nil
}

type cardSummaryRepo struct {
	interfaces.SyncLogRepository
	bulkCalls int
}

func (r *cardSummaryRepo) FindCardSummaries(_ context.Context, ids []string) (map[string]*types.SyncLog, map[string]*time.Time, error) {
	r.bulkCalls++
	if len(ids) != 2 {
		panic("expected both source IDs in one batch")
	}
	success := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	return map[string]*types.SyncLog{
		"a": {ID: "a-partial", DataSourceID: "a", Status: types.SyncLogStatusPartial, ItemsFailed: 212},
		"b": {ID: "b-failed", DataSourceID: "b", Status: types.SyncLogStatusFailed},
	}, map[string]*time.Time{"a": &success}, nil
}

func (r *cardSummaryRepo) FindLatest(context.Context, string) (*types.SyncLog, error) {
	panic("ListDataSources must not query one log per source when bulk summary is available")
}

func TestListDataSourcesUsesOneBulkCardSummaryInsteadOfNPlusOne(t *testing.T) {
	logs := &cardSummaryRepo{}
	svc := &DataSourceService{
		dsRepo:      cardSourceRepo{rows: []*types.DataSource{{ID: "a"}, {ID: "b"}}},
		syncLogRepo: logs,
	}
	rows, err := svc.ListDataSources(context.Background(), "kb")
	require.NoError(t, err)
	require.Equal(t, 1, logs.bulkCalls)
	require.Equal(t, types.SyncLogStatusPartial, rows[0].LatestSyncLog.Status)
	require.Equal(t, 212, rows[0].LatestSyncLog.ItemsFailed)
	require.NotNil(t, rows[0].LastSuccessfulSyncAt)
	require.Equal(t, types.SyncLogStatusFailed, rows[1].LatestSyncLog.Status)
	require.Nil(t, rows[1].LastSuccessfulSyncAt)
}
