package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

const maxGitHubScheduleMigrationItems = 100

// An optional repository capability keeps the wider data-source repository
// interface and its lightweight adapters unchanged. Production SQL implements
// the complete tenant/KB/version/running-sync predicate atomically.
type githubScheduleMigrationCAS interface {
	UpdateGitHubScheduleIfUnchanged(context.Context, uint64, string, string, time.Time, string) (bool, error)
}

func (s *DataSourceService) migrationKnowledgeBase(ctx context.Context, tenantID uint64, kbID string) error {
	if tenantID == 0 || kbID == "" {
		return errors.New("knowledge base not found")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil || kb == nil || kb.TenantID != tenantID {
		return errors.New("knowledge base not found")
	}
	return nil
}

// PreviewGitHubScheduleMigration is read-only. Existing custom, manual,
// paused and error sources are visible as skipped rows, not silently changed.
func (s *DataSourceService) PreviewGitHubScheduleMigration(
	ctx context.Context, req *types.GitHubScheduleMigrationPreviewRequest,
) (*types.GitHubScheduleMigrationPreviewResponse, error) {
	if req == nil || s.migrationKnowledgeBase(ctx, req.TenantID, req.KnowledgeBaseID) != nil {
		return nil, errors.New("knowledge base not found")
	}
	rows, err := s.dsRepo.FindByKnowledgeBase(ctx, req.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	owned := make([]*types.DataSource, 0, len(rows))
	for _, row := range rows {
		if row != nil && row.TenantID == req.TenantID {
			owned = append(owned, row)
		}
	}
	items := PreviewGitHubLegacySchedules(owned)
	for index := range items {
		if !items[index].Eligible {
			continue
		}
		running, err := s.syncLogRepo.HasRunningSync(ctx, items[index].DataSourceID)
		if err != nil {
			return nil, err
		}
		if running {
			items[index].Eligible = false
			items[index].Proposed = ""
			items[index].Reason = "running_sync"
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Repository == items[j].Repository {
			return items[i].Mode < items[j].Mode
		}
		return items[i].Repository < items[j].Repository
	})
	return &types.GitHubScheduleMigrationPreviewResponse{KnowledgeBaseID: req.KnowledgeBaseID, Items: items}, nil
}

// ApplyGitHubScheduleMigration changes only rows explicitly selected from a
// matching preview. Every row is independently CAS-guarded, so one stale row
// cannot make another row unsafe or require a mass retry of the whole batch.
func (s *DataSourceService) ApplyGitHubScheduleMigration(
	ctx context.Context, req *types.GitHubScheduleMigrationApplyRequest,
) (*types.GitHubScheduleMigrationApplyResponse, error) {
	if req == nil || s.migrationKnowledgeBase(ctx, req.TenantID, req.KnowledgeBaseID) != nil {
		return nil, errors.New("knowledge base not found")
	}
	if len(req.Selections) == 0 || len(req.Selections) > maxGitHubScheduleMigrationItems {
		return nil, errors.New("select between 1 and 100 GitHub sources")
	}
	cas, ok := s.dsRepo.(githubScheduleMigrationCAS)
	if !ok {
		return nil, errors.New("GitHub schedule migration is unavailable")
	}
	seen := make(map[string]bool, len(req.Selections))
	for _, choice := range req.Selections {
		if choice.DataSourceID == "" || choice.ExpectedSchedule != defaultGitHubBatchSchedule ||
			choice.ExpectedStatus != types.DataSourceStatusActive || choice.ExpectedUpdatedAt.IsZero() ||
			choice.ExpectedProposed == "" || seen[choice.DataSourceID] {
			return nil, errors.New("GitHub schedule migration selection is invalid or duplicated")
		}
		seen[choice.DataSourceID] = true
	}
	response := &types.GitHubScheduleMigrationApplyResponse{
		KnowledgeBaseID: req.KnowledgeBaseID,
		Results:         make([]types.GitHubScheduleMigrationApplyItem, 0, len(req.Selections)),
	}
	for _, choice := range req.Selections {
		result := types.GitHubScheduleMigrationApplyItem{DataSourceID: choice.DataSourceID, Status: "skipped"}
		ds, err := s.dsRepo.FindByID(ctx, choice.DataSourceID)
		if err != nil || ds == nil || ds.TenantID != req.TenantID || ds.KnowledgeBaseID != req.KnowledgeBaseID || ds.Type != types.ConnectorTypeGitHub {
			result.Reason = "not_found"
			response.Results = append(response.Results, result)
			continue
		}
		plan := PreviewGitHubLegacySchedules([]*types.DataSource{ds})[0]
		proposed := githubStaggeredSixHourSchedule(plan.Repository, plan.Mode)
		if choice.ExpectedProposed != proposed || proposed == "" {
			result.Reason = "stale_preview"
		} else if ds.SyncSchedule == proposed && ds.Status == types.DataSourceStatusActive {
			result.Reason = "already_applied"
			result.Schedule = ds.SyncSchedule
		} else if !plan.Eligible {
			result.Reason = plan.Reason
			result.Schedule = ds.SyncSchedule
		} else if !ds.UpdatedAt.Equal(choice.ExpectedUpdatedAt) || ds.SyncSchedule != choice.ExpectedSchedule || ds.Status != choice.ExpectedStatus {
			result.Reason = "stale_preview"
			result.Schedule = ds.SyncSchedule
		} else {
			running, checkErr := s.syncLogRepo.HasRunningSync(ctx, ds.ID)
			if checkErr != nil {
				result.Reason = "sync_state_unavailable"
			} else if running {
				result.Reason = "running_sync"
			} else {
				applied, updateErr := cas.UpdateGitHubScheduleIfUnchanged(ctx, req.TenantID, req.KnowledgeBaseID, ds.ID, choice.ExpectedUpdatedAt, proposed)
				if updateErr != nil {
					result.Reason = "storage_error"
				} else if !applied {
					result.Reason = "stale_or_running"
				} else {
					result.Status = "applied"
					result.Schedule = proposed
					current, readErr := s.dsRepo.FindByID(ctx, ds.ID)
					if readErr != nil || current == nil {
						result.Status = "applied_with_warning"
						result.Reason = "readback_failed"
					} else if current.SyncSchedule != proposed {
						result.Status = "applied_with_warning"
						result.Reason = "changed_after_apply"
						result.Schedule = current.SyncSchedule
					} else if s.scheduler == nil {
						result.Status = "applied_with_warning"
						result.Reason = "scheduler_unavailable"
					} else {
						if err := s.scheduler.AddOrUpdate(current); err != nil {
							result.Status = "applied_with_warning"
							result.Reason = "scheduler_refresh_failed"
						}
					}
					outcome := types.AuditOutcomeSuccess
					if result.Status == "applied_with_warning" {
						outcome = types.AuditOutcomePartial
					}
					recordKBActivity(ctx, s.audit, req.TenantID, req.KnowledgeBaseID,
						types.AuditActionDataSourceUpdated, "data_source", ds.ID, outcome,
						map[string]any{"name": ds.Name, "type": ds.Type, "changed_fields": []string{"sync_schedule"},
							"previous_schedule": choice.ExpectedSchedule, "new_schedule": proposed, "trigger": "github_schedule_migration"})
				}
			}
		}
		response.Results = append(response.Results, result)
	}
	return response, nil
}
