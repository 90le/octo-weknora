package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/datasource"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const (
	restartRecoveryPreviewTokenTTL = 10 * time.Minute
	restartRecoveryLeaseDuration   = 5 * time.Minute
	restartRecoveryCandidateWait   = 30 * time.Minute
	restartRecoveryPollInterval    = 2 * time.Second
	restartRecoveryPlanVersion     = 1
)

var errRestartRecoveryLeaseLost = errors.New("restart recovery lease was lost")

// dataSourceRestartRecoveryStore is deliberately an optional extension of the
// established source repository contract. It lets existing alternate data
// source implementations remain usable while the server's SQL repository
// provides the durable recovery state machine.
type dataSourceRestartRecoveryStore interface {
	TryAcquireRestartRecoveryLease(context.Context, string, string, time.Time) (bool, error)
	RenewRestartRecoveryLease(context.Context, string, string, time.Time) (bool, error)
	ReleaseRestartRecoveryLease(context.Context, string, string) error
	CreateRestartRecoveryRun(context.Context, *types.DataSourceRestartRecoveryRun) error
	FindRestartRecoveryRun(context.Context, string) (*types.DataSourceRestartRecoveryRun, error)
	ClaimRestartRecoveryRun(context.Context, string, string) (bool, error)
	BlockRestartRecoveryRunBeforeLease(context.Context, string, string) (bool, error)
	UpdateRestartRecoveryRunIfLease(context.Context, *types.DataSourceRestartRecoveryRun, string) (bool, error)
}

// dataSourceKnowledgeLister is defined in datasource_delete.go. We use the
// same exact datasource_id + tenant + KB ownership fence for recovery, then
// narrow further to restart-marked prepared candidates.

type dataSourceRestartRecoveryPreviewToken struct {
	Version          int    `json:"v"`
	DataSourceID     string `json:"d"`
	TenantID         uint64 `json:"t"`
	KnowledgeBaseID  string `json:"k"`
	PrincipalSubject string `json:"a"`
	PlanDigest       string `json:"h"`
	ExpiresAtUnix    int64  `json:"e"`
}

func (s *DataSourceService) restartRecoveryStore() (dataSourceRestartRecoveryStore, error) {
	store, ok := s.dsRepo.(dataSourceRestartRecoveryStore)
	if !ok || store == nil {
		return nil, apperrors.NewServiceUnavailableError("restart recovery storage is unavailable")
	}
	return store, nil
}

// PreviewRestartInterruptedRecovery is strictly read-only. It neither changes
// candidate parse state nor creates a sync log, task, lease, or recovery run.
func (s *DataSourceService) PreviewRestartInterruptedRecovery(
	ctx context.Context,
	dsID string,
) (*types.DataSourceRestartRecoveryPreview, error) {
	ds, kb, plan, err := s.buildRestartRecoveryPlan(ctx, dsID)
	if err != nil {
		return nil, err
	}
	digest := restartRecoveryPlanDigest(ds, kb, plan)
	preview := &types.DataSourceRestartRecoveryPreview{
		DataSourceID:    ds.ID,
		KnowledgeBaseID: ds.KnowledgeBaseID,
		Candidates:      append([]types.DataSourceRestartRecoveryCandidate(nil), plan.PreviewCandidates...),
		Blockers:        append([]string(nil), plan.Blockers...),
		PlanDigest:      digest,
	}
	for _, candidate := range plan.PreviewCandidates {
		switch candidate.State {
		case types.DataSourceRestartRecoveryCandidatePending:
			preview.EligibleCount++
		case types.DataSourceRestartRecoveryCandidateBlocked:
			preview.BlockedCount++
		default:
			preview.ExcludedCount++
		}
	}
	if preview.EligibleCount == 0 || len(plan.Blockers) > 0 {
		return preview, nil
	}
	expiresAt := time.Now().UTC().Add(restartRecoveryPreviewTokenTTL)
	token, err := signRestartRecoveryPreview(dataSourceRestartRecoveryPreviewToken{
		Version:          restartRecoveryPlanVersion,
		DataSourceID:     ds.ID,
		TenantID:         ds.TenantID,
		KnowledgeBaseID:  ds.KnowledgeBaseID,
		PrincipalSubject: dataSourceDeletePrincipalSubject(ctx),
		PlanDigest:       digest,
		ExpiresAtUnix:    expiresAt.Unix(),
	})
	if err != nil {
		return nil, err
	}
	preview.PreviewToken = token
	preview.ExpiresAt = expiresAt.Format(time.RFC3339)
	return preview, nil
}

// StartRestartInterruptedRecovery validates the signed preview against a fresh
// database-only plan, then persists a run before enqueueing its worker. The
// request carries no knowledge IDs or path/URL input, so it cannot broaden a
// reviewed plan into an arbitrary reparse operation.
func (s *DataSourceService) StartRestartInterruptedRecovery(
	ctx context.Context,
	dsID string,
	req *types.DataSourceRestartRecoveryRequest,
) (*types.DataSourceRestartRecoveryRun, error) {
	if req == nil || strings.TrimSpace(req.PreviewToken) == "" {
		return nil, apperrors.NewBadRequestError("preview_token is required for restart recovery")
	}
	store, err := s.restartRecoveryStore()
	if err != nil {
		return nil, err
	}
	if s.taskEnqueuer == nil {
		return nil, apperrors.NewServiceUnavailableError("restart recovery task executor is unavailable")
	}
	token, err := verifyRestartRecoveryPreview(req.PreviewToken)
	if err != nil {
		return nil, apperrors.NewConflictError("restart recovery preview is invalid or expired; request a new preview")
	}
	ds, kb, plan, err := s.buildRestartRecoveryPlan(ctx, dsID)
	if err != nil {
		return nil, err
	}
	digest := restartRecoveryPlanDigest(ds, kb, plan)
	if token.Version != restartRecoveryPlanVersion || token.DataSourceID != ds.ID ||
		token.TenantID != ds.TenantID || token.KnowledgeBaseID != ds.KnowledgeBaseID ||
		token.PrincipalSubject != dataSourceDeletePrincipalSubject(ctx) || token.PlanDigest != digest {
		return nil, apperrors.NewConflictError("restart recovery candidates changed; request a new preview")
	}
	if len(plan.Blockers) > 0 {
		return nil, apperrors.NewConflictError("restart recovery is blocked by current knowledge-base processing configuration")
	}
	if countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidatePending) == 0 {
		return nil, apperrors.NewConflictError("no eligible restart-interrupted local file candidates remain")
	}
	if running, e := s.syncLogRepo.HasRunningSync(ctx, ds.ID); e != nil {
		return nil, e
	} else if running {
		return nil, apperrors.NewConflictError("data source sync is running; wait for it to finish before restart recovery")
	}
	planBlob, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("encode restart recovery plan: %w", err)
	}
	run := &types.DataSourceRestartRecoveryRun{
		ID:              uuid.NewString(),
		TenantID:        ds.TenantID,
		DataSourceID:    ds.ID,
		KnowledgeBaseID: ds.KnowledgeBaseID,
		PlanDigest:      digest,
		Plan:            types.JSON(planBlob),
		Status:          types.DataSourceRestartRecoveryStatusPending,
	}
	if err := store.CreateRestartRecoveryRun(ctx, run); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(types.DataSourceRestartRecoveryPayload{
		TenantID: ds.TenantID, DataSourceID: ds.ID, RunID: run.ID,
	})
	if err != nil {
		return nil, err
	}
	task := asynq.NewTask(types.TypeDataSourceRestartRecovery, payload,
		asynq.Queue(types.QueueMaintenance), asynq.MaxRetry(3), asynq.Timeout(4*time.Hour),
		asynq.TaskID("datasource-restart-recovery-"+run.ID))
	if _, err := s.taskEnqueuer.Enqueue(task); err != nil {
		// Lite's SyncTaskExecutor is intentionally process-local, while the run
		// row is durable. If this enqueue fails (or the process dies after it
		// succeeds), startup re-arms the approved pending run after handlers are
		// registered. Surface the durable run ID instead of silently pretending
		// the request was never accepted.
		return nil, apperrors.NewServiceUnavailableError(fmt.Sprintf("restart recovery plan %s was saved but its worker could not be queued", run.ID))
	}
	return run, nil
}

func (s *DataSourceService) GetRestartInterruptedRecoveryRun(
	ctx context.Context,
	runID string,
) (*types.DataSourceRestartRecoveryRun, error) {
	store, err := s.restartRecoveryStore()
	if err != nil {
		return nil, err
	}
	run, err := store.FindRestartRecoveryRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, datasource.ErrDataSourceNotFound
	}
	if tenantID, ok := types.TenantIDFromContext(ctx); ok && tenantID != 0 && run.TenantID != tenantID {
		return nil, datasource.ErrDataSourceNotFound
	}
	return run, nil
}

// ProcessRestartInterruptedRecovery runs entirely from the persisted plan and
// stored FilePath. It never calls a connector, GitHub API, downloader, or URL
// parser; ReparseKnowledge reads the already-owned file through the KB storage
// service. A canonical target appearing at publish time blocks that candidate
// without deleting either record.
func (s *DataSourceService) ProcessRestartInterruptedRecovery(ctx context.Context, task *asynq.Task) error {
	var payload types.DataSourceRestartRecoveryPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("decode restart recovery payload: %w", err)
	}
	if payload.RunID == "" || payload.DataSourceID == "" || payload.TenantID == 0 {
		return fmt.Errorf("%w: invalid restart recovery payload", asynq.SkipRetry)
	}
	store, err := s.restartRecoveryStore()
	if err != nil {
		return err
	}
	run, err := store.FindRestartRecoveryRun(ctx, payload.RunID)
	if err != nil || run == nil {
		return fmt.Errorf("%w: restart recovery run unavailable", asynq.SkipRetry)
	}
	if run.Status == types.DataSourceRestartRecoveryStatusCompleted ||
		run.Status == types.DataSourceRestartRecoveryStatusPartial ||
		run.Status == types.DataSourceRestartRecoveryStatusFailed ||
		run.Status == types.DataSourceRestartRecoveryStatusBlocked {
		return nil
	}
	if run.TenantID != payload.TenantID || run.DataSourceID != payload.DataSourceID {
		return fmt.Errorf("%w: restart recovery run scope mismatch", asynq.SkipRetry)
	}
	ds, err := s.dsRepo.FindByID(ctx, run.DataSourceID)
	if err != nil || ds == nil || ds.TenantID != run.TenantID || ds.KnowledgeBaseID != run.KnowledgeBaseID {
		_, blockErr := store.BlockRestartRecoveryRunBeforeLease(ctx, run.ID, "data source is missing or its ownership changed")
		return blockErr
	}
	if running, e := s.syncLogRepo.HasRunningSync(ctx, ds.ID); e != nil {
		return e
	} else if running {
		return fmt.Errorf("%w: data source sync is running", datasource.ErrSyncAlreadyRunning)
	}
	leaseID := run.ID
	acquired, err := store.TryAcquireRestartRecoveryLease(ctx, ds.ID, leaseID, time.Now().UTC().Add(restartRecoveryLeaseDuration))
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("%w: restart recovery lease is held", datasource.ErrRestartRecoveryInProgress)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if releaseErr := store.ReleaseRestartRecoveryLease(releaseCtx, ds.ID, leaseID); releaseErr != nil {
			logger.Warnf(releaseCtx, "failed to release restart recovery lease for %s: %v", ds.ID, releaseErr)
		}
	}()
	claimed, err := store.ClaimRestartRecoveryRun(ctx, run.ID, leaseID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	run, err = store.FindRestartRecoveryRun(ctx, run.ID)
	if err != nil {
		return err
	}
	plan, err := decodeRestartRecoveryPlan(run.Plan)
	if err != nil {
		return s.finishRestartRecovery(ctx, store, run, leaseID, types.DataSourceRestartRecoveryStatusFailed,
			"persisted restart recovery plan is invalid")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, ds.KnowledgeBaseID)
	if err != nil || kb == nil {
		return s.finishRestartRecovery(ctx, store, run, leaseID, types.DataSourceRestartRecoveryStatusBlocked,
			"knowledge base is unavailable")
	}
	if plan.DataSourceFingerprint != restartRecoveryDataSourceFingerprint(ds) ||
		plan.KnowledgeBaseFingerprint != restartRecoveryKnowledgeBaseFingerprint(kb) {
		return s.finishRestartRecovery(ctx, store, run, leaseID, types.DataSourceRestartRecoveryStatusBlocked,
			"data source or knowledge-base configuration changed; request a new preview")
	}
	if len(plan.Blockers) > 0 {
		return s.finishRestartRecovery(ctx, store, run, leaseID, types.DataSourceRestartRecoveryStatusBlocked,
			"recovery plan has processing blockers")
	}
	workerCtx, err := s.restartRecoveryWorkerContext(ctx, ds, kb)
	if err != nil {
		return s.finishRestartRecovery(ctx, store, run, leaseID, types.DataSourceRestartRecoveryStatusFailed, err.Error())
	}
	for index := range plan.Candidates {
		candidate := &plan.Candidates[index]
		if candidate.State != types.DataSourceRestartRecoveryCandidatePending && candidate.State != types.DataSourceRestartRecoveryCandidateReparsing {
			continue
		}
		if err := s.renewRestartRecoveryLease(workerCtx, store, ds.ID, leaseID); err != nil {
			return err
		}
		if err := s.processRestartRecoveryCandidate(workerCtx, store, run, ds, plan, index, leaseID); err != nil {
			return err
		}
	}
	return s.finishRestartRecovery(ctx, store, run, leaseID, restartRecoveryTerminalStatus(plan), restartRecoveryTerminalMessage(plan))
}

func (s *DataSourceService) restartRecoveryWorkerContext(ctx context.Context, ds *types.DataSource, kb *types.KnowledgeBase) (context.Context, error) {
	if s.tenantRepo == nil {
		return nil, errors.New("tenant service is unavailable")
	}
	tenant, err := s.tenantRepo.GetTenantByID(ctx, ds.TenantID)
	if err != nil {
		return nil, fmt.Errorf("load recovery tenant: %w", err)
	}
	ctx = types.WithExecutionTenant(ctx, ds.TenantID)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	ctx, err = access.WithKBTaskWrite(ctx, kb, ds.TenantID)
	if err != nil {
		return nil, err
	}
	return ctx, nil
}

func (s *DataSourceService) processRestartRecoveryCandidate(
	ctx context.Context,
	store dataSourceRestartRecoveryStore,
	run *types.DataSourceRestartRecoveryRun,
	ds *types.DataSource,
	plan *types.DataSourceRestartRecoveryPlan,
	index int,
	leaseID string,
) error {
	candidate := &plan.Candidates[index]
	repo := s.knowledgeService.GetRepository()
	current, err := repo.GetKnowledgeByID(ctx, ds.TenantID, candidate.KnowledgeID)
	if err != nil || current == nil {
		candidate.State, candidate.Reason, candidate.CompletedAt = types.DataSourceRestartRecoveryCandidateFailed, "candidate is no longer available", time.Now().UTC().Format(time.RFC3339)
		return s.persistRestartRecoveryPlan(ctx, store, run, plan, leaseID)
	}
	if candidate.State == types.DataSourceRestartRecoveryCandidatePending {
		if !matchesRestartRecoveryCandidate(current, ds, *candidate) {
			candidate.State, candidate.Reason, candidate.CompletedAt = types.DataSourceRestartRecoveryCandidateBlocked, "candidate changed after preview", time.Now().UTC().Format(time.RFC3339)
			return s.persistRestartRecoveryPlan(ctx, store, run, plan, leaseID)
		}
		candidate.State = types.DataSourceRestartRecoveryCandidateReparsing
		candidate.AttemptedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.persistRestartRecoveryPlan(ctx, store, run, plan, leaseID); err != nil {
			return err
		}
		if _, err := s.knowledgeService.ReparseKnowledge(ctx, current.ID, nil); err != nil {
			candidate.State, candidate.Reason, candidate.CompletedAt = types.DataSourceRestartRecoveryCandidateFailed, sanitizeRestartRecoveryError(err), time.Now().UTC().Format(time.RFC3339)
			return s.persistRestartRecoveryPlan(ctx, store, run, plan, leaseID)
		}
	}
	ready, err := s.waitForRestartRecoveryCandidate(ctx, store, ds, candidate, leaseID)
	if err != nil {
		candidate.State, candidate.Reason, candidate.CompletedAt = types.DataSourceRestartRecoveryCandidateFailed, sanitizeRestartRecoveryError(err), time.Now().UTC().Format(time.RFC3339)
		return s.persistRestartRecoveryPlan(ctx, store, run, plan, leaseID)
	}
	outcome, err := s.finalizePreparedCandidate(ctx, ds, ready, nil, nil)
	if err != nil {
		candidate.State, candidate.Reason, candidate.CompletedAt = types.DataSourceRestartRecoveryCandidateFailed, sanitizeRestartRecoveryError(err), time.Now().UTC().Format(time.RFC3339)
	} else if outcome == preparedCandidateFinalizePublished {
		candidate.State, candidate.Reason, candidate.CompletedAt = types.DataSourceRestartRecoveryCandidatePublished, "", time.Now().UTC().Format(time.RFC3339)
	} else if outcome == preparedCandidateFinalizeBlocked {
		candidate.State, candidate.Reason, candidate.CompletedAt = types.DataSourceRestartRecoveryCandidateBlocked, "canonical target exists or candidate ownership changed", time.Now().UTC().Format(time.RFC3339)
	} else {
		candidate.State, candidate.Reason, candidate.CompletedAt = types.DataSourceRestartRecoveryCandidateFailed, "candidate was not ready for publication", time.Now().UTC().Format(time.RFC3339)
	}
	return s.persistRestartRecoveryPlan(ctx, store, run, plan, leaseID)
}

func (s *DataSourceService) waitForRestartRecoveryCandidate(
	ctx context.Context,
	store dataSourceRestartRecoveryStore,
	ds *types.DataSource,
	candidate *types.DataSourceRestartRecoveryCandidate,
	leaseID string,
) (*types.Knowledge, error) {
	deadline := time.NewTimer(restartRecoveryCandidateWait)
	defer deadline.Stop()
	ticker := time.NewTicker(restartRecoveryPollInterval)
	defer ticker.Stop()
	repo := s.knowledgeService.GetRepository()
	for {
		current, err := repo.GetKnowledgeByID(ctx, ds.TenantID, candidate.KnowledgeID)
		if err != nil || current == nil {
			return nil, errors.New("candidate disappeared while waiting for local reparse")
		}
		if indexedForSync(current) {
			return current, nil
		}
		switch current.ParseStatus {
		case types.ParseStatusFailed, types.ParseStatusCancelled, types.ParseStatusDeleting:
			if current.ErrorMessage != "" {
				return nil, fmt.Errorf("local reparse ended in %s: %s", current.ParseStatus, current.ErrorMessage)
			}
			return nil, fmt.Errorf("local reparse ended in %s", current.ParseStatus)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("timed out waiting for local reparse")
		case <-ticker.C:
			if err := s.renewRestartRecoveryLease(ctx, store, ds.ID, leaseID); err != nil {
				return nil, err
			}
		}
	}
}

func (s *DataSourceService) renewRestartRecoveryLease(ctx context.Context, store dataSourceRestartRecoveryStore, dsID, leaseID string) error {
	renewed, err := store.RenewRestartRecoveryLease(ctx, dsID, leaseID, time.Now().UTC().Add(restartRecoveryLeaseDuration))
	if err != nil {
		return err
	}
	if !renewed {
		return errRestartRecoveryLeaseLost
	}
	return nil
}

func (s *DataSourceService) persistRestartRecoveryPlan(
	ctx context.Context,
	store dataSourceRestartRecoveryStore,
	run *types.DataSourceRestartRecoveryRun,
	plan *types.DataSourceRestartRecoveryPlan,
	leaseID string,
) error {
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	run.Plan = types.JSON(body)
	applied, err := store.UpdateRestartRecoveryRunIfLease(ctx, run, leaseID)
	if err != nil {
		return err
	}
	if !applied {
		return errRestartRecoveryLeaseLost
	}
	return nil
}

func (s *DataSourceService) finishRestartRecovery(
	ctx context.Context,
	store dataSourceRestartRecoveryStore,
	run *types.DataSourceRestartRecoveryRun,
	leaseID, status, message string,
) error {
	run.Status = status
	run.ErrorMessage = message
	now := time.Now().UTC()
	run.FinishedAt = &now
	applied, err := store.UpdateRestartRecoveryRunIfLease(ctx, run, leaseID)
	if err != nil {
		return err
	}
	if !applied {
		return errRestartRecoveryLeaseLost
	}
	return nil
}

func (s *DataSourceService) buildRestartRecoveryPlan(ctx context.Context, dsID string) (*types.DataSource, *types.KnowledgeBase, *types.DataSourceRestartRecoveryPlan, error) {
	if strings.TrimSpace(dsID) == "" {
		return nil, nil, nil, apperrors.NewBadRequestError("data source id is required")
	}
	ds, items, _, err := s.dataSourceGeneratedContent(ctx, dsID)
	if err != nil {
		return nil, nil, nil, err
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, ds.KnowledgeBaseID)
	if err != nil || kb == nil || kb.TenantID != ds.TenantID {
		return nil, nil, nil, datasource.ErrKnowledgeBaseNotFound
	}
	plan := &types.DataSourceRestartRecoveryPlan{
		Version:                  restartRecoveryPlanVersion,
		DataSourceFingerprint:    restartRecoveryDataSourceFingerprint(ds),
		KnowledgeBaseFingerprint: restartRecoveryKnowledgeBaseFingerprint(kb),
		Candidates:               make([]types.DataSourceRestartRecoveryCandidate, 0),
		PreviewCandidates:        make([]types.DataSourceRestartRecoveryCandidate, 0),
	}
	if ds.Type != types.ConnectorTypeGitHub {
		plan.Blockers = append(plan.Blockers, "only GitHub document sources support restart recovery")
	}
	if kb.Type != types.KnowledgeBaseTypeDocument {
		plan.Blockers = append(plan.Blockers, "restart recovery requires a document knowledge base")
	}
	if kb.IndexingStrategy.GraphEnabled && !kb.IsGraphEnabled() {
		plan.Blockers = append(plan.Blockers, "knowledge graph is enabled but extract configuration cannot rebuild it")
	}
	// The lister is exact-source scoped. Sort its in-memory results to keep the
	// plan digest deterministic across database engines and query plans.
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	for _, item := range items {
		if item == nil {
			continue
		}
		metadata := item.GetMetadata()
		// Ordinary published source documents are deliberately not part of this
		// plan. They cannot be reprocessed merely because a recovery preview was
		// opened; only staged or restart-marked rows are auditable candidates.
		if metadata["sync_target_external_id"] == "" && item.ErrorMessage != types.RestartInterruptedKnowledgeError {
			continue
		}
		candidate := restartRecoveryCandidateFromKnowledge(item, metadata)
		candidate.State = types.DataSourceRestartRecoveryCandidateExcluded
		switch {
		case item.DeletedAt.Valid:
			candidate.Reason = "candidate was deleted"
		case metadata["datasource_id"] != ds.ID:
			candidate.Reason = "candidate is not owned by this data source"
		case item.Channel != types.ConnectorTypeGitHub:
			candidate.Reason = "candidate is not a GitHub document"
		case item.Type != "file":
			candidate.Reason = "URL and non-file candidates are excluded by default"
		case strings.TrimSpace(item.FilePath) == "":
			candidate.Reason = "candidate has no stored file path"
		case metadata["sync_target_external_id"] == "":
			candidate.Reason = "candidate is not a staged prepared file"
		case !strings.Contains(metadata["external_id"], ":pending:"):
			candidate.Reason = "candidate external id is not a prepared version"
		case restartRecoverySourceVersion(metadata) == "":
			candidate.Reason = "candidate has no source version"
		case metadata["github_url"] == "" && item.Source == "":
			candidate.Reason = "candidate has no pinned GitHub source reference"
		case item.ParseStatus != types.ParseStatusFailed || item.ErrorMessage != types.RestartInterruptedKnowledgeError:
			candidate.Reason = "candidate was not failed by application restart"
		default:
			if err := validateDefaultFileImportRequirements(ctx, kb, ResolveProcessConfig(kb, nil), item.FileType); err != nil {
				candidate.State = types.DataSourceRestartRecoveryCandidateBlocked
				candidate.Reason = "current processing configuration cannot reparse this file: " + sanitizeRestartRecoveryError(err)
			} else if readable, readErr := s.restartRecoveryStoredFileReadable(ctx, ds, item.ID); readErr != nil || !readable {
				// Do not surface a storage provider error: those often contain an
				// absolute FilePath. The preview only needs to state the safety
				// result, and this candidate must never enter the signed plan.
				candidate.Reason = "stored file is not readable from current knowledge-base storage"
			} else if canonical, findErr := s.knowledgeService.GetRepository().FindByDataSourceExternalID(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID, candidate.TargetExternalID); findErr != nil {
				return nil, nil, nil, findErr
			} else if canonical != nil {
				candidate.State = types.DataSourceRestartRecoveryCandidateBlocked
				candidate.Reason = "canonical target already exists"
			} else if len(plan.Blockers) > 0 {
				candidate.State = types.DataSourceRestartRecoveryCandidateBlocked
				candidate.Reason = "knowledge base processing configuration is blocked"
			} else {
				candidate.State = types.DataSourceRestartRecoveryCandidatePending
			}
		}
		plan.PreviewCandidates = append(plan.PreviewCandidates, candidate)
		if candidate.State == types.DataSourceRestartRecoveryCandidatePending {
			plan.Candidates = append(plan.Candidates, candidate)
		}
	}
	return ds, kb, plan, nil
}

// restartRecoveryStoredFileReadable opens, reads one byte, and closes through
// KnowledgeService.GetKnowledgeFile. That helper resolves the same KB-aware
// FileServiceForPath chain used by Reparse/ProcessDocument, including resource
// catalog and historical provider fallback. No connector, GitHub, or URL
// fetcher is called here.
func (s *DataSourceService) restartRecoveryStoredFileReadable(
	ctx context.Context,
	ds *types.DataSource,
	knowledgeID string,
) (bool, error) {
	if s == nil || s.knowledgeService == nil || s.tenantRepo == nil || ds == nil || knowledgeID == "" {
		return false, errors.New("restart recovery file verification is unavailable")
	}
	tenant, err := s.tenantRepo.GetTenantByID(ctx, ds.TenantID)
	if err != nil || tenant == nil {
		return false, errors.New("restart recovery tenant storage context is unavailable")
	}
	readCtx := types.WithExecutionTenant(ctx, ds.TenantID)
	readCtx = context.WithValue(readCtx, types.TenantInfoContextKey, tenant)
	file, _, err := s.knowledgeService.GetKnowledgeFile(readCtx, knowledgeID)
	if err != nil || file == nil {
		return false, err
	}
	var probe [1]byte
	_, readErr := file.Read(probe[:])
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, readErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	return true, nil
}

func restartRecoveryCandidateFromKnowledge(item *types.Knowledge, metadata map[string]string) types.DataSourceRestartRecoveryCandidate {
	return types.DataSourceRestartRecoveryCandidate{
		KnowledgeID:      item.ID,
		Title:            item.Title,
		FileName:         item.FileName,
		SourceVersion:    restartRecoverySourceVersion(metadata),
		TargetExternalID: metadata["sync_target_external_id"],
		Fingerprint:      restartRecoveryCandidateFingerprint(item),
	}
}

func matchesRestartRecoveryCandidate(item *types.Knowledge, ds *types.DataSource, candidate types.DataSourceRestartRecoveryCandidate) bool {
	if item == nil || ds == nil || item.ID != candidate.KnowledgeID || item.TenantID != ds.TenantID || item.KnowledgeBaseID != ds.KnowledgeBaseID || item.DeletedAt.Valid {
		return false
	}
	metadata := item.GetMetadata()
	return item.Channel == types.ConnectorTypeGitHub && item.Type == "file" && item.FilePath != "" &&
		metadata["datasource_id"] == ds.ID && metadata["sync_target_external_id"] == candidate.TargetExternalID &&
		item.ParseStatus == types.ParseStatusFailed && item.ErrorMessage == types.RestartInterruptedKnowledgeError &&
		restartRecoveryCandidateFingerprint(item) == candidate.Fingerprint
}

func restartRecoverySourceVersion(metadata map[string]string) string {
	if metadata == nil {
		return ""
	}
	if value := metadata["source_version"]; value != "" {
		return value
	}
	return metadata["github_blob_sha"]
}

func restartRecoveryCandidateFingerprint(item *types.Knowledge) string {
	if item == nil {
		return ""
	}
	return restartRecoveryHash(strings.Join([]string{
		item.ID,
		item.KnowledgeBaseID,
		item.Channel,
		item.Type,
		item.FilePath,
		item.FileName,
		item.ParseStatus,
		item.ErrorMessage,
		item.UpdatedAt.UTC().Format(time.RFC3339Nano),
		string(item.Metadata),
		item.Source,
	}, "\x00"))
}

func restartRecoveryDataSourceFingerprint(ds *types.DataSource) string {
	if ds == nil {
		return ""
	}
	return restartRecoveryHash(strings.Join([]string{
		ds.ID, fmt.Sprintf("%d", ds.TenantID), ds.KnowledgeBaseID, ds.Type,
	}, "\x00"))
}

func restartRecoveryKnowledgeBaseFingerprint(kb *types.KnowledgeBase) string {
	if kb == nil {
		return ""
	}
	// This hash intentionally covers only configuration which changes the
	// local parser/indexer behaviour. It never serializes model credentials.
	return restartRecoveryHash(strings.Join([]string{
		kb.ID, fmt.Sprintf("%d", kb.TenantID), kb.Type, kb.EmbeddingModelID,
		fmt.Sprintf("%t", kb.IsVectorEnabled()), fmt.Sprintf("%t", kb.IsKeywordEnabled()),
		fmt.Sprintf("%t", kb.IndexingStrategy.GraphEnabled), fmt.Sprintf("%t", kb.IsGraphEnabled()),
		fmt.Sprintf("%t", kb.IsMultimodalEnabled()), fmt.Sprintf("%t", kb.ASRConfig.IsASREnabled()),
	}, "\x00"))
}

func restartRecoveryPlanDigest(ds *types.DataSource, kb *types.KnowledgeBase, plan *types.DataSourceRestartRecoveryPlan) string {
	if plan == nil {
		return ""
	}
	records := make([]string, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		records = append(records, strings.Join([]string{candidate.KnowledgeID, candidate.Fingerprint, candidate.State, candidate.Reason, candidate.TargetExternalID, candidate.SourceVersion}, "\x00"))
	}
	sort.Strings(records)
	blockers := append([]string(nil), plan.Blockers...)
	sort.Strings(blockers)
	parts := []string{
		fmt.Sprintf("v=%d", plan.Version), restartRecoveryDataSourceFingerprint(ds), restartRecoveryKnowledgeBaseFingerprint(kb),
	}
	parts = append(parts, records...)
	parts = append(parts, blockers...)
	return restartRecoveryHash(strings.Join(parts, "\n"))
}

func restartRecoveryHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func countRestartRecoveryCandidates(plan *types.DataSourceRestartRecoveryPlan, state string) int {
	if plan == nil {
		return 0
	}
	count := 0
	for _, candidate := range plan.Candidates {
		if candidate.State == state {
			count++
		}
	}
	return count
}

func decodeRestartRecoveryPlan(raw types.JSON) (*types.DataSourceRestartRecoveryPlan, error) {
	var plan types.DataSourceRestartRecoveryPlan
	if len(raw) == 0 || json.Unmarshal(raw, &plan) != nil || plan.Version != restartRecoveryPlanVersion {
		return nil, errors.New("invalid restart recovery plan")
	}
	return &plan, nil
}

func restartRecoveryTerminalStatus(plan *types.DataSourceRestartRecoveryPlan) string {
	published := countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidatePublished)
	failed := countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidateFailed)
	blocked := countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidateBlocked)
	pending := countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidatePending) + countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidateReparsing)
	if pending > 0 {
		return types.DataSourceRestartRecoveryStatusFailed
	}
	if failed == 0 && blocked == 0 && published > 0 {
		return types.DataSourceRestartRecoveryStatusCompleted
	}
	if published > 0 {
		return types.DataSourceRestartRecoveryStatusPartial
	}
	if blocked > 0 {
		return types.DataSourceRestartRecoveryStatusBlocked
	}
	return types.DataSourceRestartRecoveryStatusFailed
}

func restartRecoveryTerminalMessage(plan *types.DataSourceRestartRecoveryPlan) string {
	status := restartRecoveryTerminalStatus(plan)
	if status == types.DataSourceRestartRecoveryStatusCompleted {
		return ""
	}
	return fmt.Sprintf("published=%d blocked=%d failed=%d",
		countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidatePublished),
		countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidateBlocked),
		countRestartRecoveryCandidates(plan, types.DataSourceRestartRecoveryCandidateFailed))
}

func sanitizeRestartRecoveryError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func signRestartRecoveryPreview(payload dataSourceRestartRecoveryPreviewToken) (string, error) {
	key := secutils.SystemHMACKey()
	if len(key) == 0 {
		return "", apperrors.NewServiceUnavailableError("restart recovery preview signing is unavailable")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyRestartRecoveryPreview(rawToken string) (*dataSourceRestartRecoveryPreviewToken, error) {
	key := secutils.SystemHMACKey()
	if len(key) == 0 {
		return nil, apperrors.NewServiceUnavailableError("restart recovery preview signing is unavailable")
	}
	parts := strings.Split(strings.TrimSpace(rawToken), ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid restart recovery preview")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return nil, errors.New("restart recovery preview signature mismatch")
	}
	var token dataSourceRestartRecoveryPreviewToken
	if err := json.Unmarshal(payload, &token); err != nil {
		return nil, err
	}
	if token.ExpiresAtUnix == 0 || time.Now().UTC().After(time.Unix(token.ExpiresAtUnix, 0)) {
		return nil, errors.New("restart recovery preview expired")
	}
	return &token, nil
}
