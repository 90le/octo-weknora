package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/hibiken/asynq"
)

// DataSourceService implements the DataSourceService interface
type DataSourceService struct {
	dsRepo            interfaces.DataSourceRepository
	syncLogRepo       interfaces.SyncLogRepository
	knowledgeService  interfaces.KnowledgeService
	kbService         interfaces.KnowledgeBaseService
	taskEnqueuer      interfaces.TaskEnqueuer
	connectorRegistry *datasource.ConnectorRegistry
	scheduler         *datasource.Scheduler
	tenantRepo        interfaces.TenantRepository
	tagService        interfaces.KnowledgeTagService
	audit             interfaces.AuditLogService
	manualSyncMu      sync.Mutex
}

// syncRunAtomicFinalizer is intentionally optional so lightweight test and
// alternate repositories remain usable. The production SQL repository
// implements it with a single transaction for SyncLog + DataSource state.
type syncRunAtomicFinalizer interface {
	UpdateResultAndDataSourceIfRunning(context.Context, *types.SyncLog, *types.DataSource) (bool, error)
}

// NewDataSourceService creates a new data source service
func NewDataSourceService(
	dsRepo interfaces.DataSourceRepository,
	syncLogRepo interfaces.SyncLogRepository,
	knowledgeService interfaces.KnowledgeService,
	kbService interfaces.KnowledgeBaseService,
	taskEnqueuer interfaces.TaskEnqueuer,
	connectorRegistry *datasource.ConnectorRegistry,
	scheduler *datasource.Scheduler,
	tenantRepo interfaces.TenantRepository,
	tagService interfaces.KnowledgeTagService,
	audit interfaces.AuditLogService,
) interfaces.DataSourceService {
	return &DataSourceService{
		dsRepo:            dsRepo,
		syncLogRepo:       syncLogRepo,
		knowledgeService:  knowledgeService,
		kbService:         kbService,
		taskEnqueuer:      taskEnqueuer,
		connectorRegistry: connectorRegistry,
		scheduler:         scheduler,
		tenantRepo:        tenantRepo,
		tagService:        tagService,
		audit:             audit,
	}
}

// CreateDataSource creates a new data source configuration
func (s *DataSourceService) CreateDataSource(ctx context.Context, ds *types.DataSource) (*types.DataSource, error) {
	if ds == nil {
		return nil, datasource.ErrDataSourceInvalid
	}

	// Validate knowledge base exists
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, ds.KnowledgeBaseID)
	if err != nil || kb == nil {
		return nil, datasource.ErrKnowledgeBaseNotFound
	}
	if kb.TenantID != ds.TenantID {
		return nil, datasource.ErrKnowledgeBaseNotFound
	}

	// Validate connector type
	_, err = s.connectorRegistry.Get(ds.Type)
	if err != nil {
		return nil, err
	}

	// Validate configuration
	if cfg, err := ds.ParseConfig(); err == nil && cfg != nil {
		cfg.StripNonSecretCredentials(ds.Type)
		if blob, err := cfg.ToJSON(); err == nil {
			ds.Config = blob
		}
	}
	if err := s.validateDataSourceConfig(ctx, ds); err != nil {
		return nil, err
	}

	// Create in database
	if err := s.dsRepo.Create(ctx, ds); err != nil {
		logger.Errorf(ctx, "failed to create data source: %v", err)
		return nil, err
	}

	// Register cron schedule if configured
	if ds.SyncSchedule != "" && ds.Status == types.DataSourceStatusActive {
		if err := s.scheduler.AddOrUpdate(ds); err != nil {
			logger.Warnf(ctx, "failed to register cron for ds=%s: %v", ds.ID, err)
		}
	}

	logger.Infof(ctx, "data source created: id=%s type=%s kb=%s", ds.ID, ds.Type, ds.KnowledgeBaseID)
	recordKBActivity(ctx, s.audit, ds.TenantID, ds.KnowledgeBaseID, types.AuditActionDataSourceCreated,
		"data_source", ds.ID, types.AuditOutcomeSuccess,
		map[string]any{"name": ds.Name, "type": ds.Type})
	return ds, nil
}

// GetDataSource retrieves a data source by ID
func (s *DataSourceService) GetDataSource(ctx context.Context, id string) (*types.DataSource, error) {
	ds, err := s.dsRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return ds, nil
}

// ListDataSources lists all data sources for a knowledge base
func (s *DataSourceService) ListDataSources(ctx context.Context, kbID string) ([]*types.DataSource, error) {
	dataSources, err := s.dsRepo.FindByKnowledgeBase(ctx, kbID)
	if err != nil {
		logger.Errorf(ctx, "failed to list data sources: %v", err)
		return nil, err
	}

	// Attach latest sync log to each data source
	for _, ds := range dataSources {
		log, _ := s.syncLogRepo.FindLatest(ctx, ds.ID)
		if log != nil {
			ds.LatestSyncLog = log
		}
	}

	return dataSources, nil
}

// UpdateDataSource updates an existing data source
func (s *DataSourceService) UpdateDataSource(ctx context.Context, ds *types.DataSource) (*types.DataSource, error) {
	if ds == nil || ds.ID == "" {
		return nil, datasource.ErrDataSourceInvalid
	}

	// Verify data source exists
	existing, err := s.dsRepo.FindByID(ctx, ds.ID)
	if err != nil {
		return nil, err
	}

	if ds.KnowledgeBaseID == "" {
		ds.KnowledgeBaseID = existing.KnowledgeBaseID
	}
	if ds.KnowledgeBaseID != existing.KnowledgeBaseID {
		return nil, fmt.Errorf("changing knowledge base is not allowed")
	}

	if ds.TenantID == 0 {
		ds.TenantID = existing.TenantID
	}
	if ds.TenantID != existing.TenantID {
		return nil, datasource.ErrDataSourceInvalid
	}

	// Credentials NEVER flow through this endpoint — they live behind the
	// /credentials subresource. Force-preserve the stored credentials map
	// regardless of what the body says. Log a warning if a stale caller
	// passes one so we can spot them and migrate later. Non-credential
	// fields of Config (Type / ResourceIDs / Settings) flow through.
	var mergedCfg, existingParsedCfg *types.DataSourceConfig
	if len(ds.Config) > 0 {
		incomingCfg, parseIncErr := ds.ParseConfig()
		existingCfg, parseExErr := existing.ParseConfig()
		if parseIncErr == nil && parseExErr == nil && incomingCfg != nil {
			if incomingCfg.HasCredentials() {
				logger.Warnf(ctx,
					"deprecated: credentials in PUT /datasource/%s body are ignored; use PUT /credentials instead",
					secutils.SanitizeForLog(ds.ID))
			}
			merged := *incomingCfg
			if existingCfg != nil {
				merged.Credentials = existingCfg.Credentials
			} else {
				merged.Credentials = nil
			}
			merged.StripNonSecretCredentials(ds.Type)
			if blob, err := merged.ToJSON(); err == nil {
				ds.Config = blob
			}
			mergedCfg = &merged
			existingParsedCfg = existingCfg
		}
	}

	// Validate new configuration if non-credential fields changed. Skip
	// when there are no stored credentials yet (validators would fail with
	// no token to call the live API) and when the parsed config is
	// structurally identical.
	configActuallyChanged := true
	if mergedCfg != nil && existingParsedCfg != nil {
		configActuallyChanged = !reflect.DeepEqual(*mergedCfg, *existingParsedCfg)
	}
	hasCreds := mergedCfg != nil && mergedCfg.HasConfiguredCredentials(ds.Type)
	if (hasCreds || ds.Type == types.ConnectorTypeGitHub || ds.Type == localfolder.Type) && (ds.Type != existing.Type || configActuallyChanged) {
		if err := s.validateDataSourceConfig(ctx, ds); err != nil {
			return nil, err
		}
	}

	if err := s.dsRepo.Update(ctx, ds); err != nil {
		logger.Errorf(ctx, "failed to update data source: %v", err)
		return nil, err
	}

	// Update cron schedule
	if err := s.scheduler.AddOrUpdate(ds); err != nil {
		logger.Warnf(ctx, "failed to update cron for ds=%s: %v", ds.ID, err)
	}

	logger.Infof(ctx, "data source updated: id=%s", ds.ID)
	recordKBActivity(ctx, s.audit, ds.TenantID, ds.KnowledgeBaseID, types.AuditActionDataSourceUpdated,
		"data_source", ds.ID, types.AuditOutcomeSuccess,
		map[string]any{"name": ds.Name, "type": ds.Type, "changed_fields": []string{"settings"}})
	return ds, nil
}

// UpdateDataSourceCredentials replaces the connector credential map. This is
// a single atomic write; the previous credential set is discarded entirely
// (callers cannot patch individual keys because half-configured connector
// auth is meaningless). After persisting, the live connection is validated
// so the caller learns immediately if the new credentials are wrong.
func (s *DataSourceService) UpdateDataSourceCredentials(
	ctx context.Context, id string, credentials map[string]interface{},
) (*types.DataSource, error) {
	if id == "" {
		return nil, datasource.ErrDataSourceInvalid
	}
	existing, err := s.dsRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	parsed, err := existing.ParseConfig()
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		parsed = &types.DataSourceConfig{Type: existing.Type}
	}
	parsed.Credentials = credentials
	parsed.StripNonSecretCredentials(existing.Type)
	blob, err := parsed.ToJSON()
	if err != nil {
		return nil, err
	}
	existing.Config = blob

	// Run live validation now that the credentials are in place — surfaces
	// "wrong token" feedback immediately to the user instead of waiting for
	// the next scheduled sync.
	if err := s.validateDataSourceConfig(ctx, existing); err != nil {
		return nil, err
	}
	if err := s.dsRepo.Update(ctx, existing); err != nil {
		return nil, err
	}
	if existing.Type == types.ConnectorTypeGitHub && snapshot.IsSource(parsed) {
		if store, storeErr := snapshot.FromEnvironment(); storeErr == nil {
			if clearErr := store.ClearPrivateDirectory(existing, "git"); clearErr != nil {
				logger.Warnf(ctx, "failed to clear GitHub transport cache after credential update: %v", clearErr)
			}
		}
	}
	logger.Infof(ctx, "DataSource credentials updated: id=%s", secutils.SanitizeForLog(id))
	recordKBActivity(ctx, s.audit, existing.TenantID, existing.KnowledgeBaseID, types.AuditActionDataSourceUpdated,
		"data_source", existing.ID, types.AuditOutcomeSuccess,
		map[string]any{"name": existing.Name, "type": existing.Type, "changed_fields": []string{"credentials"}})
	return existing, nil
}

// ClearDataSourceCredentials wipes the connector credential map without
// touching any other config field. Idempotent.
func (s *DataSourceService) ClearDataSourceCredentials(ctx context.Context, id string) error {
	if id == "" {
		return datasource.ErrDataSourceInvalid
	}
	existing, err := s.dsRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	parsed, err := existing.ParseConfig()
	if err != nil {
		return err
	}
	if parsed == nil {
		return nil
	}
	parsed.StripNonSecretCredentials(existing.Type)
	if !parsed.HasConfiguredCredentials(existing.Type) {
		blob, err := parsed.ToJSON()
		if err != nil {
			return err
		}
		existing.Config = blob
		return s.dsRepo.Update(ctx, existing)
	}
	parsed.Credentials = nil
	blob, err := parsed.ToJSON()
	if err != nil {
		return err
	}
	existing.Config = blob
	if err := s.dsRepo.Update(ctx, existing); err != nil {
		return err
	}
	logger.Infof(ctx, "DataSource credentials cleared by user: id=%s", secutils.SanitizeForLog(id))
	recordKBActivity(ctx, s.audit, existing.TenantID, existing.KnowledgeBaseID, types.AuditActionDataSourceUpdated,
		"data_source", existing.ID, types.AuditOutcomeSuccess,
		map[string]any{"name": existing.Name, "type": existing.Type, "changed_fields": []string{"credentials"}})
	return nil
}

// DeleteDataSource deletes a data source (soft delete)
func (s *DataSourceService) DeleteDataSource(ctx context.Context, id string) error {
	// Verify data source exists
	existing, err := s.dsRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}

	if err := s.dsRepo.Delete(ctx, id); err != nil {
		logger.Errorf(ctx, "failed to delete data source: %v", err)
		return err
	}

	// Remove only generated source snapshots, never the input folder.
	removeSourceCache(existing)
	// Remove cron schedule
	s.scheduler.Remove(id)

	// Cancel any pending/running sync logs so queued asynq tasks won't retry
	if err := s.syncLogRepo.CancelPendingByDataSource(ctx, id); err != nil {
		logger.Warnf(ctx, "failed to cancel pending sync logs for ds=%s: %v", id, err)
	}

	logger.Infof(ctx, "data source deleted: id=%s", id)
	recordKBActivity(ctx, s.audit, existing.TenantID, existing.KnowledgeBaseID, types.AuditActionDataSourceDeleted,
		"data_source", existing.ID, types.AuditOutcomeSuccess,
		map[string]any{"name": existing.Name, "type": existing.Type})
	return nil
}

// ValidateConnection tests the connection to an external data source
func (s *DataSourceService) ValidateConnection(ctx context.Context, dsID string) error {
	ds, err := s.GetDataSource(ctx, dsID)
	if err != nil {
		return err
	}

	// Get connector
	connector, err := s.connectorRegistry.Get(ds.Type)
	if err != nil {
		return err
	}

	// Parse configuration
	config, err := ds.ParseConfig()
	if err != nil {
		return datasource.ErrInvalidConfig
	}

	// Validate connection
	if err := connector.Validate(ctx, config); err != nil {
		// Update data source with error
		ds.Status = types.DataSourceStatusError
		ds.ErrorMessage = err.Error()
		_ = s.dsRepo.Update(ctx, ds)
		return err
	}

	// Clear error if it was previously in error state
	if ds.Status == types.DataSourceStatusError {
		ds.Status = types.DataSourceStatusActive
		ds.ErrorMessage = ""
		_ = s.dsRepo.Update(ctx, ds)
	}

	return nil
}

// ListAvailableResources lists resources available for sync in the external system.
// parentID enables lazy (on-demand) loading of hierarchical resources: pass "" to
// list the top level, or a resource's ExternalID to list only its direct children.
func (s *DataSourceService) ListAvailableResources(
	ctx context.Context, dsID string, parentID string,
) ([]types.Resource, error) {
	ds, err := s.GetDataSource(ctx, dsID)
	if err != nil {
		return nil, err
	}

	// Get connector
	connector, err := s.connectorRegistry.Get(ds.Type)
	if err != nil {
		return nil, err
	}

	// Parse configuration
	config, err := ds.ParseConfig()
	if err != nil {
		return nil, datasource.ErrInvalidConfig
	}

	// List resources
	resources, err := connector.ListResources(ctx, config, parentID)
	if err != nil {
		logger.Errorf(ctx, "failed to list resources: %v", err)
		return nil, err
	}

	return resources, nil
}

// ResolveResourceAncestors resolves the ancestor ExternalIDs needed to reveal the
// given resources in a lazily-loaded picker (see the connector method for details).
func (s *DataSourceService) ResolveResourceAncestors(
	ctx context.Context, dsID string, resourceIDs []string,
) ([]string, error) {
	if len(resourceIDs) == 0 {
		return []string{}, nil
	}

	ds, err := s.GetDataSource(ctx, dsID)
	if err != nil {
		return nil, err
	}

	connector, err := s.connectorRegistry.Get(ds.Type)
	if err != nil {
		return nil, err
	}

	config, err := ds.ParseConfig()
	if err != nil {
		return nil, datasource.ErrInvalidConfig
	}

	ancestors, err := connector.ResolveResourceAncestors(ctx, config, resourceIDs)
	if err != nil {
		logger.Errorf(ctx, "failed to resolve resource ancestors: %v", err)
		return nil, err
	}

	return ancestors, nil
}

// ManualSync triggers an immediate sync for a data source
func (s *DataSourceService) ManualSync(ctx context.Context, dsID string) (*types.SyncLog, error) {
	// Creation of the log and its queue task is a short critical section. Once
	// the first caller has enqueued a sync, later clicks observe the running log
	// instead of creating duplicate GitHub fetches for the same source.
	s.manualSyncMu.Lock()
	defer s.manualSyncMu.Unlock()
	ds, err := s.GetDataSource(ctx, dsID)
	if err != nil {
		return nil, err
	}
	if ds.HasActiveRestartRecoveryLease(time.Now().UTC()) {
		return nil, datasource.ErrRestartRecoveryInProgress
	}

	if ds.Status != types.DataSourceStatusActive &&
		ds.Status != types.DataSourceStatusError &&
		ds.Status != types.DataSourceStatusPaused {
		return nil, datasource.ErrDataSourceNotActive
	}
	if running, err := s.syncLogRepo.HasRunningSync(ctx, dsID); err == nil && running {
		return nil, datasource.ErrSyncAlreadyRunning
	}

	// Create sync log
	syncLog := &types.SyncLog{
		DataSourceID: dsID,
		TenantID:     ds.TenantID,
		Status:       types.SyncLogStatusRunning,
		StartedAt:    time.Now().UTC(),
	}

	if err := s.syncLogRepo.Create(ctx, syncLog); err != nil {
		logger.Errorf(ctx, "failed to create sync log: %v", err)
		return nil, err
	}

	// Enqueue sync task
	payload := &types.DataSourceSyncPayload{
		DataSourceID: dsID,
		TenantID:     ds.TenantID,
		SyncLogID:    syncLog.ID,
		ForceFull:    false,
		Initiator:    types.TaskInitiatorFromContext(ctx),
		Trigger:      "manual",
	}
	langfuse.InjectTracing(ctx, payload)

	payloadJSON, _ := json.Marshal(payload)
	task := asynq.NewTask(types.TypeDataSourceSync, payloadJSON,
		asynq.Queue(types.QueueSync), asynq.MaxRetry(5), asynq.Timeout(types.DataSourceSyncTaskTimeout))

	info, err := s.taskEnqueuer.Enqueue(task)
	if err != nil {
		logger.Errorf(ctx, "failed to enqueue sync task: %v", err)
		syncLog.Status = types.SyncLogStatusFailed
		syncLog.FinishedAt = timePtr(time.Now().UTC())
		syncLog.ErrorMessage = err.Error()
		_ = s.syncLogRepo.Update(ctx, syncLog)
		if ds.Status != types.DataSourceStatusPaused {
			ds.Status = types.DataSourceStatusError
		}
		ds.ErrorMessage = fmt.Sprintf("Failed to enqueue sync: %v", err)
		_ = s.dsRepo.Update(ctx, ds)
		recordKBActivity(ctx, s.audit, ds.TenantID, ds.KnowledgeBaseID, types.AuditActionDataSourceSyncFailed,
			"data_source", ds.ID, types.AuditOutcomeFailed,
			map[string]any{"name": ds.Name, "type": ds.Type, "sync_log_id": syncLog.ID, "trigger": "manual"})
		return nil, err
	}

	logger.Infof(ctx, "sync task enqueued: ds=%s syncLog=%s", dsID, syncLog.ID)
	recordKBActivity(ctx, s.audit, ds.TenantID, ds.KnowledgeBaseID, types.AuditActionDataSourceSyncStarted,
		"data_source", ds.ID, types.AuditOutcomeAccepted,
		map[string]any{
			"name": ds.Name, "type": ds.Type, "sync_log_id": syncLog.ID,
			"task_id": info.ID, "trigger": "manual", "processing_status": "pending",
		})
	return syncLog, nil
}

// PauseDataSource pauses a data source's scheduled syncs
func (s *DataSourceService) PauseDataSource(ctx context.Context, id string) error {
	ds, err := s.GetDataSource(ctx, id)
	if err != nil {
		return err
	}

	ds.Status = types.DataSourceStatusPaused
	if err := s.dsRepo.Update(ctx, ds); err != nil {
		logger.Errorf(ctx, "failed to pause data source: %v", err)
		return err
	}

	// Remove cron schedule
	s.scheduler.Remove(id)

	logger.Infof(ctx, "data source paused: id=%s", id)
	recordKBActivity(ctx, s.audit, ds.TenantID, ds.KnowledgeBaseID, types.AuditActionDataSourcePaused,
		"data_source", ds.ID, types.AuditOutcomeSuccess, map[string]any{"name": ds.Name, "type": ds.Type})
	return nil
}

// ResumeDataSource resumes a paused data source
func (s *DataSourceService) ResumeDataSource(ctx context.Context, id string) error {
	ds, err := s.GetDataSource(ctx, id)
	if err != nil {
		return err
	}

	ds.Status = types.DataSourceStatusActive
	if err := s.dsRepo.Update(ctx, ds); err != nil {
		logger.Errorf(ctx, "failed to resume data source: %v", err)
		return err
	}

	// Re-register cron schedule
	if err := s.scheduler.AddOrUpdate(ds); err != nil {
		logger.Warnf(ctx, "failed to re-register cron for ds=%s: %v", ds.ID, err)
	}

	logger.Infof(ctx, "data source resumed: id=%s", id)
	recordKBActivity(ctx, s.audit, ds.TenantID, ds.KnowledgeBaseID, types.AuditActionDataSourceResumed,
		"data_source", ds.ID, types.AuditOutcomeSuccess, map[string]any{"name": ds.Name, "type": ds.Type})
	return nil
}

// GetSyncLogs retrieves sync history for a data source
func (s *DataSourceService) GetSyncLogs(ctx context.Context, dsID string, limit int, offset int) ([]*types.SyncLog, error) {
	logs, err := s.syncLogRepo.FindByDataSource(ctx, dsID, limit, offset)
	if err != nil {
		logger.Errorf(ctx, "failed to get sync logs: %v", err)
		return nil, err
	}
	return logs, nil
}

// GetSyncLog retrieves a specific sync log entry
func (s *DataSourceService) GetSyncLog(ctx context.Context, syncLogID string) (*types.SyncLog, error) {
	log, err := s.syncLogRepo.FindByID(ctx, syncLogID)
	if err != nil {
		return nil, err
	}
	return log, nil
}

// ProcessSync handles the actual sync operation (called by asynq task)
func (s *DataSourceService) ProcessSync(ctx context.Context, task *asynq.Task) error {
	var payload types.DataSourceSyncPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		logger.Errorf(ctx, "failed to unmarshal sync payload: %v", err)
		return err
	}
	ctx = payload.Initiator.Apply(ctx)
	taskID, _ := asynq.GetTaskID(ctx)
	ctx = withKBActivityTask(ctx, taskID, payload.Trigger)

	logger.Infof(ctx, "processing data source sync: ds=%s syncLog=%s", payload.DataSourceID, payload.SyncLogID)

	// Get data source
	ds, err := s.GetDataSource(ctx, payload.DataSourceID)
	if err != nil {
		logger.Warnf(ctx, "data source not found (likely deleted), cancelling sync: ds=%s err=%v", payload.DataSourceID, err)
		if syncLog, slErr := s.syncLogRepo.FindByID(ctx, payload.SyncLogID); slErr == nil && syncLog != nil {
			syncLog.Status = types.SyncLogStatusCanceled
			syncLog.FinishedAt = timePtr(time.Now().UTC())
			syncLog.ErrorMessage = "data source has been deleted"
			_, _ = s.syncLogRepo.UpdateResultIfRunning(ctx, syncLog)
		}
		return nil
	}
	wasPaused := ds.Status == types.DataSourceStatusPaused
	if ds.HasActiveRestartRecoveryLease(time.Now().UTC()) {
		// A queued sync may have been admitted in the short interval before
		// recovery acquired its database lease. It must terminate before any
		// connector fetch; the recovery plan owns the candidate metadata and
		// local reparse lifecycle for this source.
		if syncLog, slErr := s.syncLogRepo.FindByID(ctx, payload.SyncLogID); slErr == nil && syncLog != nil && syncLog.Status == types.SyncLogStatusRunning {
			syncLog.Status = types.SyncLogStatusCanceled
			syncLog.FinishedAt = timePtr(time.Now().UTC())
			syncLog.ErrorMessage = datasource.ErrRestartRecoveryInProgress.Error()
			_, _ = s.syncLogRepo.UpdateResultIfRunning(ctx, syncLog)
		}
		logger.Infof(ctx, "skipping data source sync while restart recovery owns source: ds=%s", payload.DataSourceID)
		return nil
	}

	// Get sync log
	syncLog, err := s.syncLogRepo.FindByID(ctx, payload.SyncLogID)
	if err != nil {
		logger.Errorf(ctx, "failed to get sync log: %v", err)
		return nil
	}
	if syncLog.Status != types.SyncLogStatusRunning {
		logger.Infof(ctx, "sync log is already terminal, skipping execution: ds=%s syncLog=%s status=%s",
			payload.DataSourceID, payload.SyncLogID, syncLog.Status)
		return nil
	}
	runGuard := &syncRunGuard{svc: s, syncLogID: syncLog.ID}
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, nil, wasPaused, err)
	}
	// A terminal sync-log transition (deletion, explicit cancellation, or
	// startup recovery) cancels connectors that honour context. Boundary
	// checks below still protect non-cooperative connectors before they write.
	runCtx, cancelRun := context.WithCancel(ctx)
	stopRunWatch := runGuard.watch(runCtx, cancelRun)
	defer func() {
		cancelRun()
		stopRunWatch()
	}()
	ctx = runCtx

	kb, kbErr := s.kbService.GetKnowledgeBaseByID(ctx, ds.KnowledgeBaseID)
	if kbErr != nil {
		logger.Warnf(ctx, "knowledge base not found (likely deleted), cancelling sync: kb=%s ds=%s err=%v",
			ds.KnowledgeBaseID, payload.DataSourceID, kbErr)
		syncLog.Status = types.SyncLogStatusCanceled
		syncLog.FinishedAt = timePtr(time.Now().UTC())
		syncLog.ErrorMessage = "knowledge base has been deleted"
		_, _ = s.syncLogRepo.UpdateResultIfRunning(ctx, syncLog)
		return nil
	}

	ctx, err = access.WithKBTaskWrite(ctx, kb, ds.TenantID)
	if err != nil {
		cause := fmt.Errorf("data source KB does not belong to its tenant: %w", err)
		return s.failSyncRun(ctx, ds, syncLog, nil, cause.Error(), wasPaused, cause, false)
	}

	// Get connector
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, nil, wasPaused, err)
	}
	connector, err := s.connectorRegistry.Get(ds.Type)
	if err != nil {
		logger.Errorf(ctx, "connector not found: type=%s", ds.Type)
		return s.failSyncRun(ctx, ds, syncLog, nil,
			fmt.Sprintf("Connector not found: %s", ds.Type), wasPaused, err, false)
	}

	// Parse configuration
	config, err := ds.ParseConfig()
	if err != nil {
		logger.Errorf(ctx, "failed to parse config: %v", err)
		return s.failSyncRun(ctx, ds, syncLog, nil,
			fmt.Sprintf("Invalid configuration: %v", err), wasPaused, err, false)
	}
	// Surface the KB's multimodal/VLM state to the connector so it only extracts
	// embedded images for OCR when the KB can actually ingest them (never persisted).
	config.MultimodalEnabled = kb.IsMultimodalEnabled()
	guard := &syncAccessGuard{svc: s, ds: ds, config: config, allowPaused: wasPaused && payload.Trigger == "manual"}
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, nil, wasPaused, err)
	}
	if err := guard.check(ctx); err != nil {
		return s.stopSyncAfterAccessChange(ctx, ds, syncLog, nil, wasPaused, err)
	}
	if snapshot.IsSource(config) {
		return s.processSourceSnapshot(ctx, ds, config, connector, syncLog, wasPaused, runGuard)
	}

	// Streaming path: connectors that support it interleave fetch→ingest→
	// checkpoint so a large sync bounds memory and resumes after a timeout
	// instead of restarting (Tencent/WeKnora#2136). Others fall back below.
	if sc, ok := connector.(datasource.StreamingConnector); ok {
		return s.processSyncStreaming(ctx, sc, ds, syncLog, config, payload, wasPaused, runGuard)
	}

	// Fetch items based on sync mode
	var items []types.FetchedItem
	var nextCursor *types.SyncCursor
	var fetchErr error

	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, nil, wasPaused, err)
	}
	if payload.ForceFull || ds.SyncMode == types.SyncModeFull {
		// Full sync
		items, fetchErr = connector.FetchAll(ctx, config, config.ResourceIDs)
		logger.Infof(ctx, "full sync fetched %d items", len(items))
	} else {
		// Incremental sync
		cursor, _ := ds.ParseSyncCursor()
		items, nextCursor, fetchErr = connector.FetchIncremental(ctx, config, cursor)
		logger.Infof(ctx, "incremental sync fetched %d items", len(items))
	}
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, nil, wasPaused, err)
	}
	if err := guard.check(ctx); err != nil {
		return s.stopSyncAfterAccessChange(ctx, ds, syncLog, nil, wasPaused, err)
	}

	var fetchWarnings []string
	var partialFetch *datasource.PartialFetchError
	if errors.As(fetchErr, &partialFetch) {
		fetchWarnings = partialFetch.Details
		fetchErr = nil
	}

	if fetchErr != nil {
		// Persist connector cursor even when fetch failed so transient outages
		// (e.g. RSS feed downtime) do not force a full re-ingest on recovery.
		if nextCursor != nil {
			if cursorJSON, cerr := nextCursor.ToJSON(); cerr == nil {
				ds.LastSyncCursor = cursorJSON
			}
		}
		logger.Errorf(ctx, "fetch operation failed: %v", fetchErr)
		return s.failSyncRun(ctx, ds, syncLog, nil,
			fmt.Sprintf("Fetch failed: %v", fetchErr), wasPaused, fetchErr, true)
	}

	// Process fetched items and write to knowledge base
	result := &types.SyncResult{
		Total: len(items),
	}

	// Set tenant context so KnowledgeService can resolve tenant info correctly
	ctx = context.WithValue(ctx, types.TenantIDContextKey, ds.TenantID)

	tenant, err := s.tenantRepo.GetTenantByID(ctx, ds.TenantID)
	if err != nil {
		logger.Errorf(ctx, "failed to get tenant info: %v", err)
		return s.failSyncRun(ctx, ds, syncLog, nil,
			fmt.Sprintf("Failed to get tenant info: %v", err), wasPaused, err, true)
	}
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)

	// Auto-tag: find or create a tag for this data source so synced items are easily identifiable
	autoTagIDs := s.resolveAutoTagIDs(ctx, ds)

	for index, item := range items {
		if err := runGuard.beforeItem(ctx, index); err != nil {
			if errors.Is(err, errSyncRunTerminated) {
				return stopSyncAfterRunTermination(err)
			}
			return s.finishSyncRunGuardError(ctx, ds, syncLog, result, wasPaused, err)
		}
		if err := guard.beforeItem(ctx, index); err != nil {
			return s.stopSyncAfterAccessChange(ctx, ds, syncLog, result, wasPaused, err)
		}
		item := item
		s.applyFetchedItem(withKBActivitySuppressed(ctx), ds, &item, autoTagIDs, result)
	}
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, result, wasPaused, err)
	}
	if err := guard.check(ctx); err != nil {
		return s.stopSyncAfterAccessChange(ctx, ds, syncLog, result, wasPaused, err)
	}

	resultJSON, _ := result.ToJSON()
	if err := allFetchedItemsFailedError(result); err != nil {
		logger.Errorf(ctx, "data source sync failed while processing fetched items: %v", err)
		if updateErr := s.updateSyncRunResult(ctx, ds, syncLog, result, resultJSON,
			types.SyncLogStatusFailed, err.Error(), wasPaused, true); updateErr != nil {
			return updateErr
		}
		return err
	}

	// A repository manifest is only acknowledged after all selected documents
	// are usable. Partial imports must be retried from the previous manifest.
	if nextCursor != nil && ((ds.Type != types.ConnectorTypeGitHub && ds.Type != localfolder.Type) || result.Failed == 0) {
		cursorJSON, _ := nextCursor.ToJSON()
		ds.LastSyncCursor = cursorJSON
	}

	ds.LastSyncAt = timePtr(time.Now().UTC())
	syncStatus := types.SyncLogStatusSuccess
	syncErrorMessage := ""
	if len(fetchWarnings) > 0 {
		syncStatus = types.SyncLogStatusPartial
		syncErrorMessage = fmt.Sprintf("Some feeds failed: %s", strings.Join(fetchWarnings, "; "))
		for _, w := range fetchWarnings {
			result.Errors = append(result.Errors, types.SyncItemError{Message: w})
		}
		resultJSON, _ = result.ToJSON()
	}
	if result.Failed > 0 {
		// Per-document failures flip the sync to partial so the drawer shows
		// which docs didn't make it (mirrors the streaming path). Deletion
		// failures additionally only retry on a later full sync.
		syncStatus = types.SyncLogStatusPartial
		if syncErrorMessage != "" {
			syncErrorMessage += "; "
		}
		syncErrorMessage += fmt.Sprintf("%d document(s) failed to sync", result.Failed)
		if result.DeletionFailed > 0 {
			syncErrorMessage += fmt.Sprintf(
				"; %d deletion failure(s) will only retry on the next full sync", result.DeletionFailed)
		}
	}
	if err := s.updateSyncRunResult(ctx, ds, syncLog, result, resultJSON,
		syncStatus, syncErrorMessage, wasPaused, false); err != nil {
		return err
	}

	logger.Infof(ctx, "data source sync completed: ds=%s created=%d updated=%d deleted=%d",
		payload.DataSourceID, syncLog.ItemsCreated, syncLog.ItemsUpdated, syncLog.ItemsDeleted)

	return nil
}

// resolveAutoTagIDs finds or creates the per-data-source tag applied to every
// synced item so results are identifiable in the KB. A tag failure is
// non-fatal: the sync proceeds untagged.
func (s *DataSourceService) resolveAutoTagIDs(ctx context.Context, ds *types.DataSource) []string {
	autoTagIDs := []string{}
	if autoTag, tagErr := s.tagService.FindOrCreateTagByName(ctx, ds.KnowledgeBaseID, ds.Name); tagErr != nil {
		logger.Warnf(ctx, "failed to find/create auto-tag %q: %v (proceeding without tag)", ds.Name, tagErr)
	} else if autoTag != nil {
		autoTagIDs = append(autoTagIDs, autoTag.ID)
		logger.Infof(ctx, "using auto-tag %q (id=%s) for data source sync", ds.Name, autoTag.ID)
	}
	return autoTagIDs
}

// maxSyncResultErrors bounds the per-item error sample retained in
// SyncResult.Errors. That slice is persisted as jsonb and returned in every
// sync-log list response, so an unbounded list on a sync that fails thousands of
// documents means multi-MB DB rows and payloads. The accurate failure count
// lives in SyncResult.Failed (a bounded int); this list only keeps a sample for
// display (Tencent/WeKnora#2136 / #1262).
const maxSyncResultErrors = 100

// recordSyncError appends an error sample to result.Errors, capped at
// maxSyncResultErrors. Callers still increment result.Failed for the exact count.
func recordSyncError(result *types.SyncResult, item types.SyncItemError) {
	if len(result.Errors) < maxSyncResultErrors {
		result.Errors = append(result.Errors, item)
	}
}

// fetchFailureSyncError maps a connector error item into a structured, user-
// facing sample. Connectors that classify their errors (Feishu) provide a stable
// i18n code + params via metadata so the frontend localises it to the viewer's
// language; the raw status/body/log_id never leaves the server logs. Connectors
// without codes keep the raw text as a Message fallback. Best practice per
// Airbyte/Fivetran/Onyx: humanised, actionable, localised UI; raw detail in logs.
func fetchFailureSyncError(item *types.FetchedItem, rawMsg string) types.SyncItemError {
	e := types.SyncItemError{Title: item.Title}
	if code := item.Metadata["error_reason_code"]; code != "" {
		e.Code = code
		if v := item.Metadata["error_reason_code_value"]; v != "" {
			e.Params = map[string]string{"code": v}
		}
		e.Message = item.Metadata["error_reason"] // fallback if the client lacks the key
	} else {
		e.Message = rawMsg
	}
	return e
}

// applyFetchedItem writes a single fetched item into the knowledge base and
// updates result counters. It is the shared core of the batch loop and the
// streaming handler so item classification (deleted / empty / ingest outcome)
// stays identical across both fetch paths.
func (s *DataSourceService) applyFetchedItem(
	ctx context.Context, ds *types.DataSource, item *types.FetchedItem,
	tagIDs []string, result *types.SyncResult,
) {
	if item.IsDeleted {
		if !ds.SyncDeletions {
			// Sync deletion disabled: neither count nor delete.
			return
		}
		if item.ExternalID == "" {
			logger.Warnf(ctx, "skipping deletion for item %q: empty external_id", item.Title)
			result.Skipped++
			return
		}
		// Perform real KB deletion, scoped to items owned by this data source
		// so identical external IDs from different data sources cannot collide.
		repo := s.knowledgeService.GetRepository()
		existing, lookupErr := repo.FindByDataSourceExternalID(
			ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID, item.ExternalID,
		)
		if lookupErr != nil {
			logger.Errorf(ctx, "failed to find deleted knowledge for external_id=%s (ds=%s, kb=%s): %v",
				item.ExternalID, ds.ID, ds.KnowledgeBaseID, lookupErr)
			result.Failed++
			result.DeletionFailed++
			recordSyncError(result, types.SyncItemError{
				Title:   item.Title,
				Code:    "deletion_lookup_failed",
				Message: "Failed to look up the item before deletion; see server logs",
			})
			return
		}
		if existing == nil {
			// Deletion is idempotent: the source item may already have been
			// removed manually or by an earlier sync.
			result.Skipped++
			return
		}
		if deleteErr := s.knowledgeService.DeleteKnowledge(ctx, existing.ID); deleteErr != nil {
			// The cursor is already past this item, so a failed deletion normally
			// retries only on a later full sync. Counted separately so the
			// sync-log message can warn the operator about this gap.
			result.Failed++
			result.DeletionFailed++
			logger.Errorf(ctx, "failed to delete knowledge %s for external_id=%s (ds=%s): %v",
				existing.ID, item.ExternalID, ds.ID, deleteErr)
			recordSyncError(result, types.SyncItemError{
				Title:   item.Title,
				Code:    "deletion_failed",
				Message: "Deletion failed; see server logs",
			})
			return
		}
		if herr := repo.HardDeleteKnowledge(ctx, ds.TenantID, existing.ID); herr != nil {
			result.Failed++
			result.DeletionFailed++
			logger.Errorf(ctx, "failed to hard-delete knowledge %s for external_id=%s (ds=%s): %v",
				existing.ID, item.ExternalID, ds.ID, herr)
			recordSyncError(result, types.SyncItemError{
				Title:   item.Title,
				Code:    "deletion_failed",
				Message: "Deletion failed; see server logs",
			})
			return
		}
		result.Deleted++
		return
	}

	if len(item.Content) == 0 && item.URL == "" {
		// Check if this is an error item from the connector (failed to fetch content)
		if errMsg, hasErr := item.Metadata["error"]; hasErr {
			logger.Warnf(ctx, "item %q (external_id=%s) fetch failed: %s", item.Title, item.ExternalID, errMsg)
			result.Failed++
			recordSyncError(result, fetchFailureSyncError(item, errMsg))
		} else {
			logger.Infof(ctx, "skipping item %q (external_id=%s): no content or URL", item.Title, item.ExternalID)
			result.Skipped++
		}
		return
	}

	isUpdate, err := s.ingestItem(ctx, ds, item, tagIDs)
	if err != nil {
		var dupErr *types.DuplicateKnowledgeError
		switch {
		case errors.As(err, &dupErr):
			// Duplicate file/URL is not a failure — count as skipped.
			logger.Infof(ctx, "item %q (external_id=%s) already exists, skipping", item.Title, item.ExternalID)
			result.Skipped++
		case item.Metadata["embedded_image"] == "true":
			// An image extracted from a document for OCR is a best-effort
			// enrichment, not the document itself. If the KB cannot ingest it
			// (VLM/object-storage not configured for images, or a transient error),
			// skip it rather than failing the whole sync: the doc body already
			// synced, and the image stays in SubtreeKeep for a later retry once the
			// KB is configured.
			logger.Infof(ctx, "skipping embedded image %q (external_id=%s), not ingested: %v",
				item.Title, item.ExternalID, err)
			result.Skipped++
		default:
			logger.Warnf(ctx, "failed to ingest item %q (external_id=%s): %v", item.Title, item.ExternalID, err)
			result.Failed++
			recordSyncError(result, types.SyncItemError{
				Title:   item.Title,
				Code:    "ingest_failed",
				Message: "Ingest failed; see server logs",
			})
		}
	} else if isUpdate {
		result.Updated++
	} else {
		result.Created++
	}
}

// streamStartCursor decides which cursor a streaming fetch should resume from.
// A user-triggered full sync on its first attempt drops the cursor so every
// item is re-fetched; a retried full sync (attempt > 0) and every incremental
// sync resume from the last persisted checkpoint so a timed-out run converges
// instead of restarting from scratch.
func streamStartCursor(ds *types.DataSource, forceFull bool, attempt int) (*types.SyncCursor, error) {
	if forceFull && attempt == 0 {
		return nil, nil
	}
	return ds.ParseSyncCursor()
}

// streamSyncHandler adapts a streaming fetch to the knowledge-base ingest path.
// Emit ingests each item as it arrives (bounding memory) and Checkpoint persists
// the connector cursor plus live progress counts at page boundaries.
type streamSyncHandler struct {
	svc      *DataSourceService
	ds       *types.DataSource
	tagIDs   []string
	result   *types.SyncResult
	syncLog  *types.SyncLog
	guard    *syncAccessGuard
	runGuard *syncRunGuard
}

// Emit ingests one streamed item. A canceled context aborts the stream so the
// connector stops fetching; per-item ingest failures are recorded in result and
// do NOT abort (matching the batch loop, which never fails the whole sync for
// one bad document).
func (h *streamSyncHandler) Emit(ctx context.Context, item types.FetchedItem) error {
	if h.runGuard != nil {
		if err := h.runGuard.beforeItem(ctx, h.result.Total); err != nil {
			if errors.Is(err, errSyncRunTerminated) {
				return stopSyncAfterRunTermination(err)
			}
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.guard != nil {
		if err := h.guard.beforeItem(ctx, h.result.Total); err != nil {
			return err
		}
	}
	h.result.Total++
	h.svc.applyFetchedItem(withKBActivitySuppressed(ctx), h.ds, &item, h.tagIDs, h.result)
	return nil
}

// Checkpoint persists the connector cursor onto the data source and mirrors the
// running counts into the sync log so progress survives a crash and the UI can
// reflect a long sync mid-flight instead of jumping from 0 to done.
func (h *streamSyncHandler) Checkpoint(ctx context.Context, cursor *types.SyncCursor) error {
	if cursor == nil {
		return nil
	}
	if h.runGuard != nil {
		if err := ensureSyncRunActive(ctx, h.runGuard); err != nil {
			return err
		}
	}
	if h.guard != nil {
		if err := h.guard.check(ctx); err != nil {
			return err
		}
	}
	cursorJSON, err := cursor.ToJSON()
	if err != nil {
		return err
	}
	h.ds.LastSyncCursor = cursorJSON
	if err := h.svc.dsRepo.UpdateSyncState(ctx, h.ds); err != nil {
		return err
	}

	// Best-effort live progress; a failure here must not abort the sync.
	h.syncLog.ItemsTotal = h.result.Total
	h.syncLog.ItemsCreated = h.result.Created
	h.syncLog.ItemsUpdated = h.result.Updated
	h.syncLog.ItemsDeleted = h.result.Deleted
	h.syncLog.ItemsSkipped = h.result.Skipped
	h.syncLog.ItemsFailed = h.result.Failed
	applied, err := h.svc.syncLogRepo.UpdateResultIfRunning(ctx, h.syncLog)
	if err != nil {
		logger.Warnf(ctx, "failed to persist sync log progress at checkpoint: %v", err)
	} else if !applied {
		return stopSyncAfterRunTermination(errSyncRunTerminated)
	}
	return nil
}

// processSyncStreaming runs a sync through a StreamingConnector, ingesting each
// item as it arrives and checkpointing progress so the run is memory-bounded and
// resumable after a timeout.
func (s *DataSourceService) processSyncStreaming(
	ctx context.Context, sc datasource.StreamingConnector,
	ds *types.DataSource, syncLog *types.SyncLog,
	config *types.DataSourceConfig, payload types.DataSourceSyncPayload, wasPaused bool,
	runGuard *syncRunGuard,
) error {
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, nil, wasPaused, err)
	}
	// Tenant + auto-tag setup must precede fetching because the stream ingests
	// each item on the fly.
	ctx = context.WithValue(ctx, types.TenantIDContextKey, ds.TenantID)
	tenant, err := s.tenantRepo.GetTenantByID(ctx, ds.TenantID)
	if err != nil {
		logger.Errorf(ctx, "failed to get tenant info: %v", err)
		if updateErr := s.updateSyncRunResult(ctx, ds, syncLog, &types.SyncResult{}, nil,
			types.SyncLogStatusFailed, fmt.Sprintf("Failed to get tenant info: %v", err), wasPaused, true); updateErr != nil {
			return updateErr
		}
		return err
	}
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)

	autoTagIDs := s.resolveAutoTagIDs(ctx, ds)

	forceFull := payload.ForceFull || ds.SyncMode == types.SyncModeFull
	attempt, _, _ := syncTaskAttempt(ctx)
	startCursor, err := streamStartCursor(ds, forceFull, attempt)
	if err != nil {
		logger.Errorf(ctx, "failed to parse sync cursor: %v", err)
		if updateErr := s.updateSyncRunResult(ctx, ds, syncLog, &types.SyncResult{}, nil,
			types.SyncLogStatusFailed, fmt.Sprintf("Invalid cursor: %v", err), wasPaused, false); updateErr != nil {
			return updateErr
		}
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}

	result := &types.SyncResult{}
	guard := &syncAccessGuard{svc: s, ds: ds, config: config, allowPaused: wasPaused && payload.Trigger == "manual"}
	handler := &streamSyncHandler{svc: s, ds: ds, tagIDs: autoTagIDs, result: result, syncLog: syncLog, guard: guard, runGuard: runGuard}

	nextCursor, fetchErr := sc.FetchStream(ctx, config, startCursor, handler)
	if err := ensureSyncRunActive(ctx, runGuard); err != nil {
		return s.finishSyncRunGuardError(ctx, ds, syncLog, result, wasPaused, err)
	}
	if err := guard.check(ctx); err != nil {
		return s.stopSyncAfterAccessChange(ctx, ds, syncLog, result, wasPaused, err)
	}
	if errors.Is(fetchErr, errSyncAccessChanged) {
		return s.stopSyncAfterAccessChange(ctx, ds, syncLog, result, wasPaused, fetchErr)
	}
	if fetchErr != nil {
		// Progress so far is already checkpointed onto ds.LastSyncCursor; leave
		// it in place so the Asynq retry resumes from there. Persist counts.
		logger.Errorf(ctx, "streaming fetch failed: %v", fetchErr)
		resultJSON, _ := result.ToJSON()
		if updateErr := s.updateSyncRunResult(ctx, ds, syncLog, result, resultJSON,
			types.SyncLogStatusFailed, fmt.Sprintf("Fetch failed: %v", fetchErr), wasPaused, true); updateErr != nil {
			return updateErr
		}
		return fetchErr
	}

	resultJSON, _ := result.ToJSON()
	if err := allFetchedItemsFailedError(result); err != nil {
		logger.Errorf(ctx, "streaming sync failed while processing fetched items: %v", err)
		if updateErr := s.updateSyncRunResult(ctx, ds, syncLog, result, resultJSON,
			types.SyncLogStatusFailed, err.Error(), wasPaused, true); updateErr != nil {
			return updateErr
		}
		return err
	}

	// Persist the final cursor for the next incremental sync.
	if nextCursor != nil {
		if cursorJSON, cerr := nextCursor.ToJSON(); cerr == nil {
			ds.LastSyncCursor = cursorJSON
		}
	}
	ds.LastSyncAt = timePtr(time.Now().UTC())

	// Surface per-document failures as a partial sync (not silent success), so
	// the sync-log drawer's failure detail explains which docs didn't make it —
	// the visibility gap behind "status normal but not everything syncs"
	// (Tencent/WeKnora#2136). Fetch failures abort the stream before the failed
	// page is checkpointed, so the next run retries them; deletion failures are
	// past the cursor and only retry on a full sync in the normal case (see
	// applyFetchedItem).
	status := types.SyncLogStatusSuccess
	errMsg := ""
	if result.Failed > 0 {
		status = types.SyncLogStatusPartial
		errMsg = fmt.Sprintf("%d document(s) failed to sync", result.Failed)
		if result.DeletionFailed > 0 {
			errMsg += fmt.Sprintf("; %d deletion failure(s) will only retry on the next full sync", result.DeletionFailed)
		}
	}
	if err := s.updateSyncRunResult(ctx, ds, syncLog, result, resultJSON,
		status, errMsg, wasPaused, false); err != nil {
		return err
	}
	logger.Infof(ctx, "streaming sync completed: ds=%s created=%d updated=%d deleted=%d skipped=%d failed=%d",
		payload.DataSourceID, result.Created, result.Updated, result.Deleted, result.Skipped, result.Failed)
	return nil
}

func (s *DataSourceService) updateSyncRunResult(
	ctx context.Context,
	ds *types.DataSource,
	syncLog *types.SyncLog,
	result *types.SyncResult,
	resultJSON types.JSON,
	status string,
	errorMessage string,
	wasPaused bool,
	retryable bool,
) error {
	if result == nil {
		result = &types.SyncResult{}
	}
	// Preserve a running lease between retry attempts. A log becomes failed
	// only when no retry remains (or the failure is known permanent), so a
	// fresh/duplicate task can always treat failed as terminal without needing
	// to guess who wrote it.
	retryPending := status == types.SyncLogStatusFailed && retryable && syncTaskCanRetry(ctx)
	effectiveStatus := status
	if retryPending {
		effectiveStatus = types.SyncLogStatusRunning
	}
	syncLog.ItemsTotal = result.Total
	syncLog.ItemsCreated = result.Created
	syncLog.ItemsUpdated = result.Updated
	syncLog.ItemsDeleted = result.Deleted
	syncLog.ItemsSkipped = result.Skipped
	syncLog.ItemsFailed = result.Failed
	syncLog.Status = effectiveStatus
	if retryPending {
		syncLog.FinishedAt = nil
	} else {
		syncLog.FinishedAt = timePtr(time.Now().UTC())
	}
	syncLog.ErrorMessage = errorMessage
	syncLog.Result = resultJSON
	// Prepare the whole datasource outcome before handing it to the atomic
	// repository path. The transaction must persist the status/error/result
	// belonging to this exact SyncLog outcome, not the previous in-memory state.
	if effectiveStatus == types.SyncLogStatusFailed {
		if !wasPaused {
			ds.Status = types.DataSourceStatusError
		}
	} else if wasPaused {
		ds.Status = types.DataSourceStatusPaused
	} else {
		ds.Status = types.DataSourceStatusActive
	}
	ds.ErrorMessage = errorMessage
	ds.LastSyncResult = resultJSON
	persistCtx, cancelPersist := detachedSyncRunPersistenceContext(ctx)
	defer cancelPersist()
	var (
		applied bool
		err     error
	)
	if finalizer, ok := s.syncLogRepo.(syncRunAtomicFinalizer); ok {
		// Production path: rollback the log CAS if datasource state cannot be
		// persisted, keeping the run running for the next retry.
		applied, err = finalizer.UpdateResultAndDataSourceIfRunning(persistCtx, syncLog, ds)
	} else {
		// Safe fallback for lightweight adapters: write datasource state first,
		// so a state-write failure can never leave a terminal result behind.
		if err = s.dsRepo.UpdateSyncState(persistCtx, ds); err == nil {
			applied, err = s.syncLogRepo.UpdateResultIfRunning(persistCtx, syncLog)
		}
	}
	if err != nil {
		return fmt.Errorf("persist sync outcome: %w", err)
	}
	if !applied {
		return stopSyncAfterRunTermination(errSyncRunTerminated)
	}

	if retryPending {
		return nil
	}
	action := types.AuditActionDataSourceSyncCompleted
	outcome := types.AuditOutcomeSuccess
	if effectiveStatus == types.SyncLogStatusFailed {
		action = types.AuditActionDataSourceSyncFailed
		outcome = types.AuditOutcomeFailed
	} else if status == types.SyncLogStatusPartial {
		outcome = types.AuditOutcomePartial
	}
	recordKBActivity(persistCtx, s.audit, ds.TenantID, ds.KnowledgeBaseID, action,
		"data_source", ds.ID, outcome,
		map[string]any{
			"name": ds.Name, "type": ds.Type, "sync_log_id": syncLog.ID,
			"total": result.Total, "created": result.Created, "updated": result.Updated,
			"deleted": result.Deleted, "skipped": result.Skipped, "failed": result.Failed,
		})
	return nil
}

// failSyncRun records a failure through the running-only CAS. Retryable
// failures retain the running lease until the queue has exhausted its own
// attempts; permanent failures are terminal and explicitly stop retry.
func (s *DataSourceService) failSyncRun(
	ctx context.Context,
	ds *types.DataSource,
	syncLog *types.SyncLog,
	result *types.SyncResult,
	errorMessage string,
	wasPaused bool,
	cause error,
	retryable bool,
) error {
	if result == nil {
		result = &types.SyncResult{}
	}
	resultJSON, _ := result.ToJSON()
	if err := s.updateSyncRunResult(ctx, ds, syncLog, result, resultJSON,
		types.SyncLogStatusFailed, errorMessage, wasPaused, retryable); err != nil {
		return err
	}
	if !retryable {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, cause)
	}
	return cause
}

func allFetchedItemsFailedError(result *types.SyncResult) error {
	if result == nil || result.Total == 0 {
		return nil
	}
	if result.Failed != result.Total || result.Created != 0 || result.Updated != 0 ||
		result.Deleted != 0 || result.Skipped != 0 {
		return nil
	}

	detail := ""
	if len(result.Errors) > 0 {
		detail = result.Errors[0].Display()
		const maxDetailLen = 500
		if len(detail) > maxDetailLen {
			detail = detail[:maxDetailLen] + "..."
		}
	}
	if detail == "" {
		return fmt.Errorf("all fetched items failed during sync (%d/%d)", result.Failed, result.Total)
	}
	return fmt.Errorf("all fetched items failed during sync (%d/%d): %s", result.Failed, result.Total, detail)
}

// ValidateCredentials tests connectivity using raw credentials without persisting anything.
func (s *DataSourceService) ValidateCredentials(ctx context.Context, connectorType string, credentials map[string]interface{}) error {
	connector, err := s.connectorRegistry.Get(connectorType)
	if err != nil {
		return err
	}
	config := &types.DataSourceConfig{
		Type:        connectorType,
		Credentials: credentials,
	}
	if err := connector.Validate(ctx, config); err != nil {
		return err
	}

	return nil
}

// Helper functions

func (s *DataSourceService) validateDataSourceConfig(ctx context.Context, ds *types.DataSource) error {
	connector, err := s.connectorRegistry.Get(ds.Type)
	if err != nil {
		return err
	}

	config, err := ds.ParseConfig()
	if err != nil {
		return datasource.ErrInvalidConfig
	}

	return connector.Validate(ctx, config)
}

// ingestItem writes a single FetchedItem into the knowledge base.
// If a knowledge item with the same external_id already exists, it is deleted first (update = delete + re-create).
//
// Routing logic:
//   - Has Content bytes → CreateKnowledgeFromFile (走完整的文档解析 pipeline)
//   - Has URL only      → CreateKnowledgeFromURL  (让 WeKnora 下载并解析)
//
// Returns (isUpdate, error) — isUpdate is true when an existing item was replaced.
func (s *DataSourceService) ingestItem(ctx context.Context, ds *types.DataSource, item *types.FetchedItem, tagIDs []string) (bool, error) {
	if ds.Type == types.ConnectorTypeGitHub || ds.Type == localfolder.Type {
		return s.ingestPreparedFile(ctx, ds, item, tagIDs)
	}
	// Channel decides the knowledge "source" label shown in the UI. Prefer the
	// connector-supplied metadata["channel"] (e.g. Feishu Drive sets it to
	// "feishu" so Drive docs share the wiki's "飞书" label instead of showing
	// "unknown" for the raw ds.Type "feishu_drive"). Fall back to ds.Type so
	// connectors that don't set metadata.channel still get a meaningful label.
	channel := ds.Type // e.g. "feishu", "notion"
	if item.Metadata != nil {
		if mc, ok := item.Metadata["channel"]; ok && mc != "" {
			channel = mc
		}
	}

	metadata := map[string]string{
		"external_id":        item.ExternalID,
		"source_resource_id": item.SourceResourceID,
		"datasource_id":      ds.ID,
	}
	// The source system's own last-modified time, when the connector supplied
	// one. The knowledge row's UpdatedAt moves on every re-parse, so this is
	// the only record of how old the document itself is.
	if !item.UpdatedAt.IsZero() {
		metadata["source_updated_at"] = item.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !item.CreatedAt.IsZero() {
		metadata["source_created_at"] = item.CreatedAt.UTC().Format(time.RFC3339)
	}
	for k, v := range item.Metadata {
		metadata[k] = v
	}

	// Check if a knowledge item with this external_id already exists → delete it first (update)
	isUpdate := false
	if item.ExternalID != "" {
		repo := s.knowledgeService.GetRepository()
		// Scope the lookup to items owned by this data source so identical
		// external IDs from two data sources cannot collide or overwrite each
		// other during updates.
		existing, err := repo.FindByDataSourceExternalID(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID, item.ExternalID)
		if err != nil {
			logger.Warnf(ctx, "failed to check existing knowledge for external_id=%s: %v", item.ExternalID, err)
			// Non-fatal: proceed with creation (may produce duplicate)
		} else if existing != nil {
			logger.Infof(ctx, "found existing knowledge %s for external_id=%s, deleting for update", existing.ID, item.ExternalID)
			if err := s.knowledgeService.DeleteKnowledge(ctx, existing.ID); err != nil {
				logger.Warnf(ctx, "failed to delete existing knowledge %s: %v", existing.ID, err)
			} else {
				if herr := repo.HardDeleteKnowledge(ctx, ds.TenantID, existing.ID); herr != nil {
					logger.Warnf(ctx, "failed to hard-delete replaced knowledge %s: %v", existing.ID, herr)
				}
				isUpdate = true
			}
		}
	}

	// Case 1: content already fetched → build a FileHeader from bytes and call CreateKnowledgeFromFile
	if len(item.Content) > 0 {
		fh, err := bytesToFileHeader(item.Content, item.FileName)
		if err != nil {
			return isUpdate, fmt.Errorf("build file header: %w", err)
		}
		if _, err := s.knowledgeService.CreateKnowledgeFromFile(
			ctx,
			ds.KnowledgeBaseID,
			fh,
			metadata,
			nil,           // use KB default for multimodal
			item.FileName, // customFileName — must include extension for file-type validation
			tagIDs,        // auto-tag from data source
			channel,
			nil,
		); err != nil {
			var dupErr *types.DuplicateKnowledgeError
			if errors.As(err, &dupErr) && dupIsSameNode(dupErr, item) {
				// Identical content is already present in the KB under THIS node's
				// own external_id, so the parent effectively exists — reconcile the
				// subtree so children removed from the doc do not linger.
				s.sweepStaleSubtree(ctx, ds, item)
			}
			return isUpdate, err
		}
		s.sweepStaleSubtree(ctx, ds, item)
		return isUpdate, nil
	}

	// Case 2: only a remote URL — let WeKnora handle downloading and parsing
	if item.URL != "" {
		created, err := s.knowledgeService.CreateKnowledgeFromURL(
			ctx,
			ds.KnowledgeBaseID,
			item.URL,
			item.FileName,
			"",  // auto-detect file type
			nil, // use KB default for multimodal
			item.Title,
			tagIDs, // auto-tag from data source
			channel,
			nil,
		)
		if err != nil {
			var dupErr *types.DuplicateKnowledgeError
			if errors.As(err, &dupErr) && dupIsSameNode(dupErr, item) {
				// Identical content is already present in the KB under THIS node's
				// own external_id, so the parent effectively exists — reconcile the
				// subtree so children removed from the doc do not linger.
				s.sweepStaleSubtree(ctx, ds, item)
			}
			return isUpdate, err
		}
		// URL-created knowledge has no metadata, so a later deletion could
		// never find it. Attach the datasource keys on fresh creation only;
		// the duplicate path reuses an existing row that must not be re-tagged.
		if created != nil {
			metadataBytes, mErr := json.Marshal(metadata)
			if mErr != nil {
				return isUpdate, fmt.Errorf("marshal datasource metadata: %w", mErr)
			}
			created.Metadata = types.JSON(metadataBytes)
			if uErr := s.knowledgeService.GetRepository().UpdateKnowledge(ctx, created); uErr != nil {
				return isUpdate, fmt.Errorf("attach datasource metadata: %w", uErr)
			}
		}
		s.sweepStaleSubtree(ctx, ds, item)
		return isUpdate, nil
	}

	return isUpdate, fmt.Errorf("item has neither content nor URL")
}

// dupIsSameNode reports whether a duplicate-content error means the parent still
// exists in the KB *under this item's own external_id* — i.e. a content-dedup hit
// against this same node, so reconciling its subtree is safe. File deduplication
// keys on file_hash plus file_type (CheckKnowledgeExists), so an updated node whose rebuilt body
// happens to hash-collide with a DIFFERENT knowledge item (another node, or a
// manually-uploaded file with no external_id) would otherwise sweep this node's
// children even though its own parent row was just deleted for the update and
// never recreated — deleting those children with no parent to replace them. In
// that case the matched row's external_id differs (or is absent), so we skip the
// sweep and leave the children intact.
func dupIsSameNode(dupErr *types.DuplicateKnowledgeError, item *types.FetchedItem) bool {
	return dupErr != nil && dupErr.Knowledge != nil &&
		dupErr.Knowledge.GetMetadata()["external_id"] == item.ExternalID
}

// sweepStaleSubtree deletes STALE sub-items of item — knowledge whose external_id
// is prefixed with "<item.ExternalID>#" (e.g. attachment children of a docx node)
// that is NOT listed in item.SubtreeKeep, i.e. no longer present in the source.
//
// It runs only AFTER the parent item exists in the KB (freshly (re)created, or
// confirmed present via a duplicate-hash error), so a genuinely failed parent
// write never destroys existing children. Children still present in the source
// are preserved via SubtreeKeep even when they could not be re-ingested this
// cycle (e.g. a transient attachment download failure), so a still-present
// attachment never loses its previously-synced good copy. The "<id>#" prefix
// never matches the parent's own "<id>" external_id, so the parent is never
// self-swept.
func (s *DataSourceService) sweepStaleSubtree(ctx context.Context, ds *types.DataSource, item *types.FetchedItem) {
	if !item.ReplacesSubtree || item.ExternalID == "" {
		return
	}
	repo := s.knowledgeService.GetRepository()
	children, err := repo.FindByMetadataKeyPrefix(ctx, ds.TenantID, ds.KnowledgeBaseID, "external_id", types.SubtreeChildPrefix(item.ExternalID))
	if err != nil {
		logger.Warnf(ctx, "failed to list subtree of external_id=%s: %v", item.ExternalID, err)
		return
	}
	if len(children) == 0 {
		return
	}
	ids := make([]string, 0, len(children))
	for _, child := range children {
		// Scope to this data source so identical external_id prefixes from
		// another connector in the same KB cannot be swept.
		if child.GetMetadata()["datasource_id"] != ds.ID {
			continue
		}
		// A child still present in the source is preserved even if it could not be
		// re-ingested this sync; only children that vanished from the source are
		// stale and swept. Every child here was selected by the external_id-prefix
		// query, so its external_id is guaranteed present and readable (a malformed
		// row could not have matched the SQL predicate), and GetMetadata resolves
		// it identically to the keep-set entries the connector built. SubtreeKeep
		// holds one entry per still-present sub-item of this node (a small set), so
		// a linear scan is cheaper than materializing a lookup map.
		if slices.Contains(item.SubtreeKeep, child.GetMetadata()["external_id"]) {
			continue
		}
		ids = append(ids, child.ID)
	}
	if len(ids) == 0 {
		return
	}
	// Batch the deletion so a node whose attachment set shrank from N pays one
	// round of the delete fan-out rather than N sequential ones.
	if derr := s.knowledgeService.DeleteKnowledgeList(ctx, ids); derr != nil {
		logger.Warnf(ctx, "failed to delete %d stale sub-item(s) of external_id=%s: %v",
			len(ids), item.ExternalID, derr)
	} else if herr := repo.HardDeleteKnowledgeList(ctx, ds.TenantID, ids); herr != nil {
		logger.Warnf(ctx, "failed to hard-delete %d stale sub-item(s) of external_id=%s: %v",
			len(ids), item.ExternalID, herr)
	}
}

// bytesToFileHeader wraps a []byte into a *multipart.FileHeader so it can be
// consumed by KnowledgeService.CreateKnowledgeFromFile.
func bytesToFileHeader(data []byte, filename string) (*multipart.FileHeader, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Create a form file part
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	partHeader.Set("Content-Type", "application/octet-stream")

	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return nil, fmt.Errorf("create multipart part: %w", err)
	}

	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("write data to part: %w", err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	// Parse the multipart data to get a FileHeader
	reader := multipart.NewReader(&buf, writer.Boundary())
	form, err := reader.ReadForm(int64(len(data)) + 1024)
	if err != nil {
		return nil, fmt.Errorf("read multipart form: %w", err)
	}

	files := form.File["file"]
	if len(files) == 0 {
		return nil, fmt.Errorf("no file in multipart form")
	}

	return files[0], nil
}

func timePtr(t time.Time) *time.Time {
	utc := t.UTC()
	return &utc
}
