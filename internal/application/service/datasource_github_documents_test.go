package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	githubConnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGitHubDocumentCredentialScopeNeverStoresToken(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "")
	public := &types.DataSource{Config: types.JSON(`{"type":"github","settings":{"repository":"test/docs"}}`)}
	publicConfig := &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "test/docs"}}
	scope, err := githubDocumentCredentialScope(public, publicConfig)
	require.NoError(t, err)
	require.Equal(t, "public", scope)

	private := &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "secret-keep-private"}}
	_, err = githubDocumentCredentialScope(public, private)
	require.ErrorContains(t, err, "SYSTEM_AES_KEY")
	require.NotContains(t, err.Error(), "secret-keep-private")
	t.Setenv("SYSTEM_AES_KEY", "0123456789abcdef0123456789abcdef")
	scope, err = githubDocumentCredentialScope(public, private)
	require.NoError(t, err)
	require.Len(t, scope, 64)
	require.NotContains(t, scope, "secret-keep-private")
	other, err := githubDocumentCredentialScope(public, &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "other-token"}})
	require.NoError(t, err)
	require.NotEqual(t, scope, other)

	// A rotated encryption key makes ParseConfig blank an unreadable encrypted
	// token. Never silently reinterpret such a source as public.
	privateSource := &types.DataSource{Config: types.JSON(`{"credentials":{"access_token":"enc:v1:opaque"}}`)}
	_, err = githubDocumentCredentialScope(privateSource, publicConfig)
	require.ErrorContains(t, err, "cannot be decrypted")
}

func TestGitHubDocumentProgressDistinguishesReadyAndFailedDeletion(t *testing.T) {
	items := []types.GitHubDocumentSyncItem{
		{Path: "guide.md", Operation: types.GitHubDocumentItemUpsert, Status: types.GitHubDocumentItemReady, Outcome: types.GitHubDocumentOutcomeCreated},
		{Path: "removed.md", Operation: types.GitHubDocumentItemDelete, Status: types.GitHubDocumentItemFailed, ErrorCode: "deletion_failed"},
		{Path: "pending.md", Operation: types.GitHubDocumentItemUpsert, Status: types.GitHubDocumentItemPending},
	}
	result := githubDocumentProgressResult(items)
	require.Equal(t, 3, result.Total)
	require.Equal(t, 1, result.Created)
	require.Equal(t, 1, result.Failed)
	require.Equal(t, 1, result.DeletionFailed)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "deletion_failed", result.Errors[0].Code)
}

func TestGitHubSyncAccessGuardRejectsCredentialRevocation(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "0123456789abcdef0123456789abcdef")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "github-credential-guard.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}))
	repo := apprepo.NewDataSourceRepository(db)
	cfg := &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "token-one"},
		Settings: map[string]interface{}{"repository": "test/docs", "mode": "documents"}}
	encoded, err := cfg.ToJSON()
	require.NoError(t, err)
	ds := &types.DataSource{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", Type: types.ConnectorTypeGitHub,
		Status: types.DataSourceStatusActive, Config: encoded}
	require.NoError(t, repo.Create(context.Background(), ds))
	guard := &syncAccessGuard{svc: &DataSourceService{dsRepo: repo}, ds: ds, config: cfg}
	require.NoError(t, guard.check(context.Background()))
	rotated := &types.DataSourceConfig{Credentials: map[string]interface{}{"access_token": "token-two"}, Settings: cfg.Settings}
	newConfig, err := rotated.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Model(&types.DataSource{}).Where("id = ?", ds.ID).Update("config", newConfig).Error)
	require.ErrorIs(t, guard.check(context.Background()), errSyncAccessChanged)
}

type githubDocumentTestKnowledgeService struct {
	interfaces.KnowledgeService
	repo      *preparedRepo
	failPath  string
	createdBy map[string]int
}

func (s *githubDocumentTestKnowledgeService) GetRepository() interfaces.KnowledgeRepository {
	return s.repo
}
func (s *githubDocumentTestKnowledgeService) CreateKnowledgeFromFile(
	_ context.Context, kb string, _ *multipart.FileHeader, metadata map[string]string, _ *bool,
	_ string, _ []string, _ string, _ *types.KnowledgeProcessOverrides,
) (*types.Knowledge, error) {
	path := metadata["github_path"]
	s.createdBy[path]++
	if path == s.failPath {
		return nil, errors.New("test parser unavailable")
	}
	encoded, _ := json.Marshal(metadata)
	now := time.Now().UTC()
	knowledge := &types.Knowledge{
		ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: kb, Metadata: encoded,
		ParseStatus: types.ParseStatusCompleted, EnableStatus: "enabled", ProcessedAt: &now,
	}
	s.repo.rows[knowledge.ID] = knowledge
	return knowledge, nil
}
func (s *githubDocumentTestKnowledgeService) DeleteKnowledge(_ context.Context, id string) error {
	delete(s.repo.rows, id)
	return nil
}

type githubDocumentRoundTrip func(*http.Request) (*http.Response, error)

func (fn githubDocumentRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func serviceGitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

func TestGitHubDocumentRunRetriesOnlyFailedFileAfterRestart(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "github-doc-service.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}, &types.SyncLog{}, &types.GitHubDocumentRun{}, &types.GitHubDocumentSyncItem{}))
	ctx := context.Background()
	gitRepo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(gitRepo, "docs"), 0o700))
	serviceGitRun(t, gitRepo, "init", "-q")
	serviceGitRun(t, gitRepo, "config", "user.email", "test@example.invalid")
	serviceGitRun(t, gitRepo, "config", "user.name", "Test")
	for _, name := range []string{"a.md", "b.md"} {
		require.NoError(t, os.WriteFile(filepath.Join(gitRepo, "docs", name), []byte("old "+name+"\n"), 0o600))
	}
	serviceGitRun(t, gitRepo, "add", ".")
	serviceGitRun(t, gitRepo, "commit", "-qm", "old")
	oldCommit := serviceGitRun(t, gitRepo, "rev-parse", "HEAD")
	sourceRepo := apprepo.NewDataSourceRepository(db)
	ds := &types.DataSource{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", Name: "GitHub docs",
		Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusActive, SyncDeletions: true}
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"repository": "test/docs", "ref": "main", "mode": "documents", "paths": []string{"docs"}},
		SyncSource: &types.DataSourceSyncSource{TenantID: 7, KnowledgeBaseID: "kb", DataSourceID: ds.ID}}
	ds.Config, err = cfg.ToJSON()
	require.NoError(t, err)
	require.NoError(t, sourceRepo.Create(ctx, ds))
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", t.TempDir())
	store, err := snapshot.FromEnvironment()
	require.NoError(t, err)
	privateDir, err := store.PrivateDirectory(ds, "git")
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("test/docs"))
	bare := filepath.Join(privateDir, hex.EncodeToString(hash[:]))
	serviceGitRun(t, "", "clone", "--bare", "--", gitRepo, bare)
	serviceGitRun(t, "", "--git-dir="+bare, "remote", "set-url", "origin", "https://github.com/test/docs.git")
	head := oldCommit
	client := &http.Client{Transport: githubDocumentRoundTrip(func(r *http.Request) (*http.Response, error) {
		var data any
		switch r.URL.Path {
		case "/repos/test/docs":
			data = map[string]string{"full_name": "test/docs", "default_branch": "main"}
		case "/repos/test/docs/commits/main":
			data = map[string]any{"sha": head, "commit": map[string]any{"tree": map[string]string{"sha": strings.Repeat("a", 40)}}}
		default:
			t.Fatalf("document sync made unexpected REST tree/blob request: %s", r.URL.Path)
		}
		encoded, _ := json.Marshal(data)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
	})}
	connector := githubConnector.NewConnectorWithHTTPClient(client, "https://api.github.com")
	baseline, err := connector.PlanDocuments(ctx, cfg, nil, false, "")
	require.NoError(t, err)
	ds.LastSyncCursor, err = baseline.Cursor().ToJSON()
	require.NoError(t, err)
	require.NoError(t, sourceRepo.UpdateSyncState(ctx, ds))
	knowledgeRepo := &preparedRepo{rows: map[string]*types.Knowledge{}}
	now := time.Now().UTC()
	for _, document := range baseline.Upserts {
		meta, _ := json.Marshal(map[string]string{
			"datasource_id": ds.ID, "external_id": "github:test/docs:main:" + document.Path, "github_blob_sha": document.BlobSHA,
		})
		oldID := uuid.NewString()
		knowledgeRepo.rows[oldID] = &types.Knowledge{
			ID: oldID, TenantID: 7, KnowledgeBaseID: "kb", Metadata: meta,
			ParseStatus: types.ParseStatusCompleted, EnableStatus: "enabled", ProcessedAt: &now,
		}
	}
	for _, name := range []string{"a.md", "b.md"} {
		require.NoError(t, os.WriteFile(filepath.Join(gitRepo, "docs", name), []byte("new "+name+"\n"), 0o600))
	}
	serviceGitRun(t, gitRepo, "add", ".")
	serviceGitRun(t, gitRepo, "commit", "-qm", "new")
	head = serviceGitRun(t, gitRepo, "rev-parse", "HEAD")
	serviceGitRun(t, "", "--git-dir="+bare, "fetch", "--no-tags", "--", gitRepo, "+"+head+":refs/weknora/test")
	knowledge := &githubDocumentTestKnowledgeService{repo: knowledgeRepo, failPath: "docs/a.md", createdBy: map[string]int{}}
	syncLogs := apprepo.NewSyncLogRepository(db)
	svc := &DataSourceService{dsRepo: sourceRepo, syncLogRepo: syncLogs, knowledgeService: knowledge,
		tenantRepo: &processSyncTenantRepo{tenant: &types.Tenant{ID: 7}}, tagService: &processSyncTagService{}}
	process := func(logID string) error {
		log := &types.SyncLog{ID: logID, DataSourceID: ds.ID, TenantID: ds.TenantID, Status: types.SyncLogStatusRunning}
		require.NoError(t, syncLogs.Create(ctx, log))
		current, findErr := sourceRepo.FindByID(ctx, ds.ID)
		require.NoError(t, findErr)
		guard := &syncAccessGuard{svc: svc, ds: current, config: cfg}
		return svc.processGitHubDocumentRun(ctx, connector, current, cfg, log,
			types.DataSourceSyncPayload{DataSourceID: ds.ID, TenantID: ds.TenantID, SyncLogID: logID},
			false, &syncRunGuard{svc: svc, syncLogID: logID}, guard)
	}
	require.NoError(t, process(uuid.NewString()))
	firstCursor, err := sourceRepo.FindByID(ctx, ds.ID)
	require.NoError(t, err)
	require.Contains(t, string(firstCursor.LastSyncCursor), oldCommit)
	require.Equal(t, 1, knowledge.createdBy["docs/a.md"])
	require.Equal(t, 1, knowledge.createdBy["docs/b.md"])
	stillOld, err := knowledgeRepo.FindByDataSourceExternalID(ctx, 7, "kb", ds.ID, "github:test/docs:main:docs/a.md")
	require.NoError(t, err)
	require.NotNil(t, stillOld, "failed replacement must retain its old indexed document")
	require.Equal(t, baseline.Upserts[0].BlobSHA, stillOld.GetMetadata()["github_blob_sha"])
	knowledge.failPath = ""
	require.NoError(t, process(uuid.NewString()))
	finalCursor, err := sourceRepo.FindByID(ctx, ds.ID)
	require.NoError(t, err)
	require.Contains(t, string(finalCursor.LastSyncCursor), head)
	require.Equal(t, 2, knowledge.createdBy["docs/a.md"])
	require.Equal(t, 1, knowledge.createdBy["docs/b.md"], "already ACKed file must not be re-ingested")
}

type githubDocumentDeleteRepo struct {
	*preparedRepo
	tombstone map[string]bool
	failHard  bool
}

func (r *githubDocumentDeleteRepo) FindByDataSourceExternalID(ctx context.Context, tenant uint64, kb, ds, external string) (*types.Knowledge, error) {
	row, err := r.preparedRepo.FindByDataSourceExternalID(ctx, tenant, kb, ds, external)
	if row != nil && r.tombstone[row.ID] {
		return nil, err
	}
	return row, err
}

func (r *githubDocumentDeleteRepo) ListByDataSourceIDIncludingDeleted(
	_ context.Context, tenant uint64, kb, ds string,
) ([]*types.Knowledge, error) {
	var out []*types.Knowledge
	for _, row := range r.rows {
		if row.TenantID == tenant && row.KnowledgeBaseID == kb && row.GetMetadata()["datasource_id"] == ds {
			out = append(out, row)
		}
	}
	return out, nil
}

func (r *githubDocumentDeleteRepo) HardDeleteKnowledge(ctx context.Context, tenant uint64, id string) error {
	if r.failHard {
		r.failHard = false
		return errors.New("test hard-delete outage")
	}
	return r.preparedRepo.HardDeleteKnowledge(ctx, tenant, id)
}

type githubDocumentDeleteService struct {
	interfaces.KnowledgeService
	repo *githubDocumentDeleteRepo
}

func (s *githubDocumentDeleteService) GetRepository() interfaces.KnowledgeRepository { return s.repo }
func (s *githubDocumentDeleteService) DeleteKnowledge(_ context.Context, id string) error {
	s.repo.tombstone[id] = true
	return nil
}

func TestGitHubDocumentDeleteRetriesSoftTombstoneBeforeAck(t *testing.T) {
	id := uuid.NewString()
	metadata, _ := json.Marshal(map[string]string{
		"external_id": "github:test/docs:main:removed.md", "datasource_id": "ds", "github_blob_sha": "old",
	})
	repo := &githubDocumentDeleteRepo{
		preparedRepo: &preparedRepo{rows: map[string]*types.Knowledge{
			id: {ID: id, TenantID: 7, KnowledgeBaseID: "kb", Metadata: metadata},
		}},
		tombstone: map[string]bool{}, failHard: true,
	}
	svc := &DataSourceService{knowledgeService: &githubDocumentDeleteService{repo: repo}}
	ds := &types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb"}
	item := types.FetchedItem{ExternalID: "github:test/docs:main:removed.md", IsDeleted: true}
	_, err := svc.deleteGitHubDocumentItem(context.Background(), ds, item)
	require.Error(t, err)
	require.True(t, repo.tombstone[id])
	require.Contains(t, repo.rows, id)
	outcome, err := svc.deleteGitHubDocumentItem(context.Background(), ds, item)
	require.NoError(t, err)
	require.Equal(t, types.GitHubDocumentOutcomeDeleted, outcome)
	require.NotContains(t, repo.rows, id)
}
