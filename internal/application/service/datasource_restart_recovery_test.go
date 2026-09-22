package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type restartRecoveryDataSourceRepo struct {
	interfaces.DataSourceRepository
	ds          *types.DataSource
	leaseWrites int
	runs        map[string]*types.DataSourceRestartRecoveryRun
}

func (r *restartRecoveryDataSourceRepo) FindByID(_ context.Context, id string) (*types.DataSource, error) {
	if r.ds == nil || r.ds.ID != id {
		return nil, datasource.ErrDataSourceNotFound
	}
	return r.ds, nil
}

func (r *restartRecoveryDataSourceRepo) TryAcquireRestartRecoveryLease(_ context.Context, _, _ string, _ time.Time) (bool, error) {
	r.leaseWrites++
	return true, nil
}
func (r *restartRecoveryDataSourceRepo) RenewRestartRecoveryLease(_ context.Context, _, _ string, _ time.Time) (bool, error) {
	return true, nil
}
func (r *restartRecoveryDataSourceRepo) ReleaseRestartRecoveryLease(_ context.Context, _, _ string) error {
	return nil
}
func (r *restartRecoveryDataSourceRepo) CreateRestartRecoveryRun(_ context.Context, run *types.DataSourceRestartRecoveryRun) error {
	if r.runs == nil {
		r.runs = map[string]*types.DataSourceRestartRecoveryRun{}
	}
	r.runs[run.ID] = run
	return nil
}
func (r *restartRecoveryDataSourceRepo) FindRestartRecoveryRun(_ context.Context, id string) (*types.DataSourceRestartRecoveryRun, error) {
	return r.runs[id], nil
}
func (r *restartRecoveryDataSourceRepo) ClaimRestartRecoveryRun(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (r *restartRecoveryDataSourceRepo) BlockRestartRecoveryRunBeforeLease(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (r *restartRecoveryDataSourceRepo) UpdateRestartRecoveryRunIfLease(_ context.Context, _ *types.DataSourceRestartRecoveryRun, _ string) (bool, error) {
	return true, nil
}

type restartRecoveryKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	items     []*types.Knowledge
	canonical *types.Knowledge
}

func (r *restartRecoveryKnowledgeRepo) ListByDataSourceIDIncludingDeleted(
	_ context.Context, _ uint64, _ string, _ string,
) ([]*types.Knowledge, error) {
	return append([]*types.Knowledge(nil), r.items...), nil
}
func (r *restartRecoveryKnowledgeRepo) FindByDataSourceExternalID(
	_ context.Context, _ uint64, _, _, _ string,
) (*types.Knowledge, error) {
	return r.canonical, nil
}

type restartRecoveryKnowledgeService struct {
	interfaces.KnowledgeService
	repo     interfaces.KnowledgeRepository
	openErr  map[string]error
	readErr  map[string]error
	closeErr map[string]error
	empty    map[string]bool
	readCall []string
	reads    map[string]int
	closed   map[string]bool
}

func (s *restartRecoveryKnowledgeService) GetRepository() interfaces.KnowledgeRepository {
	return s.repo
}

func (s *restartRecoveryKnowledgeService) GetKnowledgeFile(_ context.Context, id string) (io.ReadCloser, string, error) {
	s.readCall = append(s.readCall, id)
	if err := s.openErr[id]; err != nil {
		return nil, "", err
	}
	content := "x"
	if s.empty[id] {
		content = ""
	}
	return &restartRecoveryReadCloser{Reader: strings.NewReader(content), readErr: s.readErr[id], onRead: func() {
		s.reads[id]++
	}, close: func() error {
		s.closed[id] = true
		return s.closeErr[id]
	}}, id + ".md", nil
}

type restartRecoveryReadCloser struct {
	io.Reader
	readErr error
	onRead  func()
	close   func() error
}

func (r *restartRecoveryReadCloser) Read(p []byte) (int, error) {
	if r.onRead != nil {
		r.onRead()
	}
	if r.readErr != nil {
		return 0, r.readErr
	}
	return r.Reader.Read(p)
}

func (r *restartRecoveryReadCloser) Close() error { return r.close() }

type restartRecoveryKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *restartRecoveryKBService) GetKnowledgeBaseByID(_ context.Context, _ string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type restartRecoveryTenantRepo struct{ interfaces.TenantRepository }

func (r *restartRecoveryTenantRepo) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	return &types.Tenant{ID: id}, nil
}

type restartRecoverySyncLogs struct {
	interfaces.SyncLogRepository
	running bool
}

func (s *restartRecoverySyncLogs) HasRunningSync(_ context.Context, _ string) (bool, error) {
	return s.running, nil
}

type restartRecoveryTaskQueue struct {
	interfaces.TaskEnqueuer
	tasks []*asynq.Task
}

func (q *restartRecoveryTaskQueue) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.tasks = append(q.tasks, task)
	return &asynq.TaskInfo{ID: "queued", Type: task.Type()}, nil
}

func newRestartRecoveryPreviewFixture(t *testing.T) (*DataSourceService, *restartRecoveryDataSourceRepo, *restartRecoveryKnowledgeRepo, context.Context) {
	t.Helper()
	t.Setenv("SYSTEM_AES_KEY", "test-restart-recovery-preview-signing-key")
	now := time.Now().UTC()
	ds := &types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb", Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusActive}
	eligibleMeta, err := json.Marshal(map[string]string{
		"datasource_id": "ds", "external_id": "doc:pending:abc", "sync_target_external_id": "doc",
		"source_version": "abc", "github_url": "https://github.com/example/repo/blob/abc/doc.md",
	})
	require.NoError(t, err)
	urlMeta, err := json.Marshal(map[string]string{
		"datasource_id": "ds", "external_id": "url:pending:def", "sync_target_external_id": "url",
		"source_version": "def", "github_url": "https://github.com/example/repo/blob/def/url.md",
	})
	require.NoError(t, err)
	repo := &restartRecoveryKnowledgeRepo{items: []*types.Knowledge{
		{ID: "file", TenantID: 7, KnowledgeBaseID: "kb", Title: "File", FileName: "doc.md", FileType: "md", Type: "file", Channel: types.ConnectorTypeGitHub, FilePath: "resource://candidate-file", Metadata: eligibleMeta, ParseStatus: types.ParseStatusFailed, EnableStatus: "disabled", ErrorMessage: types.RestartInterruptedKnowledgeError, UpdatedAt: now},
		{ID: "url", TenantID: 7, KnowledgeBaseID: "kb", Title: "URL", FileName: "url.md", FileType: "md", Type: "url", Channel: types.ConnectorTypeGitHub, Metadata: urlMeta, ParseStatus: types.ParseStatusFailed, EnableStatus: "disabled", ErrorMessage: types.RestartInterruptedKnowledgeError, UpdatedAt: now},
	}}
	dataSources := &restartRecoveryDataSourceRepo{ds: ds, runs: map[string]*types.DataSourceRestartRecoveryRun{}}
	queue := &restartRecoveryTaskQueue{}
	knowledgeService := &restartRecoveryKnowledgeService{
		repo: repo, openErr: map[string]error{}, readErr: map[string]error{}, closeErr: map[string]error{},
		empty: map[string]bool{}, reads: map[string]int{}, closed: map[string]bool{},
	}
	svc := &DataSourceService{
		dsRepo:           dataSources,
		syncLogRepo:      &restartRecoverySyncLogs{},
		knowledgeService: knowledgeService,
		kbService:        &restartRecoveryKBService{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7, Type: types.KnowledgeBaseTypeDocument, IndexingStrategy: types.DefaultIndexingStrategy()}},
		taskEnqueuer:     queue,
		tenantRepo:       &restartRecoveryTenantRepo{},
	}
	ctx := types.WithCaller(types.WithExecutionTenant(context.Background(), 7), types.Caller{TenantID: 7, UserID: "admin", Role: types.TenantRoleAdmin})
	return svc, dataSources, repo, ctx
}

func TestRestartRecoveryPreviewIsReadOnlyAndExcludesURL(t *testing.T) {
	svc, sources, _, ctx := newRestartRecoveryPreviewFixture(t)
	preview, err := svc.PreviewRestartInterruptedRecovery(ctx, "ds")
	require.NoError(t, err)
	require.Equal(t, 1, preview.EligibleCount)
	require.Equal(t, 1, preview.ExcludedCount)
	require.Len(t, preview.Candidates, 2)
	require.NotEmpty(t, preview.PreviewToken)
	require.Empty(t, sources.runs, "preview must not persist a run")
	require.Zero(t, sources.leaseWrites, "preview must not acquire a source lease")
	knowledgeService := svc.knowledgeService.(*restartRecoveryKnowledgeService)
	require.Contains(t, knowledgeService.readCall, "file")
	require.Positive(t, knowledgeService.reads["file"], "preview must read a storage byte, not only open the handle")
	require.True(t, knowledgeService.closed["file"], "preview must close its one-byte storage readability probe")
	for _, candidate := range preview.Candidates {
		require.NotContains(t, candidate.Reason, "resource://", "preview must not disclose FilePath")
		if candidate.KnowledgeID == "url" {
			require.Equal(t, types.DataSourceRestartRecoveryCandidateExcluded, candidate.State)
			require.Contains(t, candidate.Reason, "URL")
		}
	}
}

func TestRestartRecoveryExecuteRejectsChangedPreviewAndQueuesExactPlan(t *testing.T) {
	svc, sources, repo, ctx := newRestartRecoveryPreviewFixture(t)
	preview, err := svc.PreviewRestartInterruptedRecovery(ctx, "ds")
	require.NoError(t, err)

	// Change the exact restart-marked candidate after preview. Execute must
	// fail before a run or lease is created, rather than trying a stale file.
	repo.items[0].ErrorMessage = "different failure"
	_, err = svc.StartRestartInterruptedRecovery(ctx, "ds", &types.DataSourceRestartRecoveryRequest{PreviewToken: preview.PreviewToken})
	require.Error(t, err)
	require.Empty(t, sources.runs)
	require.Zero(t, sources.leaseWrites)

	// A fresh preview of the original exact state creates one durable pending
	// run and queues only the server-owned payload.
	repo.items[0].ErrorMessage = types.RestartInterruptedKnowledgeError
	preview, err = svc.PreviewRestartInterruptedRecovery(ctx, "ds")
	require.NoError(t, err)
	run, err := svc.StartRestartInterruptedRecovery(ctx, "ds", &types.DataSourceRestartRecoveryRequest{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)
	require.Equal(t, types.DataSourceRestartRecoveryStatusPending, run.Status)
	require.Contains(t, sources.runs, run.ID)
	queue := svc.taskEnqueuer.(*restartRecoveryTaskQueue)
	require.Len(t, queue.tasks, 1)
	require.Equal(t, types.TypeDataSourceRestartRecovery, queue.tasks[0].Type())
}

func TestManualSyncRejectsActiveRestartRecoveryLease(t *testing.T) {
	svc, sources, _, ctx := newRestartRecoveryPreviewFixture(t)
	until := time.Now().UTC().Add(time.Minute)
	sources.ds.RestartRecoveryLeaseID = "recovery-run"
	sources.ds.RestartRecoveryLeaseUntil = &until
	_, err := svc.ManualSync(ctx, sources.ds.ID)
	require.ErrorIs(t, err, datasource.ErrRestartRecoveryInProgress)
}

func TestRestartRecoveryPreviewExcludesUnreadableStoredFileFromSignedPlan(t *testing.T) {
	svc, sources, repo, ctx := newRestartRecoveryPreviewFixture(t)
	metadata, err := json.Marshal(map[string]string{
		"datasource_id": "ds", "external_id": "missing:pending:def", "sync_target_external_id": "missing",
		"source_version": "def", "github_url": "https://github.com/example/repo/blob/def/missing.md",
	})
	require.NoError(t, err)
	repo.items = append(repo.items, &types.Knowledge{
		ID: "missing", TenantID: 7, KnowledgeBaseID: "kb", Title: "Missing", FileName: "missing.md", FileType: "md",
		Type: "file", Channel: types.ConnectorTypeGitHub, FilePath: "resource://missing", Metadata: metadata,
		ParseStatus: types.ParseStatusFailed, EnableStatus: "disabled", ErrorMessage: types.RestartInterruptedKnowledgeError, UpdatedAt: time.Now().UTC(),
	})
	knowledgeService := svc.knowledgeService.(*restartRecoveryKnowledgeService)
	knowledgeService.openErr["missing"] = errors.New("failed to open file: /private/path/missing.md")

	preview, err := svc.PreviewRestartInterruptedRecovery(ctx, "ds")
	require.NoError(t, err)
	require.Equal(t, 1, preview.EligibleCount)
	require.Equal(t, 2, preview.ExcludedCount)
	require.Contains(t, knowledgeService.readCall, "missing")
	for _, candidate := range preview.Candidates {
		if candidate.KnowledgeID == "missing" {
			require.Equal(t, types.DataSourceRestartRecoveryCandidateExcluded, candidate.State)
			require.Equal(t, "stored file is not readable from current knowledge-base storage", candidate.Reason)
			require.NotContains(t, candidate.Reason, "/private/path")
		}
	}

	run, err := svc.StartRestartInterruptedRecovery(ctx, "ds", &types.DataSourceRestartRecoveryRequest{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)
	var plan types.DataSourceRestartRecoveryPlan
	require.NoError(t, json.Unmarshal(run.Plan, &plan))
	require.Len(t, plan.Candidates, 1, "unreadable rows must not enter the signed executable plan")
	require.Equal(t, "file", plan.Candidates[0].KnowledgeID)
	require.Contains(t, sources.runs, run.ID)
}

func TestRestartRecoveryStoredFileReadabilityAcceptsEOFAndRejectsReadOrCloseFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		empty     bool
		readErr   error
		closeErr  error
		wantReady bool
	}{
		{name: "empty file is readable", empty: true, wantReady: true},
		{name: "read failure", readErr: errors.New("read failed"), wantReady: false},
		{name: "close failure", closeErr: errors.New("close failed"), wantReady: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, ctx := newRestartRecoveryPreviewFixture(t)
			knowledgeService := svc.knowledgeService.(*restartRecoveryKnowledgeService)
			knowledgeService.empty["file"] = tc.empty
			knowledgeService.readErr["file"] = tc.readErr
			knowledgeService.closeErr["file"] = tc.closeErr
			ready, err := svc.restartRecoveryStoredFileReadable(ctx, &types.DataSource{ID: "ds", TenantID: 7}, "file")
			if tc.wantReady {
				require.NoError(t, err)
				require.True(t, ready)
			} else {
				require.Error(t, err)
				require.False(t, ready)
			}
			require.Positive(t, knowledgeService.reads["file"])
			require.True(t, knowledgeService.closed["file"])
		})
	}
}
