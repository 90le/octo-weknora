package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	githubConnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const (
	githubDocumentChunkItems = 24
	githubDocumentChunkBytes = 64 << 20
	githubDocumentLeaseTime  = 90 * time.Second
)

// GitHub document progress is an optional extension of the established data
// source repository. Production SQL implements it; alternate adapters fail
// closed instead of reverting to all-at-once REST/blob ingestion.
type githubDocumentProgressStore interface {
	FindRunningGitHubDocumentRun(context.Context, uint64, string, string) (*types.GitHubDocumentRun, error)
	CreateGitHubDocumentRun(context.Context, *types.GitHubDocumentRun, []types.GitHubDocumentSyncItem) error
	ListGitHubDocumentRunItems(context.Context, *types.GitHubDocumentRun) ([]types.GitHubDocumentSyncItem, error)
	UpdateGitHubDocumentItem(context.Context, *types.GitHubDocumentRun, string, types.GitHubDocumentSyncItem, string, string, string) error
	SupersedeGitHubDocumentRun(context.Context, *types.GitHubDocumentRun) error
	AcquireGitHubDocumentRunLease(context.Context, *types.GitHubDocumentRun, string, time.Time) (bool, error)
	RenewGitHubDocumentRunLease(context.Context, *types.GitHubDocumentRun, string, time.Time) (bool, error)
	ReleaseGitHubDocumentRunLease(context.Context, *types.GitHubDocumentRun, string) error
	PublishGitHubDocumentRun(context.Context, *types.GitHubDocumentRun, string, *types.DataSource) error
}

// The HMAC scope is only an opaque equality marker for a configured token.
// The token itself is never stored in a progress row or log. A key rotation
// invalidates unfinished runs while leaving LastSyncCursor untouched.
func githubDocumentCredentialScope(ds *types.DataSource, cfg *types.DataSourceConfig) (string, error) {
	if ds == nil || cfg == nil {
		return "", errors.New("GitHub document credentials are unavailable")
	}
	token, _ := cfg.Credentials["access_token"].(string)
	token = strings.TrimSpace(token)
	key := secutils.GetAESKey()
	if token == "" {
		var persisted struct {
			Credentials map[string]interface{} `json:"credentials"`
		}
		if err := json.Unmarshal(ds.Config, &persisted); err != nil {
			return "", errors.New("GitHub document credential configuration is invalid")
		}
		storedToken, _ := persisted.Credentials["access_token"].(string)
		if strings.HasPrefix(storedToken, secutils.EncPrefix) {
			return "", errors.New("GitHub document credentials cannot be decrypted; check SYSTEM_AES_KEY")
		}
		return "public", nil
	}
	if key == nil {
		return "", errors.New("GitHub private document sync requires SYSTEM_AES_KEY")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("github-document-sync-credential-v1\x00"))
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func githubDocumentPathHash(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func newGitHubDocumentRun(
	ds *types.DataSource, plan *githubConnector.DocumentPlan, credentialScope string, forceFull bool,
) (*types.GitHubDocumentRun, []types.GitHubDocumentSyncItem, error) {
	cursorJSON, err := plan.Cursor().ToJSON()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	run := &types.GitHubDocumentRun{
		ID: uuid.NewString(), TenantID: ds.TenantID, KnowledgeBaseID: ds.KnowledgeBaseID, DataSourceID: ds.ID,
		Selection: plan.Selection, CommitSHA: plan.Commit, PlanDigest: plan.Digest, CredentialScope: credentialScope,
		TargetCursor: cursorJSON, ForceFull: forceFull, Status: types.GitHubDocumentRunRunning, CreatedAt: now, UpdatedAt: now,
	}
	items := make([]types.GitHubDocumentSyncItem, 0, len(plan.Upserts)+len(plan.Deletions))
	seen := make(map[string]string)
	add := func(item githubConnector.DocumentPlanItem, operation string) error {
		hash := githubDocumentPathHash(item.Path)
		if previous, exists := seen[hash]; exists && previous != item.Path {
			return errors.New("GitHub document path identity collision")
		}
		seen[hash] = item.Path
		items = append(items, types.GitHubDocumentSyncItem{
			RunID: run.ID, PathHash: hash, Path: item.Path, BlobSHA: item.BlobSHA,
			Operation: operation, Status: types.GitHubDocumentItemPending, UpdatedAt: now,
		})
		return nil
	}
	for _, item := range plan.Upserts {
		if err := add(item, types.GitHubDocumentItemUpsert); err != nil {
			return nil, nil, err
		}
	}
	if ds.SyncDeletions {
		for _, item := range plan.Deletions {
			if err := add(item, types.GitHubDocumentItemDelete); err != nil {
				return nil, nil, err
			}
		}
	}
	return run, items, nil
}

func (s *DataSourceService) processGitHubDocumentRun(
	ctx context.Context, connector *githubConnector.Connector, ds *types.DataSource,
	cfg *types.DataSourceConfig, syncLog *types.SyncLog, payload types.DataSourceSyncPayload,
	wasPaused bool, runGuard *syncRunGuard, accessGuard *syncAccessGuard,
) error {
	store, ok := s.dsRepo.(githubDocumentProgressStore)
	if !ok || store == nil {
		cause := errors.New("GitHub document checkpoint storage is unavailable")
		return s.failSyncRun(ctx, ds, syncLog, nil, cause.Error(), wasPaused, cause, false)
	}
	credentialScope, err := githubDocumentCredentialScope(ds, cfg)
	if err != nil {
		return s.failSyncRun(ctx, ds, syncLog, nil, err.Error(), wasPaused, err, false)
	}
	selection, err := githubConnector.DocumentSelection(cfg)
	if err != nil {
		return s.failSyncRun(ctx, ds, syncLog, nil, "GitHub document selection is invalid", wasPaused, err, false)
	}
	published, err := ds.ParseSyncCursor()
	if err != nil {
		return s.failSyncRun(ctx, ds, syncLog, nil, "GitHub document cursor is invalid", wasPaused, err, false)
	}
	run, err := store.FindRunningGitHubDocumentRun(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID)
	if err != nil {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
	}
	if run != nil && (run.Selection != selection || run.CredentialScope != credentialScope) {
		if err := store.SupersedeGitHubDocumentRun(ctx, run); err != nil {
			return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
		}
		run = nil
	}
	forceFull := payload.ForceFull || ds.SyncMode == types.SyncModeFull
	pinCommit := ""
	if run != nil {
		forceFull, pinCommit = run.ForceFull, run.CommitSHA
	}
	plan, err := connector.PlanDocuments(ctx, cfg, published, forceFull, pinCommit)
	if err != nil {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
	}
	if run != nil && (run.PlanDigest != plan.Digest || run.CommitSHA != plan.Commit || plan.Complete) {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused,
			errors.New("GitHub document checkpoint no longer matches the fixed-commit plan"))
	}
	if plan.Complete && run == nil {
		// This is the previous fully published cursor. Even if the generated Git
		// cache was cleaned, unchanged remote content needs no re-indexing.
		ds.LastSyncAt = timePtr(time.Now().UTC())
		result := &types.SyncResult{}
		data, _ := result.ToJSON()
		return s.updateSyncRunResult(ctx, ds, syncLog, result, data, types.SyncLogStatusSuccess, "", wasPaused, false)
	}
	if run == nil {
		var items []types.GitHubDocumentSyncItem
		run, items, err = newGitHubDocumentRun(ds, plan, credentialScope, forceFull)
		if err != nil {
			return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
		}
		if err = store.CreateGitHubDocumentRun(ctx, run, items); err != nil {
			return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
		}
	}
	leaseID := uuid.NewString()
	acquired, err := store.AcquireGitHubDocumentRunLease(ctx, run, leaseID, time.Now().UTC().Add(githubDocumentLeaseTime))
	if err != nil {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
	}
	if !acquired {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, errors.New("GitHub document sync is already processing this source"))
	}
	workCtx, cancel := context.WithCancel(ctx)
	stopHeartbeat := startGitHubDocumentLeaseHeartbeat(workCtx, cancel, store, run, leaseID)
	defer func() {
		stopHeartbeat()
		cancel()
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer releaseCancel()
		if releaseErr := store.ReleaseGitHubDocumentRunLease(releaseCtx, run, leaseID); releaseErr != nil {
			logger.Warnf(releaseCtx, "GitHub document run lease release failed: %v", releaseErr)
		}
	}()
	ctx = workCtx
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, nil, wasPaused, err)
	}
	if err := accessGuard.check(ctx); err != nil {
		return s.stopSyncAfterAccessChange(ctx, ds, syncLog, nil, wasPaused, err)
	}
	items, err := store.ListGitHubDocumentRunItems(ctx, run)
	if err != nil {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
	}
	if err := validateGitHubDocumentRunItems(plan, ds.SyncDeletions, items); err != nil {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
	}
	ctx = context.WithValue(ctx, types.TenantIDContextKey, ds.TenantID)
	tenant, err := s.tenantRepo.GetTenantByID(ctx, ds.TenantID)
	if err != nil {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
	}
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	tagIDs := s.resolveAutoTagIDs(ctx, ds)
	return s.processGitHubDocumentChunk(ctx, connector, store, ds, cfg, syncLog, payload,
		wasPaused, runGuard, accessGuard, run, leaseID, plan, items, tagIDs)
}

func startGitHubDocumentLeaseHeartbeat(
	ctx context.Context, cancel context.CancelFunc, store githubDocumentProgressStore,
	run *types.GitHubDocumentRun, leaseID string,
) func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				ok, err := store.RenewGitHubDocumentRunLease(renewCtx, run, leaseID, time.Now().UTC().Add(githubDocumentLeaseTime))
				renewCancel()
				if err != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done); <-exited }
}

func validateGitHubDocumentRunItems(plan *githubConnector.DocumentPlan, syncDeletions bool, items []types.GitHubDocumentSyncItem) error {
	expected := make(map[string]types.GitHubDocumentSyncItem, len(plan.Upserts)+len(plan.Deletions))
	add := func(path, sha, operation string) {
		expected[githubDocumentPathHash(path)] = types.GitHubDocumentSyncItem{
			Path: path, BlobSHA: sha, Operation: operation,
		}
	}
	for _, item := range plan.Upserts {
		add(item.Path, item.BlobSHA, types.GitHubDocumentItemUpsert)
	}
	if syncDeletions {
		for _, item := range plan.Deletions {
			add(item.Path, "", types.GitHubDocumentItemDelete)
		}
	}
	if len(items) != len(expected) {
		return errors.New("GitHub document checkpoint item count does not match its fixed-commit plan")
	}
	for _, item := range items {
		want, ok := expected[item.PathHash]
		if !ok || item.Path != want.Path || item.BlobSHA != want.BlobSHA || item.Operation != want.Operation {
			return errors.New("GitHub document checkpoint item identity changed")
		}
	}
	return nil
}

func githubDocumentProgressResult(items []types.GitHubDocumentSyncItem) *types.SyncResult {
	result := &types.SyncResult{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case types.GitHubDocumentItemReady:
			switch item.Outcome {
			case types.GitHubDocumentOutcomeCreated:
				result.Created++
			case types.GitHubDocumentOutcomeUpdated:
				result.Updated++
			case types.GitHubDocumentOutcomeDeleted:
				result.Deleted++
			default:
				result.Skipped++
			}
		case types.GitHubDocumentItemFailed:
			result.Failed++
			if item.Operation == types.GitHubDocumentItemDelete {
				result.DeletionFailed++
			}
			code := item.ErrorCode
			if code == "" {
				code = "ingest_failed"
			}
			recordSyncError(result, types.SyncItemError{Title: item.Path, Code: code, Message: "GitHub document did not sync; the previous version remains available"})
		}
	}
	return result
}

func (s *DataSourceService) checkpointGitHubDocumentProgress(
	ctx context.Context, syncLog *types.SyncLog, items []types.GitHubDocumentSyncItem,
) error {
	result := githubDocumentProgressResult(items)
	syncLog.ItemsTotal, syncLog.ItemsCreated, syncLog.ItemsUpdated = result.Total, result.Created, result.Updated
	syncLog.ItemsDeleted, syncLog.ItemsSkipped, syncLog.ItemsFailed = result.Deleted, result.Skipped, result.Failed
	syncLog.Result, _ = result.ToJSON()
	applied, err := s.syncLogRepo.UpdateResultIfRunning(ctx, syncLog)
	if err != nil {
		return err
	}
	if !applied {
		return stopSyncAfterRunTermination(errSyncRunTerminated)
	}
	return nil
}

func (s *DataSourceService) githubDocumentRunFailure(
	ctx context.Context, ds *types.DataSource, log *types.SyncLog, wasPaused bool, cause error,
) error {
	if cause == nil {
		return nil
	}
	// The GitHub-specific permanent/transient policy is added by the separate
	// scheduler retry PR. Until it is rebased, keep errors visible and retryable.
	logger.Warnf(ctx, "GitHub document sync failed: %v", cause)
	return s.failSyncRun(ctx, ds, log, nil, "GitHub document sync failed; see source sync details", wasPaused, cause, true)
}

func (s *DataSourceService) enqueueGitHubDocumentContinuation(
	ctx context.Context, run *types.GitHubDocumentRun, payload types.DataSourceSyncPayload,
) error {
	if s.taskEnqueuer == nil {
		return errors.New("GitHub document continuation queue is unavailable")
	}
	payload.GitHubDocumentChunk++
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	task := asynq.NewTask(types.TypeDataSourceSync, data)
	id := fmt.Sprintf("github-document:%s:%d", run.ID, payload.GitHubDocumentChunk)
	_, err = s.taskEnqueuer.Enqueue(task,
		asynq.Queue(types.QueueSync), asynq.MaxRetry(5), asynq.Timeout(types.DataSourceSyncTaskTimeout),
		asynq.TaskID(id), asynq.ProcessIn(10*time.Second))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil // a retry of the previous chunk already queued this ordinal
	}
	return err
}

func (s *DataSourceService) processGitHubDocumentChunk(
	ctx context.Context, connector *githubConnector.Connector, store githubDocumentProgressStore,
	ds *types.DataSource, cfg *types.DataSourceConfig, syncLog *types.SyncLog,
	payload types.DataSourceSyncPayload, wasPaused bool, runGuard *syncRunGuard, accessGuard *syncAccessGuard,
	run *types.GitHubDocumentRun, leaseID string, plan *githubConnector.DocumentPlan,
	items []types.GitHubDocumentSyncItem, tagIDs []string,
) error {
	planned := make(map[string]githubConnector.DocumentPlanItem, len(plan.Upserts)+len(plan.Deletions))
	for _, item := range plan.Upserts {
		planned[githubDocumentPathHash(item.Path)] = item
	}
	for _, item := range plan.Deletions {
		planned[githubDocumentPathHash(item.Path)] = item
	}
	// Upserts always precede deletions. Within each operation, unprocessed
	// files precede prior failures so one bad parser cannot starve the rest.
	sort.Slice(items, func(i, j int) bool {
		if items[i].Operation != items[j].Operation {
			return items[i].Operation == types.GitHubDocumentItemUpsert
		}
		if items[i].Status != items[j].Status {
			return items[i].Status == types.GitHubDocumentItemPending
		}
		return items[i].Path < items[j].Path
	})
	processed, bytesRead := 0, int64(0)
	for index := range items {
		item := &items[index]
		if item.Status == types.GitHubDocumentItemReady {
			continue
		}
		if item.Operation == types.GitHubDocumentItemDelete {
			allUpsertsReady := true
			for _, earlier := range items {
				if earlier.Operation == types.GitHubDocumentItemUpsert && earlier.Status != types.GitHubDocumentItemReady {
					allUpsertsReady = false
					break
				}
			}
			if !allUpsertsReady {
				break
			}
		}
		if processed >= githubDocumentChunkItems ||
			(item.Operation == types.GitHubDocumentItemUpsert && processed > 0 && bytesRead+planned[item.PathHash].Size > githubDocumentChunkBytes) {
			break
		}
		if err := ensureSyncRunActive(ctx, runGuard); err != nil {
			return s.finishSyncRunGuardError(ctx, ds, syncLog, githubDocumentProgressResult(items), wasPaused, err)
		}
		if err := accessGuard.check(ctx); err != nil {
			return s.stopSyncAfterAccessChange(ctx, ds, syncLog, githubDocumentProgressResult(items), wasPaused, err)
		}
		var outcome, errorCode string
		if item.Operation == types.GitHubDocumentItemUpsert {
			plannedItem := planned[item.PathHash]
			fetched, readErr := connector.ReadPlannedDocument(ctx, plan, plannedItem)
			if readErr != nil {
				errorCode = "github_blob_read_failed"
				var gitErr *githubConnector.Error
				if errors.As(readErr, &gitErr) {
					errorCode = gitErr.Code
				}
			} else {
				updated, ingestErr := s.ingestPreparedFile(withKBActivitySuppressed(ctx), ds, &fetched, tagIDs)
				switch {
				case ingestErr == nil && updated:
					outcome = types.GitHubDocumentOutcomeUpdated
				case ingestErr == nil:
					outcome = types.GitHubDocumentOutcomeCreated
				default:
					var duplicate *types.DuplicateKnowledgeError
					if errors.As(ingestErr, &duplicate) {
						outcome = types.GitHubDocumentOutcomeSkipped
					} else if errors.Is(ingestErr, ErrInvalidFileType) {
						errorCode = "unsupported_file_type"
					} else if errors.Is(ingestErr, errPreparedFileOwnedByAnotherSource) {
						errorCode = "duplicate_other_source"
					} else {
						errorCode = "ingest_failed"
					}
				}
			}
			bytesRead += plannedItem.Size
		} else {
			before := &types.SyncResult{}
			deleted := plan.DeletedItem(planned[item.PathHash])
			s.applyFetchedItem(withKBActivitySuppressed(ctx), ds, &deleted, tagIDs, before)
			if before.Failed > 0 {
				errorCode = "deletion_failed"
			} else if before.Deleted > 0 {
				outcome = types.GitHubDocumentOutcomeDeleted
			} else {
				outcome = types.GitHubDocumentOutcomeSkipped
			}
		}
		status := types.GitHubDocumentItemReady
		if errorCode != "" {
			status = types.GitHubDocumentItemFailed
		}
		if err := store.UpdateGitHubDocumentItem(ctx, run, leaseID, *item, status, outcome, errorCode); err != nil {
			return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
		}
		item.Status, item.Outcome, item.ErrorCode = status, outcome, errorCode
		processed++
		if err := s.checkpointGitHubDocumentProgress(ctx, syncLog, items); err != nil {
			return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
		}
	}
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, githubDocumentProgressResult(items), wasPaused, err)
	}
	if err := accessGuard.check(ctx); err != nil {
		return s.stopSyncAfterAccessChange(ctx, ds, syncLog, githubDocumentProgressResult(items), wasPaused, err)
	}
	pending, failed, pendingUpserts, failedUpserts := 0, 0, 0, 0
	for _, item := range items {
		if item.Status == types.GitHubDocumentItemPending {
			pending++
			if item.Operation == types.GitHubDocumentItemUpsert {
				pendingUpserts++
			}
		} else if item.Status == types.GitHubDocumentItemFailed {
			failed++
			if item.Operation == types.GitHubDocumentItemUpsert {
				failedUpserts++
			}
		}
	}
	// A failed replacement blocks destructive deletion. Leave those rows
	// pending for the next scheduled/manual retry instead of queueing an
	// endless continuation solely because deletions cannot yet be authorized.
	if pendingUpserts == 0 && failedUpserts > 0 {
		result := githubDocumentProgressResult(items)
		data, _ := result.ToJSON()
		return s.updateSyncRunResult(ctx, ds, syncLog, result, data, types.SyncLogStatusPartial,
			fmt.Sprintf("%d GitHub document(s) still need attention; deletion is deferred", failedUpserts), wasPaused, false)
	}
	if pending > 0 {
		if processed == 0 {
			return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, errors.New("GitHub document checkpoint made no progress"))
		}
		if err := s.enqueueGitHubDocumentContinuation(ctx, run, payload); err != nil {
			return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
		}
		return nil // same running SyncLog continues in the queued bounded chunk
	}
	result := githubDocumentProgressResult(items)
	data, _ := result.ToJSON()
	if failed > 0 {
		return s.updateSyncRunResult(ctx, ds, syncLog, result, data, types.SyncLogStatusPartial,
			fmt.Sprintf("%d GitHub document(s) still need attention", failed), wasPaused, false)
	}
	if err := store.PublishGitHubDocumentRun(ctx, run, leaseID, ds); err != nil {
		return s.githubDocumentRunFailure(ctx, ds, syncLog, wasPaused, err)
	}
	return s.updateSyncRunResult(ctx, ds, syncLog, result, data, types.SyncLogStatusSuccess, "", wasPaused, false)
}
