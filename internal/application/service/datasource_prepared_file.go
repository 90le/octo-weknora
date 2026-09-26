package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/types"
)

// A prepared repository file is identified by its source and path, not by a
// KB-wide content hash. Keep this authority in a private context value: file
// upload callers can provide metadata and a channel label, but cannot request
// the duplicate-content exception by supplying either of them.
type preparedFileImportContextKey struct{}

var errPreparedFileOwnedByAnotherSource = errors.New("document duplicates another source; existing document was preserved")

type preparedFileImportScope struct {
	tenantID  uint64
	kbID      string
	sourceID  string
	candidate string
}

func withPreparedFileImport(ctx context.Context, ds *types.DataSource, candidate string) context.Context {
	if ds == nil || candidate == "" || ds.ID == "" || ds.KnowledgeBaseID == "" ||
		(ds.Type != types.ConnectorTypeGitHub && ds.Type != localfolder.Type) {
		return ctx
	}
	return context.WithValue(ctx, preparedFileImportContextKey{}, preparedFileImportScope{
		tenantID: ds.TenantID, kbID: ds.KnowledgeBaseID, sourceID: ds.ID, candidate: candidate,
	})
}

func isPreparedFileImport(ctx context.Context, tenantID uint64, kbID string, metadata map[string]string) bool {
	scope, ok := ctx.Value(preparedFileImportContextKey{}).(preparedFileImportScope)
	return ok && metadata != nil && scope.tenantID == tenantID && scope.kbID == kbID &&
		scope.sourceID != "" && scope.candidate != "" &&
		metadata["datasource_id"] == scope.sourceID && metadata["external_id"] == scope.candidate
}

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
		if sourceItemVersion(oldMeta) == sourceItemVersion(item.Metadata) && indexedForSync(old) {
			return false, types.NewDuplicateFileError(old)
		}
		if ds.ConflictStrategy == types.ConflictStrategySkip {
			return false, types.NewDuplicateFileError(old)
		}
	}
	if len(item.Content) == 0 || sourceItemVersion(item.Metadata) == "" {
		return old != nil, fmt.Errorf("repository document has no content or version")
	}
	candidateID := item.ExternalID + ":pending:" + sourceItemVersion(item.Metadata)
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
		candidate, err = s.knowledgeService.CreateKnowledgeFromFile(
			withPreparedFileImport(ctx, ds, candidateID), ds.KnowledgeBaseID, fh, metadata, nil, item.FileName, tags, ds.Type, nil)
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
				return old != nil, errPreparedFileOwnedByAnotherSource
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
	// The adoption sequence is shared with restart recovery. It is deliberately
	// extracted rather than duplicated: both paths must wait for the native
	// parser, preserve parser-written metadata, publish the pinned source URL,
	// and never retire a canonical document they did not explicitly own.
	outcome, err := s.finalizePreparedCandidate(ctx, ds, candidate, old, item.Metadata)
	if err != nil {
		return old != nil, err
	}
	if outcome != preparedCandidateFinalizePublished {
		return old != nil, fmt.Errorf("repository document candidate cannot be published: %s", outcome)
	}
	return old != nil, nil
}

type preparedCandidateFinalizeOutcome string

const (
	preparedCandidateFinalizePublished preparedCandidateFinalizeOutcome = "published"
	preparedCandidateFinalizeBlocked   preparedCandidateFinalizeOutcome = "blocked"
	preparedCandidateFinalizeNotReady  preparedCandidateFinalizeOutcome = "not_ready"
)

// finalizePreparedCandidate turns a fully indexed repository-document
// candidate into its canonical external_id. previous is non-nil only for the
// normal sync replacement path; restart recovery passes nil, so the presence
// of any canonical target is a hard block and no existing document is deleted.
//
// The caller must supply a fresh candidate read. This function only writes
// metadata/source after indexedForSync returns true, preventing a parser or
// enrichment worker from having its in-flight state overwritten.
func (s *DataSourceService) finalizePreparedCandidate(
	ctx context.Context,
	ds *types.DataSource,
	candidate, previous *types.Knowledge,
	// sourceMetadata is the connector's original immutable metadata. Native
	// parsing is allowed to add fields, but older parsers may rewrite metadata;
	// normal sync passes this fallback so source_version/github_url survive.
	sourceMetadata map[string]string,
) (preparedCandidateFinalizeOutcome, error) {
	if ds == nil || candidate == nil {
		return preparedCandidateFinalizeBlocked, errors.New("prepared candidate requires data source and knowledge")
	}
	if !indexedForSync(candidate) {
		return preparedCandidateFinalizeNotReady, nil
	}
	if candidate.TenantID != ds.TenantID || candidate.KnowledgeBaseID != ds.KnowledgeBaseID {
		return preparedCandidateFinalizeBlocked, nil
	}
	var metadata map[string]string
	if err := json.Unmarshal(candidate.Metadata, &metadata); err != nil {
		return preparedCandidateFinalizeBlocked, fmt.Errorf("decode prepared candidate metadata: %w", err)
	}
	for key, value := range sourceMetadata {
		if _, exists := metadata[key]; !exists {
			metadata[key] = value
		}
	}
	targetID := metadata["sync_target_external_id"]
	if targetID == "" || metadata["datasource_id"] != ds.ID {
		return preparedCandidateFinalizeBlocked, nil
	}
	repo := s.knowledgeService.GetRepository()
	canonical, err := repo.FindByDataSourceExternalID(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID, targetID)
	if err != nil {
		return preparedCandidateFinalizeBlocked, err
	}
	if canonical != nil && (previous == nil || canonical.ID != previous.ID) {
		// Recovery never removes a canonical target it did not create. This
		// includes an operator/manual re-sync that completed after preview.
		return preparedCandidateFinalizeBlocked, nil
	}
	if sourceURL := metadata["github_url"]; sourceURL != "" {
		// Native retrieval and document details expose Knowledge.Source, not
		// connector-specific metadata. Publish the pinned URL before retiring old.
		if err := repo.UpdateKnowledgeColumn(ctx, candidate.ID, "source", sourceURL); err != nil {
			return preparedCandidateFinalizeBlocked, err
		}
	}
	if previous != nil && previous.ID != candidate.ID {
		if err := s.knowledgeService.DeleteKnowledge(ctx, previous.ID); err != nil {
			return preparedCandidateFinalizeBlocked, fmt.Errorf("new version ready; previous version cleanup failed: %w", err)
		}
		if err := repo.HardDeleteKnowledge(ctx, ds.TenantID, previous.ID); err != nil {
			return preparedCandidateFinalizeBlocked, err
		}
	}
	// Update only metadata, never write stale parse/enable fields over the
	// native asynchronous enrichment worker's state. Retain metadata written by
	// the native parser when adopting the candidate.
	metadata["external_id"] = targetID
	delete(metadata, "sync_target_external_id")
	b, err := json.Marshal(metadata)
	if err != nil {
		return preparedCandidateFinalizeBlocked, err
	}
	if err = repo.UpdateKnowledgeColumn(ctx, candidate.ID, "metadata", types.JSON(b)); err != nil {
		return preparedCandidateFinalizeBlocked, err
	}
	return preparedCandidateFinalizePublished, nil
}

func indexedForSync(k *types.Knowledge) bool {
	// Enabled means searchable, not quiescent: the parser may still save its
	// original metadata during processing/finalizing. Adopt only after it finishes.
	return k != nil && k.EnableStatus == "enabled" && k.ProcessedAt != nil && k.ParseStatus == types.ParseStatusCompleted
}

func sourceItemVersion(meta map[string]string) string {
	if value := meta["source_version"]; value != "" {
		return value
	}
	return meta["github_blob_sha"]
}
