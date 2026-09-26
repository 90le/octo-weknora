package service

import (
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type preparedRepo struct {
	interfaces.KnowledgeRepository
	rows   map[string]*types.Knowledge
	events []string
}

func (r *preparedRepo) FindByDataSourceExternalID(_ context.Context, t uint64, kb, ds, id string) (*types.Knowledge, error) {
	for _, k := range r.rows {
		var m map[string]string
		_ = json.Unmarshal(k.Metadata, &m)
		if k.TenantID == t && k.KnowledgeBaseID == kb && m["datasource_id"] == ds && m["external_id"] == id {
			return k, nil
		}
	}
	return nil, nil
}
func (r *preparedRepo) GetKnowledgeByID(ctx context.Context, _ uint64, id string) (*types.Knowledge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.events = append(r.events, "read:"+id)
	return r.rows[id], nil
}
func (r *preparedRepo) UpdateKnowledgeColumn(_ context.Context, id, col string, value interface{}) error {
	if col == "source" {
		r.rows[id].Source = value.(string)
		return nil
	}
	r.events = append(r.events, "adopt:"+id)
	r.rows[id].Metadata = value.(types.JSON)
	return nil
}
func (r *preparedRepo) HardDeleteKnowledge(_ context.Context, _ uint64, id string) error {
	delete(r.rows, id)
	return nil
}

type preparedService struct {
	interfaces.KnowledgeService
	r         *preparedRepo
	createErr error
	failed    bool
	pending   bool
}

func (s *preparedService) GetRepository() interfaces.KnowledgeRepository { return s.r }
func (s *preparedService) CreateKnowledgeFromFile(_ context.Context, kb string, _ *multipart.FileHeader, m map[string]string, _ *bool, _ string, _ []string, _ string, _ *types.KnowledgeProcessOverrides) (*types.Knowledge, error) {
	s.r.events = append(s.r.events, "create:new")
	if s.createErr != nil {
		return nil, s.createErr
	}
	b, _ := json.Marshal(m)
	now := time.Now()
	k := &types.Knowledge{ID: "new", TenantID: 7, KnowledgeBaseID: kb, Metadata: b, ParseStatus: types.ParseStatusCompleted, EnableStatus: "enabled", ProcessedAt: &now}
	if s.failed {
		k.ParseStatus = types.ParseStatusFailed
		k.EnableStatus = "disabled"
	}
	if s.pending {
		k.ParseStatus = types.ParseStatusPending
		k.EnableStatus = "disabled"
	}
	s.r.rows[k.ID] = k
	return k, nil
}
func (s *preparedService) DeleteKnowledge(_ context.Context, id string) error {
	s.r.events = append(s.r.events, "delete:"+id)
	delete(s.r.rows, id)
	return nil
}
func preparedFixture() (*DataSourceService, *preparedService, *types.DataSource, *types.FetchedItem) {
	now := time.Now()
	m, _ := json.Marshal(map[string]string{"external_id": "doc", "datasource_id": "ds", "github_blob_sha": "old"})
	r := &preparedRepo{rows: map[string]*types.Knowledge{"old": {ID: "old", TenantID: 7, KnowledgeBaseID: "kb", Metadata: m, ParseStatus: types.ParseStatusCompleted, EnableStatus: "enabled", ProcessedAt: &now}}}
	ks := &preparedService{r: r}
	return &DataSourceService{knowledgeService: ks}, ks, &types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb", Type: types.ConnectorTypeGitHub}, &types.FetchedItem{ExternalID: "doc", FileName: "guide.md", Content: []byte("updated"), Metadata: map[string]string{"github_blob_sha": "new"}}
}
func TestPreparedSyncKeepsOldOnCreateFailure(t *testing.T) {
	s, ks, ds, item := preparedFixture()
	ks.createErr = errors.New("storage failed")
	_, err := s.ingestItem(context.Background(), ds, item, nil)
	require.Error(t, err)
	require.Contains(t, ks.r.rows, "old")
	require.Equal(t, []string{"create:new"}, ks.r.events)
}

func TestPreparedSyncNeverAdoptsCrossSourceDuplicate(t *testing.T) {
	s, ks, ds, item := preparedFixture()
	foreign := &types.Knowledge{ID: "foreign", TenantID: 7, KnowledgeBaseID: "kb"}
	ks.createErr = types.NewDuplicateFileError(foreign)
	_, err := s.ingestItem(context.Background(), ds, item, nil)
	require.ErrorIs(t, err, errPreparedFileOwnedByAnotherSource)
	require.Contains(t, ks.r.rows, "old", "the previous version must remain searchable")
	require.NotContains(t, ks.r.events, "delete:old")
	require.NotContains(t, ks.r.rows, "foreign", "foreign source must not be adopted")
}
func TestPreparedSyncKeepsOldOnParseFailure(t *testing.T) {
	s, ks, ds, item := preparedFixture()
	ks.failed = true
	_, err := s.ingestItem(context.Background(), ds, item, nil)
	require.ErrorContains(t, err, "previous version preserved")
	require.Contains(t, ks.r.rows, "old")
	require.NotContains(t, ks.r.rows, "new")
}
func TestPreparedSyncRetiresOnlyAfterReady(t *testing.T) {
	s, ks, ds, item := preparedFixture()
	item.Metadata["github_url"] = "https://github.com/test/docs/blob/commit/guide.md"
	updated, err := s.ingestItem(context.Background(), ds, item, nil)
	require.NoError(t, err)
	require.True(t, updated)
	require.Equal(t, []string{"create:new", "read:new", "delete:old", "adopt:new"}, ks.r.events)
	require.NotContains(t, ks.r.rows, "old")
	k, err := ks.r.FindByDataSourceExternalID(context.Background(), 7, "kb", "ds", "doc")
	require.NoError(t, err)
	require.Equal(t, "new", k.ID)
	require.Equal(t, item.Metadata["github_url"], k.Source)
	metadata := k.GetMetadata()
	require.Equal(t, "doc", metadata["external_id"])
	require.Empty(t, metadata["sync_target_external_id"], "published candidates must no longer be hidden from answers")
}
func TestPreparedSyncCancellationKeepsOld(t *testing.T) {
	s, ks, ds, item := preparedFixture()
	ks.pending = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.ingestItem(ctx, ds, item, nil)
	require.Error(t, err)
	require.Contains(t, ks.r.rows, "old")
	require.NotContains(t, ks.r.events, "delete:old")
}
func TestPreparedSyncSkipsUnchangedContent(t *testing.T) {
	s, ks, ds, item := preparedFixture()
	item.Metadata["github_blob_sha"] = "old"
	_, err := s.ingestItem(context.Background(), ds, item, nil)
	var duplicate *types.DuplicateKnowledgeError
	require.ErrorAs(t, err, &duplicate)
	require.Empty(t, ks.r.events)
}

func TestPreparedSyncDoesNotAdoptWhileParserCanStillWrite(t *testing.T) {
	now := time.Now()
	for _, status := range []string{types.ParseStatusPending, types.ParseStatusProcessing, types.ParseStatusFinalizing} {
		require.False(t, indexedForSync(&types.Knowledge{EnableStatus: "enabled", ProcessedAt: &now, ParseStatus: status}))
	}
}

func TestPreparedSyncResumesReadyCandidateWithoutRecreating(t *testing.T) {
	s, ks, ds, item := preparedFixture()
	ks.pending = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.ingestItem(ctx, ds, item, nil)
	require.Error(t, err)
	ks.r.rows["new"].ParseStatus = types.ParseStatusCompleted
	ks.r.rows["new"].EnableStatus = "enabled"
	_, err = s.ingestItem(context.Background(), ds, item, nil)
	require.NoError(t, err)
	created := 0
	for _, event := range ks.r.events {
		if event == "create:new" {
			created++
		}
	}
	require.Equal(t, 1, created)
	require.NotContains(t, ks.r.rows, "old")
}

func TestFinalizePreparedCandidateWithoutPreviousBlocksCanonicalTarget(t *testing.T) {
	s, ks, ds, _ := preparedFixture()
	now := time.Now()
	metadata, err := json.Marshal(map[string]string{
		"datasource_id": "ds", "external_id": "doc:pending:new",
		"sync_target_external_id": "doc", "github_url": "https://github.com/test/repo/blob/new/guide.md",
	})
	require.NoError(t, err)
	candidate := &types.Knowledge{
		ID: "candidate", TenantID: ds.TenantID, KnowledgeBaseID: ds.KnowledgeBaseID,
		Metadata: metadata, ParseStatus: types.ParseStatusCompleted, EnableStatus: "enabled", ProcessedAt: &now,
	}
	ks.r.rows[candidate.ID] = candidate

	outcome, err := s.finalizePreparedCandidate(context.Background(), ds, candidate, nil, nil)
	require.NoError(t, err)
	require.Equal(t, preparedCandidateFinalizeBlocked, outcome)
	require.Contains(t, ks.r.rows, "old", "recovery must not delete an existing canonical target")
	var after map[string]string
	require.NoError(t, json.Unmarshal(candidate.Metadata, &after))
	require.Equal(t, "doc", after["sync_target_external_id"], "blocked candidates remain unpublished")
}
