package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type failingMigrationScheduler struct{}

func (failingMigrationScheduler) AddOrUpdate(*types.DataSource) error {
	return errors.New("scheduler unavailable")
}
func (failingMigrationScheduler) Remove(string) {}

type migrationKBService struct {
	interfaces.KnowledgeBaseService
	knowledgeBases map[string]*types.KnowledgeBase
}

func (s migrationKBService) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return s.knowledgeBases[id], nil
}

type migrationFixture struct {
	service   *DataSourceService
	repo      interfaces.DataSourceRepository
	logs      interfaces.SyncLogRepository
	scheduler *datasource.Scheduler
}

func newMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "schedule.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}, &types.SyncLog{}))
	repo := repository.NewDataSourceRepository(db)
	logs := repository.NewSyncLogRepository(db)
	scheduler := datasource.NewScheduler(repo, logs, kbDeleteTaskEnqueuer{})
	service := &DataSourceService{
		dsRepo: repo, syncLogRepo: logs, scheduler: scheduler,
		kbService: migrationKBService{knowledgeBases: map[string]*types.KnowledgeBase{
			"kb-one":          {ID: "kb-one", TenantID: 1},
			"kb-two":          {ID: "kb-two", TenantID: 1},
			"kb-other-tenant": {ID: "kb-other-tenant", TenantID: 2},
		}},
	}
	return migrationFixture{service: service, repo: repo, logs: logs, scheduler: scheduler}
}

func (f migrationFixture) source(t *testing.T, id, kbID, schedule, status string, tenantID uint64) *types.DataSource {
	t.Helper()
	config, err := (&types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: map[string]interface{}{
		"repository": "Mininglamp-OSS/" + id, "mode": "documents",
	}}).ToJSON()
	require.NoError(t, err)
	ds := &types.DataSource{
		ID: id, TenantID: tenantID, KnowledgeBaseID: kbID, Type: types.ConnectorTypeGitHub,
		Status: status, SyncSchedule: schedule, Config: config,
	}
	require.NoError(t, f.repo.Create(context.Background(), ds))
	current, err := f.repo.FindByID(context.Background(), id)
	require.NoError(t, err)
	return current
}

func migrationSelection(item types.GitHubScheduleMigrationPreviewItem) types.GitHubScheduleMigrationSelection {
	return types.GitHubScheduleMigrationSelection{
		DataSourceID: item.DataSourceID, ExpectedSchedule: item.Current,
		ExpectedStatus: item.Status, ExpectedUpdatedAt: item.UpdatedAt, ExpectedProposed: item.Proposed,
	}
}

func TestGitHubScheduleMigrationPreviewScopesKBAndSkipsUnsafeRows(t *testing.T) {
	f := newMigrationFixture(t)
	f.source(t, "eligible", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	f.source(t, "custom", "kb-one", "0 30 */6 * * *", types.DataSourceStatusActive, 1)
	f.source(t, "manual", "kb-one", "", types.DataSourceStatusActive, 1)
	f.source(t, "paused", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusPaused, 1)
	f.source(t, "error", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusError, 1)
	f.source(t, "running", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	f.source(t, "other-kb", "kb-two", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	f.source(t, "other-tenant", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 2)
	require.NoError(t, f.logs.Create(context.Background(), &types.SyncLog{
		ID: "running-log", DataSourceID: "running", TenantID: 1, Status: types.SyncLogStatusRunning,
	}))
	preview, err := f.service.PreviewGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationPreviewRequest{
		TenantID: 1, KnowledgeBaseID: "kb-one",
	})
	require.NoError(t, err)
	require.Len(t, preview.Items, 6)
	reasons := map[string]string{}
	for _, item := range preview.Items {
		reasons[item.DataSourceID] = item.Reason
		if item.DataSourceID == "eligible" {
			require.True(t, item.Eligible)
			require.NotEmpty(t, item.Proposed)
			require.False(t, item.UpdatedAt.IsZero())
		}
	}
	require.Equal(t, "custom_schedule", reasons["custom"])
	require.Equal(t, "manual_schedule", reasons["manual"])
	require.Equal(t, "not_active", reasons["paused"])
	require.Equal(t, "not_active", reasons["error"])
	require.Equal(t, "running_sync", reasons["running"])
	require.NotContains(t, reasons, "other-kb")
	require.NotContains(t, reasons, "other-tenant")
	_, err = f.service.PreviewGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationPreviewRequest{
		TenantID: 2, KnowledgeBaseID: "kb-one",
	})
	require.ErrorContains(t, err, "not found")
}

func TestGitHubScheduleMigrationApplyCASAndIdempotence(t *testing.T) {
	f := newMigrationFixture(t)
	f.source(t, "eligible", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	f.source(t, "other-kb", "kb-two", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	preview, err := f.service.PreviewGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationPreviewRequest{TenantID: 1, KnowledgeBaseID: "kb-one"})
	require.NoError(t, err)
	choice := migrationSelection(preview.Items[0])
	// A client cannot smuggle an unrelated KB's source into this apply request.
	other := choice
	other.DataSourceID = "other-kb"
	response, err := f.service.ApplyGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationApplyRequest{
		TenantID: 1, KnowledgeBaseID: "kb-one", Selections: []types.GitHubScheduleMigrationSelection{other, choice},
	})
	require.NoError(t, err)
	require.Equal(t, "not_found", response.Results[0].Reason)
	require.Equal(t, "applied", response.Results[1].Status)
	require.Equal(t, choice.ExpectedProposed, response.Results[1].Schedule)
	current, err := f.repo.FindByID(context.Background(), "eligible")
	require.NoError(t, err)
	require.Equal(t, choice.ExpectedProposed, current.SyncSchedule)
	require.Equal(t, 1, f.scheduler.EntryCount())
	otherCurrent, err := f.repo.FindByID(context.Background(), "other-kb")
	require.NoError(t, err)
	require.Equal(t, defaultGitHubBatchSchedule, otherCurrent.SyncSchedule)
	// A second click against the same preview is a no-op, never a second write.
	again, err := f.service.ApplyGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationApplyRequest{
		TenantID: 1, KnowledgeBaseID: "kb-one", Selections: []types.GitHubScheduleMigrationSelection{choice},
	})
	require.NoError(t, err)
	require.Equal(t, "already_applied", again.Results[0].Reason)
	require.Equal(t, 1, f.scheduler.EntryCount())
}

func TestGitHubScheduleMigrationApplyRejectsStaleAndRunning(t *testing.T) {
	f := newMigrationFixture(t)
	f.source(t, "stale", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	f.source(t, "running", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	preview, err := f.service.PreviewGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationPreviewRequest{TenantID: 1, KnowledgeBaseID: "kb-one"})
	require.NoError(t, err)
	choices := make(map[string]types.GitHubScheduleMigrationSelection)
	for _, item := range preview.Items {
		choices[item.DataSourceID] = migrationSelection(item)
	}
	stale, err := f.repo.FindByID(context.Background(), "stale")
	require.NoError(t, err)
	stale.Name = "changed after preview"
	stale.UpdatedAt = time.Now().UTC().Add(time.Second)
	require.NoError(t, f.repo.Update(context.Background(), stale))
	require.NoError(t, f.logs.Create(context.Background(), &types.SyncLog{
		ID: "sync-started", DataSourceID: "running", TenantID: 1, Status: types.SyncLogStatusRunning,
	}))
	response, err := f.service.ApplyGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationApplyRequest{
		TenantID: 1, KnowledgeBaseID: "kb-one", Selections: []types.GitHubScheduleMigrationSelection{choices["stale"], choices["running"]},
	})
	require.NoError(t, err)
	require.Equal(t, "stale_preview", response.Results[0].Reason)
	require.Equal(t, "running_sync", response.Results[1].Reason)
	require.Equal(t, 0, f.scheduler.EntryCount())
	for _, id := range []string{"stale", "running"} {
		current, readErr := f.repo.FindByID(context.Background(), id)
		require.NoError(t, readErr)
		require.Equal(t, defaultGitHubBatchSchedule, current.SyncSchedule)
	}
}

func TestGitHubScheduleMigrationRepositoryCASProtectsRunningSyncAndSourceScope(t *testing.T) {
	f := newMigrationFixture(t)
	ds := f.source(t, "guarded", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	cas, ok := f.repo.(githubScheduleMigrationCAS)
	require.True(t, ok)
	proposed := githubStaggeredSixHourSchedule("Mininglamp-OSS/guarded", "documents")
	for _, testCase := range []struct {
		tenant  uint64
		kb      string
		version time.Time
	}{
		{tenant: 2, kb: "kb-one", version: ds.UpdatedAt},
		{tenant: 1, kb: "kb-two", version: ds.UpdatedAt},
		{tenant: 1, kb: "kb-one", version: ds.UpdatedAt.Add(time.Second)},
	} {
		applied, err := cas.UpdateGitHubScheduleIfUnchanged(context.Background(), testCase.tenant, testCase.kb, ds.ID, testCase.version, proposed)
		require.NoError(t, err)
		require.False(t, applied)
	}
	require.NoError(t, f.logs.Create(context.Background(), &types.SyncLog{
		ID: "guarded-running", DataSourceID: ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning,
	}))
	applied, err := cas.UpdateGitHubScheduleIfUnchanged(context.Background(), 1, "kb-one", ds.ID, ds.UpdatedAt, proposed)
	require.NoError(t, err)
	require.False(t, applied)
	current, err := f.repo.FindByID(context.Background(), ds.ID)
	require.NoError(t, err)
	require.Equal(t, defaultGitHubBatchSchedule, current.SyncSchedule)
}

func TestGitHubScheduleMigrationRefreshFailureWarnsAndRestartRecovers(t *testing.T) {
	f := newMigrationFixture(t)
	f.source(t, "recovery", "kb-one", defaultGitHubBatchSchedule, types.DataSourceStatusActive, 1)
	preview, err := f.service.PreviewGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationPreviewRequest{TenantID: 1, KnowledgeBaseID: "kb-one"})
	require.NoError(t, err)
	f.service.scheduler = failingMigrationScheduler{}
	response, err := f.service.ApplyGitHubScheduleMigration(context.Background(), &types.GitHubScheduleMigrationApplyRequest{
		TenantID: 1, KnowledgeBaseID: "kb-one", Selections: []types.GitHubScheduleMigrationSelection{migrationSelection(preview.Items[0])},
	})
	require.NoError(t, err)
	require.Equal(t, "applied_with_warning", response.Results[0].Status)
	require.Equal(t, "scheduler_refresh_failed", response.Results[0].Reason)
	stored, err := f.repo.FindByID(context.Background(), "recovery")
	require.NoError(t, err)
	require.Equal(t, preview.Items[0].Proposed, stored.SyncSchedule)
	// A process restart rebuilds the cron entry from the durable source row.
	restarted := datasource.NewScheduler(f.repo, f.logs, kbDeleteTaskEnqueuer{})
	require.NoError(t, restarted.Start(context.Background()))
	defer restarted.Stop()
	require.Equal(t, 1, restarted.EntryCount())
}

func TestSuccessfulErrorSourceManualSyncRestoresSchedulerEntry(t *testing.T) {
	for _, testCase := range []struct {
		id, initialStatus string
		wasPaused         bool
		wantEntries       int
	}{
		{"restored", types.DataSourceStatusError, false, 1},
		{"paused", types.DataSourceStatusPaused, true, 0},
	} {
		t.Run(testCase.id, func(t *testing.T) {
			f := newMigrationFixture(t)
			ds := f.source(t, testCase.id, "kb-one", defaultGitHubBatchSchedule, testCase.initialStatus, 1)
			require.NoError(t, f.scheduler.Start(context.Background()))
			defer f.scheduler.Stop()
			require.Zero(t, f.scheduler.EntryCount(), "startup must omit error/paused sources")
			log := &types.SyncLog{ID: testCase.id + "-log", DataSourceID: ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning}
			require.NoError(t, f.logs.Create(context.Background(), log))
			require.NoError(t, f.service.updateSyncRunResult(context.Background(), ds, log, &types.SyncResult{}, nil,
				types.SyncLogStatusSuccess, "", testCase.wasPaused, false))
			current, err := f.repo.FindByID(context.Background(), ds.ID)
			require.NoError(t, err)
			if testCase.wasPaused {
				require.Equal(t, types.DataSourceStatusPaused, current.Status)
			} else {
				require.Equal(t, types.DataSourceStatusActive, current.Status)
			}
			require.Equal(t, testCase.wantEntries, f.scheduler.EntryCount())
		})
	}
}
