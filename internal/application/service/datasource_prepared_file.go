package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ingestPreparedFile is used by the new repository document connector. Existing
// connectors retain their old behavior until migrated and tested explicitly.
// It stages a deterministic candidate, waits for the native parser/indexer,
// then retires the previous version. It does NOT promise atomic whole-KB reads.
func (s *DataSourceService) ingestPreparedFile(ctx context.Context, ds *types.DataSource, item *types.FetchedItem, tags []string) (bool, error) {
	repo := s.knowledgeService.GetRepository()
	old, err := repo.FindByDataSourceExternalID(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID, item.ExternalID)
	if err != nil {
		return false, err
	}
	var oldMeta map[string]string
	if old != nil {
		if err := json.Unmarshal(old.Metadata, &oldMeta); err != nil {
			return false, err
		}
		if oldMeta["github_blob_sha"] == item.Metadata["github_blob_sha"] && indexedForSync(old) {
			return false, types.NewDuplicateFileError(old)
		}
		if ds.ConflictStrategy == types.ConflictStrategySkip {
			return false, types.NewDuplicateFileError(old)
		}
	}
	if len(item.Content) == 0 || item.Metadata["github_blob_sha"] == "" {
		return old != nil, fmt.Errorf("repository document has no content or version")
	}
	candidateID := item.ExternalID + ":pending:" + item.Metadata["github_blob_sha"]
	candidate, err := repo.FindByDataSourceExternalID(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID, candidateID)
	if err != nil {
		return old != nil, err
	}
	metadata := map[string]string{}
	for k, v := range item.Metadata {
		metadata[k] = v
	}
	metadata["external_id"], metadata["datasource_id"] = candidateID, ds.ID
	metadata["source_resource_id"], metadata["sync_target_external_id"] = item.SourceResourceID, item.ExternalID
	if candidate == nil {
		fh, err := bytesToFileHeader(item.Content, item.FileName)
		if err != nil {
			return old != nil, err
		}
		candidate, err = s.knowledgeService.CreateKnowledgeFromFile(ctx, ds.KnowledgeBaseID, fh, metadata, nil, item.FileName, tags, ds.Type, nil)
		if err != nil {
			// Do not adopt a duplicate belonging to another source or path.
			var duplicate *types.DuplicateKnowledgeError
			if !errors.As(err, &duplicate) {
				return old != nil, err
			}
			candidate, err = repo.FindByDataSourceExternalID(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID, candidateID)
			if err != nil {
				return old != nil, err
			}
			if candidate == nil {
				return old != nil, fmt.Errorf("document duplicates another source; existing document was preserved")
			}
		}
	}
	if candidate == nil {
		return old != nil, fmt.Errorf("document creation returned no candidate")
	}
	// A timeout leaves the candidate available for the next retry; a failed
	// parse is cleaned up so it can be retried without deleting the old version.
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		candidate, err = repo.GetKnowledgeByID(waitCtx, ds.TenantID, candidate.ID)
		if err != nil {
			return old != nil, err
		}
		if candidate == nil || candidate.KnowledgeBaseID != ds.KnowledgeBaseID {
			return old != nil, fmt.Errorf("staged document is unavailable")
		}
		if candidate.ParseStatus == types.ParseStatusFailed || candidate.ParseStatus == types.ParseStatusCancelled {
			if err := s.knowledgeService.DeleteKnowledge(ctx, candidate.ID); err == nil {
				_ = repo.HardDeleteKnowledge(ctx, ds.TenantID, candidate.ID)
			}
			return old != nil, fmt.Errorf("new document parsing failed; previous version preserved")
		}
		if indexedForSync(candidate) {
			break
		}
		select {
		case <-waitCtx.Done():
			return old != nil, fmt.Errorf("document indexing is not ready; previous version preserved: %w", waitCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	if old != nil {
		if err := s.knowledgeService.DeleteKnowledge(ctx, old.ID); err != nil {
			return true, fmt.Errorf("new version ready; previous version cleanup failed: %w", err)
		}
		if err := repo.HardDeleteKnowledge(ctx, ds.TenantID, old.ID); err != nil {
			return true, err
		}
	}
	// Update only metadata, never write stale parse/enable fields over the
	// native asynchronous enrichment worker's state.
	metadata["external_id"] = item.ExternalID
	delete(metadata, "sync_target_external_id")
	b, err := json.Marshal(metadata)
	if err != nil {
		return old != nil, err
	}
	if err = repo.UpdateKnowledgeColumn(ctx, candidate.ID, "metadata", types.JSON(b)); err != nil {
		return old != nil, err
	}
	return old != nil, nil
}

func indexedForSync(k *types.Knowledge) bool {
	return k != nil && k.EnableStatus == "enabled" && k.ProcessedAt != nil &&
		(k.ParseStatus == types.ParseStatusCompleted || k.ParseStatus == types.ParseStatusProcessing || k.ParseStatus == types.ParseStatusFinalizing)
}
