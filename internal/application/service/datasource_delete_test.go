package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dataSourcePurgeKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	items          []*types.Knowledge
	byID           map[string]*types.Knowledge
	listTenant     uint64
	listKB         string
	listSource     string
	hardDeletedIDs []string
	hardDeleteErr  error
}

func (r *dataSourcePurgeKnowledgeRepo) HardDeleteKnowledgeList(_ context.Context, _ uint64, ids []string) error {
	if r.hardDeleteErr != nil {
		return r.hardDeleteErr
	}
	r.hardDeletedIDs = append(r.hardDeletedIDs, ids...)
	return nil
}

func (r *dataSourcePurgeKnowledgeRepo) ListByDataSourceID(
	_ context.Context, tenantID uint64, kbID, dataSourceID string,
) ([]*types.Knowledge, error) {
	r.listTenant, r.listKB, r.listSource = tenantID, kbID, dataSourceID
	return append([]*types.Knowledge(nil), r.items...), nil
}

func (r *dataSourcePurgeKnowledgeRepo) GetKnowledgeBatch(
	_ context.Context, tenantID uint64, ids []string,
) ([]*types.Knowledge, error) {
	items := make([]*types.Knowledge, 0, len(ids))
	for _, id := range ids {
		item := r.byID[id]
		if item == nil || item.TenantID != tenantID {
			continue
		}
		copyOfItem := *item
		items = append(items, &copyOfItem)
	}
	return items, nil
}

type dataSourcePurgeKnowledgeService struct {
	interfaces.KnowledgeService
	repo       interfaces.KnowledgeRepository
	deletedIDs []string
}

func (s *dataSourcePurgeKnowledgeService) GetRepository() interfaces.KnowledgeRepository {
	return s.repo
}

func (s *dataSourcePurgeKnowledgeService) DeleteKnowledgeList(_ context.Context, ids []string) error {
	s.deletedIDs = append(s.deletedIDs, ids...)
	return nil
}

func newDataSourcePurgeService(t *testing.T) (*DataSourceService, *dataSourcePurgeKnowledgeRepo, *dataSourcePurgeKnowledgeService, *kbDeleteDSRepo) {
	t.Helper()
	t.Setenv("SYSTEM_AES_KEY", "test-data-source-delete-preview-signing-key")

	ds := &types.DataSource{
		ID: "source-a", TenantID: 7, KnowledgeBaseID: "kb-a", Name: "Source A",
		Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusActive,
	}
	matchedA := &types.Knowledge{
		ID: "matched-a", TenantID: 7, KnowledgeBaseID: "kb-a", Title: "Owned A", Type: "document",
		FilePath: types.BuildResourcePath("AAAAAAAAAAAAAAAAAAAAAA"), StorageSize: 11,
		Metadata: types.JSON(`{"datasource_id":"source-a"}`),
	}
	matchedB := &types.Knowledge{
		ID: "matched-b", TenantID: 7, KnowledgeBaseID: "kb-a", Title: "Owned B", Type: "document",
		FilePath: types.BuildResourcePath("BBBBBBBBBBBBBBBBBBBBBB"), StorageSize: 13,
		Metadata: types.JSON(`{"datasource_id":"source-a"}`),
	}
	repo := &dataSourcePurgeKnowledgeRepo{
		items: []*types.Knowledge{matchedA, matchedB},
		byID:  map[string]*types.Knowledge{matchedA.ID: matchedA, matchedB.ID: matchedB},
	}
	knowledgeSvc := &dataSourcePurgeKnowledgeService{repo: repo}
	dsRepo := newKBDeleteDSRepo(ds.KnowledgeBaseID, ds)
	syncLogs := &kbDeleteSyncLogRepo{}
	scheduler := datasource.NewScheduler(dsRepo, syncLogs, kbDeleteTaskEnqueuer{})
	return &DataSourceService{
		dsRepo: dsRepo, syncLogRepo: syncLogs, scheduler: scheduler, knowledgeService: knowledgeSvc,
	}, repo, knowledgeSvc, dsRepo
}

func TestDataSourceDeletePreviewAndPurgeOnlyExactGeneratedKnowledge(t *testing.T) {
	svc, repo, knowledgeSvc, dsRepo := newDataSourcePurgeService(t)
	ctx := ctxWithTenant(7)

	preview, err := svc.PreviewDataSourceDelete(ctx, "source-a")
	require.NoError(t, err)
	assert.Equal(t, "source-a", preview.DataSourceID)
	assert.Equal(t, 2, preview.GeneratedKnowledgeCount)
	assert.Equal(t, int64(24), preview.GeneratedStorageBytes)
	assert.Equal(t, 2, preview.SharedOrUnverifiableResourcesCount)
	assert.Equal(t, 0, preview.LegacyUnverifiableResourcesCount)
	require.NotEmpty(t, preview.PreviewToken)
	_, err = time.Parse(time.RFC3339, preview.ExpiresAt)
	require.NoError(t, err)
	assert.Equal(t, uint64(7), repo.listTenant)
	assert.Equal(t, "kb-a", repo.listKB)
	assert.Equal(t, "source-a", repo.listSource)

	result, err := svc.DeleteDataSourceWithMode(ctx, "source-a", &types.DataSourceDeleteRequest{
		Mode: types.DataSourceDeleteModePurgeGenerated, PreviewToken: preview.PreviewToken,
	})
	require.NoError(t, err)
	assert.True(t, result.DataSourceDeleted)
	assert.Equal(t, 2, result.PurgedKnowledge)
	assert.Equal(t, []string{"matched-a", "matched-b"}, knowledgeSvc.deletedIDs)
	assert.Equal(t, []string{"matched-a", "matched-b"}, repo.hardDeletedIDs)
	assert.Equal(t, []string{"source-a"}, dsRepo.deleteIDs)
}

func TestDataSourceDeletePurgeRejectsChangedCandidateSet(t *testing.T) {
	svc, repo, knowledgeSvc, dsRepo := newDataSourcePurgeService(t)
	ctx := ctxWithTenant(7)
	preview, err := svc.PreviewDataSourceDelete(ctx, "source-a")
	require.NoError(t, err)

	changed := &types.Knowledge{ID: "new-after-preview", TenantID: 7, KnowledgeBaseID: "kb-a", Title: "New", Type: "document", Metadata: types.JSON(`{"datasource_id":"source-a"}`)}
	repo.items = append(repo.items, changed)
	repo.byID[changed.ID] = changed

	_, err = svc.DeleteDataSourceWithMode(ctx, "source-a", &types.DataSourceDeleteRequest{
		Mode: types.DataSourceDeleteModePurgeGenerated, PreviewToken: preview.PreviewToken,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data source content changed")
	assert.Empty(t, knowledgeSvc.deletedIDs)
	assert.Empty(t, repo.hardDeletedIDs)
	assert.Empty(t, dsRepo.deleteIDs)
}

func TestDataSourceDeleteDetachPreservesGeneratedKnowledge(t *testing.T) {
	svc, _, knowledgeSvc, dsRepo := newDataSourcePurgeService(t)
	result, err := svc.DeleteDataSourceWithMode(ctxWithTenant(7), "source-a", &types.DataSourceDeleteRequest{
		Mode: types.DataSourceDeleteModeDetach,
	})
	require.NoError(t, err)
	assert.Equal(t, types.DataSourceDeleteModeDetach, result.Mode)
	assert.Empty(t, knowledgeSvc.deletedIDs)
	assert.Equal(t, []string{"source-a"}, dsRepo.deleteIDs)
}

func TestDataSourceDeletePurgeRequiresPreviewToken(t *testing.T) {
	svc, repo, knowledgeSvc, dsRepo := newDataSourcePurgeService(t)
	_, err := svc.DeleteDataSourceWithMode(ctxWithTenant(7), "source-a", &types.DataSourceDeleteRequest{
		Mode: types.DataSourceDeleteModePurgeGenerated,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preview_token is required")
	assert.Empty(t, knowledgeSvc.deletedIDs)
	assert.Empty(t, repo.hardDeletedIDs)
	assert.Empty(t, dsRepo.deleteIDs)
}

func TestDataSourceDeletePurgeBindsPreviewToRequestingActor(t *testing.T) {
	svc, repo, knowledgeSvc, dsRepo := newDataSourcePurgeService(t)
	ownerCtx := types.WithCaller(ctxWithTenant(7), types.Caller{TenantID: 7, UserID: "owner", Role: types.TenantRoleAdmin})
	preview, err := svc.PreviewDataSourceDelete(ownerCtx, "source-a")
	require.NoError(t, err)
	otherAdminCtx := types.WithCaller(ctxWithTenant(7), types.Caller{TenantID: 7, UserID: "another-admin", Role: types.TenantRoleAdmin})

	_, err = svc.DeleteDataSourceWithMode(otherAdminCtx, "source-a", &types.DataSourceDeleteRequest{
		Mode: types.DataSourceDeleteModePurgeGenerated, PreviewToken: preview.PreviewToken,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data source content changed")
	assert.Empty(t, knowledgeSvc.deletedIDs)
	assert.Empty(t, repo.hardDeletedIDs)
	assert.Empty(t, dsRepo.deleteIDs)
}

func TestDataSourceDeletePreviewRejectsRowsOutsideExactDataSourceOwnership(t *testing.T) {
	// This guard is deliberately duplicated at the service boundary: the
	// repository test verifies SQL scoping, while this test proves a forged row
	// returned by an alternative repository implementation never reaches the
	// knowledge deletion pipeline.
	svc, repo, knowledgeSvc, dsRepo := newDataSourcePurgeService(t)
	otherSource := &types.Knowledge{ID: "other-source", TenantID: 7, KnowledgeBaseID: "kb-a", Title: "Other", Type: "document", Metadata: types.JSON(`{"datasource_id":"source-b"}`)}
	otherKB := &types.Knowledge{ID: "other-kb", TenantID: 7, KnowledgeBaseID: "kb-b", Title: "Foreign", Type: "document", Metadata: types.JSON(`{"datasource_id":"source-a"}`)}
	repo.items = append(repo.items, otherSource, otherKB)
	repo.byID[otherSource.ID] = otherSource
	repo.byID[otherKB.ID] = otherKB

	preview, err := svc.PreviewDataSourceDelete(ctxWithTenant(7), "source-a")
	require.NoError(t, err)
	assert.Equal(t, 2, preview.GeneratedKnowledgeCount)
	result, err := svc.DeleteDataSourceWithMode(ctxWithTenant(7), "source-a", &types.DataSourceDeleteRequest{
		Mode: types.DataSourceDeleteModePurgeGenerated, PreviewToken: preview.PreviewToken,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.PurgedKnowledge)
	assert.Equal(t, []string{"matched-a", "matched-b"}, knowledgeSvc.deletedIDs)
	assert.Equal(t, []string{"matched-a", "matched-b"}, repo.hardDeletedIDs)
	assert.Equal(t, []string{"source-a"}, dsRepo.deleteIDs)
}

func TestDataSourceDeletePurgeDoesNotDetachWhenHardCleanupFails(t *testing.T) {
	svc, repo, knowledgeSvc, dsRepo := newDataSourcePurgeService(t)
	preview, err := svc.PreviewDataSourceDelete(ctxWithTenant(7), "source-a")
	require.NoError(t, err)
	repo.hardDeleteErr = assert.AnError

	_, err = svc.DeleteDataSourceWithMode(ctxWithTenant(7), "source-a", &types.DataSourceDeleteRequest{
		Mode: types.DataSourceDeleteModePurgeGenerated, PreviewToken: preview.PreviewToken,
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, []string{"matched-a", "matched-b"}, knowledgeSvc.deletedIDs)
	assert.Empty(t, repo.hardDeletedIDs)
	assert.Empty(t, dsRepo.deleteIDs)
}

func TestDataSourceDeletePurgeBlocksLegacyUnverifiableFiles(t *testing.T) {
	svc, repo, knowledgeSvc, dsRepo := newDataSourcePurgeService(t)
	legacy := &types.Knowledge{
		ID: "legacy-source", TenantID: 7, KnowledgeBaseID: "kb-a", Title: "Legacy", Type: "document",
		FilePath: "local://7/legacy/source.md", Metadata: types.JSON(`{"datasource_id":"source-a"}`),
	}
	repo.items = append(repo.items, legacy)
	repo.byID[legacy.ID] = legacy

	preview, err := svc.PreviewDataSourceDelete(ctxWithTenant(7), "source-a")
	require.NoError(t, err)
	assert.Equal(t, 1, preview.LegacyUnverifiableResourcesCount)
	require.Len(t, preview.LegacyFileSamples, 1)
	assert.Equal(t, "legacy-source", preview.LegacyFileSamples[0].KnowledgeID)
	assert.Empty(t, preview.LegacyFileSamples[0].FileName)
	assert.Equal(t, "legacy_unverifiable", preview.LegacyFileSamples[0].Status)

	_, err = svc.DeleteDataSourceWithMode(ctxWithTenant(7), "source-a", &types.DataSourceDeleteRequest{
		Mode: types.DataSourceDeleteModePurgeGenerated, PreviewToken: preview.PreviewToken,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy files")
	assert.Empty(t, knowledgeSvc.deletedIDs)
	assert.Empty(t, repo.hardDeletedIDs)
	assert.Empty(t, dsRepo.deleteIDs)
}
