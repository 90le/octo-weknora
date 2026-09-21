package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

const (
	dataSourceDeletePreviewTokenTTL = 10 * time.Minute
	dataSourceDeletePreviewVersion  = 1
	dataSourceDeletePreviewSamples  = 20
)

// dataSourceDeletePreviewToken contains no credentials or source contents.
// It binds an explicit destructive confirmation to one tenant, one source,
// one knowledge base, one authenticated actor, and the exact persisted
// candidate set shown in the preview.
type dataSourceDeletePreviewToken struct {
	Version          int    `json:"v"`
	DataSourceID     string `json:"d"`
	TenantID         uint64 `json:"t"`
	KnowledgeBaseID  string `json:"k"`
	PrincipalSubject string `json:"a"`
	CandidateDigest  string `json:"h"`
	ExpiresAtUnix    int64  `json:"e"`
}

type dataSourceDeleteSummary struct {
	knowledgeCount              int
	storageBytes                int64
	uncertainResources          int
	legacyUnverifiableResources int
	legacyFileSamples           []types.DataSourceGeneratedContentPurgeFile
	knowledgeSamples            []types.DataSourceGeneratedContentPurgeItem
}

// dataSourceKnowledgeLister is intentionally narrower than the shared
// KnowledgeRepository contract. It keeps a data-source-specific read path from
// forcing every existing test or third-party repository implementation to grow
// a method it cannot safely implement.
type dataSourceKnowledgeLister interface {
	ListByDataSourceIDIncludingDeleted(
		ctx context.Context, tenantID uint64, kbID, dataSourceID string,
	) ([]*types.Knowledge, error)
}

// PreviewDataSourceDelete gives an admin a bounded, non-mutating assessment of
// an explicit source removal. Normal DELETE keeps its legacy detach behavior;
// this preview only becomes destructive when its signed token is supplied to
// DeleteDataSourceWithMode with mode=purge_generated.
func (s *DataSourceService) PreviewDataSourceDelete(
	ctx context.Context,
	id string,
) (*types.DataSourceDeletePreview, error) {
	ds, items, summary, err := s.dataSourceGeneratedContent(ctx, id)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().UTC().Add(dataSourceDeletePreviewTokenTTL)
	token, err := signDataSourceDeletePreview(dataSourceDeletePreviewToken{
		Version:          dataSourceDeletePreviewVersion,
		DataSourceID:     ds.ID,
		TenantID:         ds.TenantID,
		KnowledgeBaseID:  ds.KnowledgeBaseID,
		PrincipalSubject: dataSourceDeletePrincipalSubject(ctx),
		CandidateDigest:  dataSourceDeleteCandidateDigest(ds, items),
		ExpiresAtUnix:    expiresAt.Unix(),
	})
	if err != nil {
		return nil, err
	}
	return &types.DataSourceDeletePreview{
		DataSourceID:                       ds.ID,
		GeneratedKnowledgeCount:            summary.knowledgeCount,
		GeneratedStorageBytes:              summary.storageBytes,
		SharedOrUnverifiableResourcesCount: summary.uncertainResources,
		LegacyUnverifiableResourcesCount:   summary.legacyUnverifiableResources,
		PreviewToken:                       token,
		ExpiresAt:                          expiresAt.Format(time.RFC3339),
		KnowledgeSamples:                   summary.knowledgeSamples,
		LegacyFileSamples:                  summary.legacyFileSamples,
	}, nil
}

// DeleteDataSourceWithMode implements the explicit delete contract used by the
// UI. "detach" intentionally delegates to DeleteDataSource and therefore
// retains imported knowledge. "purge_generated" requires a fresh signed
// preview, then deletes only metadata-proven knowledge before detaching the
// source configuration.
func (s *DataSourceService) DeleteDataSourceWithMode(
	ctx context.Context,
	id string,
	req *types.DataSourceDeleteRequest,
) (*types.DataSourceDeleteResult, error) {
	if req == nil {
		return nil, apperrors.NewBadRequestError("data source delete request is required")
	}
	switch strings.TrimSpace(req.Mode) {
	case types.DataSourceDeleteModeDetach:
		if err := s.DeleteDataSource(ctx, id); err != nil {
			return nil, err
		}
		return &types.DataSourceDeleteResult{
			DataSourceID: id, Mode: types.DataSourceDeleteModeDetach, DataSourceDeleted: true,
		}, nil
	case types.DataSourceDeleteModePurgeGenerated:
		return s.purgeDataSourceGeneratedContent(ctx, id, req.PreviewToken)
	default:
		return nil, apperrors.NewBadRequestError("mode must be detach or purge_generated")
	}
}

func (s *DataSourceService) purgeDataSourceGeneratedContent(
	ctx context.Context,
	id, rawToken string,
) (*types.DataSourceDeleteResult, error) {
	if strings.TrimSpace(rawToken) == "" {
		return nil, apperrors.NewBadRequestError("preview_token is required for purge_generated")
	}
	ds, items, summary, err := s.dataSourceGeneratedContent(ctx, id)
	if err != nil {
		return nil, err
	}
	token, err := verifyDataSourceDeletePreview(rawToken)
	if err != nil {
		if appErr, ok := apperrors.IsAppError(err); ok {
			return nil, appErr
		}
		return nil, apperrors.NewConflictError("delete preview is invalid or expired; request a new preview")
	}
	if token.Version != dataSourceDeletePreviewVersion ||
		token.DataSourceID != ds.ID || token.TenantID != ds.TenantID ||
		token.KnowledgeBaseID != ds.KnowledgeBaseID ||
		token.PrincipalSubject != dataSourceDeletePrincipalSubject(ctx) ||
		token.CandidateDigest != dataSourceDeleteCandidateDigest(ds, items) {
		return nil, apperrors.NewConflictError("data source content changed; request a new delete preview")
	}
	if summary.legacyUnverifiableResources > 0 {
		return nil, apperrors.NewConflictError("data source contains legacy files without reference-safe ownership; migrate or audit them before purging generated content")
	}

	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ID)
		}
	}
	// deleteReferencedKnowledge admits only the exact IDs read from the
	// persisted tenant+KB+datasource metadata relationship. It revalidates the
	// tenant and KB bindings before using KnowledgeService's full cleanup path
	// (vectors, chunks, graph, tags and resource/file cleanup).
	if err := deleteReferencedKnowledge(
		types.WithExecutionTenant(ctx, ds.TenantID),
		s.knowledgeService,
		ds.KnowledgeBaseID,
		ids,
	); err != nil {
		return nil, err
	}
	// Sync-internal deletion semantics remove successful source entries rather
	// than leaving tombstones that would block a later re-sync of the same
	// external ID. Do this only after the full KnowledgeService cleanup above
	// completed; a cleanup failure keeps both the source configuration and the
	// remaining entries available for a safe retry.
	if len(ids) > 0 {
		if err := s.knowledgeService.GetRepository().HardDeleteKnowledgeList(ctx, ds.TenantID, ids); err != nil {
			return nil, err
		}
	}
	// Do this last. If generated-content cleanup fails, the data source remains
	// configured so the caller can retry after resolving the underlying issue.
	if err := s.DeleteDataSource(ctx, ds.ID); err != nil {
		return nil, err
	}
	return &types.DataSourceDeleteResult{
		DataSourceID: ds.ID, Mode: types.DataSourceDeleteModePurgeGenerated,
		PurgedKnowledge: len(ids), DataSourceDeleted: true,
	}, nil
}

func (s *DataSourceService) dataSourceGeneratedContent(
	ctx context.Context,
	id string,
) (*types.DataSource, []*types.Knowledge, dataSourceDeleteSummary, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil, dataSourceDeleteSummary{}, apperrors.NewBadRequestError("data source ID is required")
	}
	ds, err := s.dsRepo.FindByID(ctx, id)
	if err != nil {
		return nil, nil, dataSourceDeleteSummary{}, err
	}
	if s.knowledgeService == nil || s.knowledgeService.GetRepository() == nil {
		return nil, nil, dataSourceDeleteSummary{}, apperrors.NewServiceUnavailableError("knowledge cleanup service is unavailable")
	}
	lister, ok := s.knowledgeService.GetRepository().(dataSourceKnowledgeLister)
	if !ok {
		return nil, nil, dataSourceDeleteSummary{}, apperrors.NewServiceUnavailableError("data source content cleanup is unavailable")
	}
	items, err := lister.ListByDataSourceIDIncludingDeleted(
		ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID,
	)
	if err != nil {
		return nil, nil, dataSourceDeleteSummary{}, err
	}
	// The repository query is already exact, but retain this in-memory fence at
	// the destructive boundary. A custom repository implementation, stale
	// replica, or future query refactor must not be able to widen deletion past
	// the persisted datasource_id ownership contract.
	items = exactDataSourceKnowledge(ds, items)
	return ds, items, summarizeDataSourceDelete(items), nil
}

// dataSourceDeletePrincipalSubject binds a destructive preview to the actual
// authenticated subject. API keys must use their KeyID rather than the tenant's
// synthetic user, otherwise two keys in the same tenant could consume each
// other's preview token. Human and other principals use their stable principal
// identity, with Caller as the legacy-service fallback.
func dataSourceDeletePrincipalSubject(ctx context.Context) string {
	if scope, ok := types.TenantAPIKeyScopeFromContext(ctx); ok && scope.KeyID > 0 {
		tenantID, _ := types.TenantIDFromContext(ctx)
		return fmt.Sprintf("api_key:%d:%d", tenantID, scope.KeyID)
	}
	if principal, ok := types.PrincipalFromContext(ctx); ok && principal.StorageID() != "" {
		return "principal:" + principal.StorageID()
	}
	if caller := types.CallerFromContext(ctx); strings.TrimSpace(caller.UserID) != "" {
		return "user:" + strings.TrimSpace(caller.UserID)
	}
	return ""
}

func exactDataSourceKnowledge(ds *types.DataSource, items []*types.Knowledge) []*types.Knowledge {
	if ds == nil {
		return nil
	}
	matched := make([]*types.Knowledge, 0, len(items))
	for _, item := range items {
		if item == nil || item.ID == "" || item.TenantID != ds.TenantID || item.KnowledgeBaseID != ds.KnowledgeBaseID {
			continue
		}
		if item.GetMetadata()["datasource_id"] != ds.ID {
			continue
		}
		matched = append(matched, item)
	}
	return matched
}

func summarizeDataSourceDelete(items []*types.Knowledge) dataSourceDeleteSummary {
	summary := dataSourceDeleteSummary{
		knowledgeSamples:  make([]types.DataSourceGeneratedContentPurgeItem, 0),
		legacyFileSamples: make([]types.DataSourceGeneratedContentPurgeFile, 0),
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		summary.knowledgeCount++
		if item.StorageSize > 0 {
			summary.storageBytes += item.StorageSize
		}
		if len(summary.knowledgeSamples) < dataSourceDeletePreviewSamples {
			summary.knowledgeSamples = append(summary.knowledgeSamples, types.DataSourceGeneratedContentPurgeItem{
				KnowledgeID: item.ID, Title: item.Title, FileName: item.FileName, Type: item.Type,
			})
		}
		if item.FilePath == "" {
			continue
		}
		// A catalog reference can still have another live claimant until the
		// release step proves otherwise. A raw legacy path has no binding proof.
		summary.uncertainResources++
		if _, isCatalogReference := types.ParseResourcePath(item.FilePath); !isCatalogReference {
			// Count every legacy resource even when the bounded sample list is
			// full. These are an explicit safety gate for purge_generated.
			summary.legacyUnverifiableResources++
			if len(summary.legacyFileSamples) < dataSourceDeletePreviewSamples {
				summary.legacyFileSamples = append(summary.legacyFileSamples, types.DataSourceGeneratedContentPurgeFile{
					KnowledgeID: item.ID, Title: item.Title, FileName: item.FileName, Status: "legacy_unverifiable",
				})
			}
		}
	}
	return summary
}

func dataSourceDeleteCandidateDigest(ds *types.DataSource, items []*types.Knowledge) string {
	records := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		records = append(records, strings.Join([]string{
			item.ID,
			item.KnowledgeBaseID,
			item.FilePath,
			fmt.Sprintf("%d", item.StorageSize),
			item.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}, "\x00"))
	}
	sort.Strings(records)
	h := sha256.New()
	_, _ = h.Write([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00", ds.ID, ds.TenantID, ds.KnowledgeBaseID)))
	for _, record := range records {
		_, _ = h.Write([]byte(record))
		_, _ = h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func signDataSourceDeletePreview(payload dataSourceDeletePreviewToken) (string, error) {
	key := secutils.SystemHMACKey()
	if len(key) == 0 {
		return "", apperrors.NewServiceUnavailableError("delete preview signing is unavailable")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal delete preview token: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyDataSourceDeletePreview(rawToken string) (*dataSourceDeletePreviewToken, error) {
	key := secutils.SystemHMACKey()
	if len(key) == 0 {
		return nil, apperrors.NewServiceUnavailableError("delete preview signing is unavailable")
	}
	parts := strings.Split(strings.TrimSpace(rawToken), ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid delete preview token")
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode delete preview token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode delete preview signature")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(rawPayload)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return nil, fmt.Errorf("delete preview signature mismatch")
	}
	var payload dataSourceDeletePreviewToken
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return nil, fmt.Errorf("decode delete preview payload")
	}
	if payload.ExpiresAtUnix == 0 || time.Now().UTC().After(time.Unix(payload.ExpiresAtUnix, 0)) {
		return nil, fmt.Errorf("delete preview expired")
	}
	return &payload, nil
}
