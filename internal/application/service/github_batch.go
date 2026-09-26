package service

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"

	githubConnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/robfig/cron/v3"
)

const maxGitHubBatchRepositories = 20
const maxGitHubBatchExclusions = 100
const defaultGitHubBatchSchedule = "0 0 */6 * * *"

// DiscoverGitHubRepositories intentionally performs only discovery. Credentials
// live in the request for the duration of this call and are neither persisted
// nor included in the response.
func (s *DataSourceService) DiscoverGitHubRepositories(ctx context.Context, req *types.GitHubDiscoveryRequest) (*types.GitHubDiscoveryResponse, error) {
	if req == nil || req.TenantID == 0 || !githubConnector.ValidOwner(req.Owner) {
		return nil, errors.New("GitHub organization or user name is invalid")
	}
	connector, err := s.connectorRegistry.Get(types.ConnectorTypeGitHub)
	if err != nil {
		return nil, err
	}
	github, ok := connector.(*githubConnector.Connector)
	if !ok {
		return nil, errors.New("GitHub repository discovery is unavailable")
	}
	config := &types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Credentials: req.Credentials, Settings: map[string]interface{}{}}
	config.StripNonSecretCredentials(types.ConnectorTypeGitHub)
	if err := github.Validate(ctx, config); err != nil {
		return nil, err
	}
	page, err := github.DiscoverRepositories(ctx, config, req.Owner, req.Cursor)
	if err != nil {
		return nil, err
	}
	return &types.GitHubDiscoveryResponse{Owner: page.Owner, Repositories: page.Repositories, NextCursor: page.NextCursor}, nil
}

// CreateGitHubBatch keeps every selected repository as an ordinary data source.
// It returns a result per repository rather than hiding partial work behind a
// single collection row. The caller may safely retry a partially successful
// request: matching repository/mode pairs are reported as existing.
func (s *DataSourceService) CreateGitHubBatch(ctx context.Context, req *types.GitHubBatchRequest) (*types.GitHubBatchResponse, error) {
	if req == nil || req.TenantID == 0 || req.KnowledgeBaseID == "" || !githubConnector.ValidOwner(req.Owner) {
		return nil, errors.New("GitHub batch request is invalid")
	}
	if len(req.Repositories) == 0 || len(req.Repositories) > maxGitHubBatchRepositories {
		return nil, fmt.Errorf("select between 1 and %d GitHub repositories per batch", maxGitHubBatchRepositories)
	}
	mode := requestedGitHubBatchMode(req.Mode)
	if mode == "" {
		return nil, errors.New("GitHub batch mode must be source or documents")
	}
	if len(req.Exclude) > maxGitHubBatchExclusions {
		return nil, fmt.Errorf("GitHub batch exclusions must contain at most %d paths", maxGitHubBatchExclusions)
	}
	syncPlan, err := resolveGitHubBatchSyncPlan(req)
	if err != nil {
		return nil, err
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, req.KnowledgeBaseID)
	if err != nil || kb == nil || kb.TenantID != req.TenantID {
		return nil, errors.New("knowledge base not found")
	}
	existing, err := s.dsRepo.FindByKnowledgeBase(ctx, req.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	present := githubDataSourcePairs(existing)
	response := &types.GitHubBatchResponse{Owner: strings.TrimSpace(req.Owner), Results: make([]types.GitHubBatchItemResult, 0, len(req.Repositories))}
	seen := map[string]bool{}
	for _, candidate := range req.Repositories {
		repository := strings.TrimSuffix(strings.TrimSpace(candidate.Repository), ".git")
		key, validPair := canonicalGitHubDataSourcePair(repository, mode)
		result := types.GitHubBatchItemResult{Repository: repository}
		if !validPair {
			result.Status, result.Message = "failed", "Repository must be an owner/name GitHub repository"
			response.Results = append(response.Results, result)
			continue
		}
		if seen[key] {
			result.Status, result.Message = "existing", "Repository appears more than once in this batch"
			response.Results = append(response.Results, result)
			continue
		}
		seen[key] = true
		if !belongsToOwner(repository, req.Owner) {
			result.Status, result.Message = "failed", "Repository does not belong to the selected GitHub organization or user"
			response.Results = append(response.Results, result)
			continue
		}
		if candidate.Archived || candidate.Disabled || candidate.Fork {
			result.Status, result.Message = "failed", "Archived, disabled, and fork repositories require an explicit single-source review"
			response.Results = append(response.Results, result)
			continue
		}
		if id, exists := present[key]; exists {
			result.Status, result.DataSourceID, result.Message = "existing", id, "This repository and mode are already configured"
			response.Results = append(response.Results, result)
			continue
		}
		ref := strings.TrimSpace(candidate.DefaultBranch)
		settings := githubBatchSettings(repository, ref, mode, req.Paths, req.Exclude)
		config := &types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Credentials: req.Credentials, Settings: settings}
		config.StripNonSecretCredentials(types.ConnectorTypeGitHub)
		blob, configErr := config.ToJSON()
		if configErr != nil {
			result.Status, result.Message = "failed", "GitHub configuration could not be secured"
			response.Results = append(response.Results, result)
			continue
		}
		ds := &types.DataSource{
			TenantID: req.TenantID, KnowledgeBaseID: req.KnowledgeBaseID,
			Name: "GitHub · " + repository, Type: types.ConnectorTypeGitHub, Config: blob,
			SyncSchedule: syncPlan.scheduleFor(repository, mode), SyncMode: types.SyncModeIncremental,
			Status: types.DataSourceStatusActive, ConflictStrategy: types.ConflictStrategyOverwrite,
		}
		created, createErr := s.CreateDataSource(ctx, ds)
		if createErr != nil {
			result.Status, result.Message = "failed", createErr.Error()
			response.Results = append(response.Results, result)
			continue
		}
		present[key] = created.ID
		result.Status, result.DataSourceID, result.Message = "created", created.ID, "Created"
		if syncPlan.StartSync {
			if _, syncErr := s.ManualSync(ctx, created.ID); syncErr != nil {
				result.Message = "Created, but its first sync was not queued: " + syncErr.Error()
			} else {
				result.Message = "Created and queued for sync"
			}
		}
		response.Results = append(response.Results, result)
	}
	return response, nil
}

type githubBatchSyncPlan struct {
	Schedule  string
	StartSync bool
	Staggered bool
}

// scheduleFor deterministically spreads a repository usage across the 360
// minutes in each six-hour window. The persisted value remains an ordinary
// six-field cron expression, so existing scheduler/runtime code is unchanged.
// Source mode is placed 180 minutes after document mode, so the same
// repository's two usages provably never compete at the same instant.
func (p githubBatchSyncPlan) scheduleFor(repository, mode string) string {
	if !p.Staggered {
		return p.Schedule
	}
	return githubStaggeredSixHourSchedule(repository, mode)
}

func githubStaggeredSixHourSchedule(repository, mode string) string {
	repository = canonicalGitHubRepository(repository)
	mode = requestedGitHubBatchMode(mode)
	if repository == "" || mode == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(repository))
	// Avalanche the FNV hash before reducing it to 360 slots. Otherwise names
	// with sequential numeric suffixes disproportionately hit a few minutes.
	v := h.Sum32()
	v ^= v >> 16
	v *= 0x85ebca6b
	v ^= v >> 13
	v *= 0xc2b2ae35
	v ^= v >> 16
	slot := int(v % 360)
	if mode == "source" {
		slot = (slot + 180) % 360
	}
	hour, minute := slot/60, slot%60
	return fmt.Sprintf("0 %d %d,%d,%d,%d * * *", minute, hour, hour+6, hour+12, hour+18)
}

// PreviewGitHubLegacySchedules never guesses that an operator's custom cron
// should be moved. Only active GitHub sources with the exact old six-hour
// default are eligible; paused/error/manual/custom rows remain untouched.
func PreviewGitHubLegacySchedules(rows []*types.DataSource) []types.GitHubScheduleMigrationPreviewItem {
	previews := make([]types.GitHubScheduleMigrationPreviewItem, 0, len(rows))
	for _, ds := range rows {
		if ds == nil || ds.Type != types.ConnectorTypeGitHub {
			continue
		}
		p := types.GitHubScheduleMigrationPreviewItem{
			DataSourceID: ds.ID, Status: ds.Status, Current: ds.SyncSchedule, UpdatedAt: ds.UpdatedAt,
		}
		config, err := ds.ParseConfig()
		if err != nil || config == nil {
			p.Reason = "invalid_config"
			previews = append(previews, p)
			continue
		}
		repository, _ := config.Settings["repository"].(string)
		mode, _ := config.Settings["mode"].(string)
		p.Repository = canonicalGitHubRepository(repository)
		p.Mode = storedGitHubDataSourceMode(mode)
		switch {
		case p.Repository == "" || p.Mode == "":
			p.Reason = "invalid_selection"
		case ds.Status != types.DataSourceStatusActive:
			p.Reason = "not_active"
		case ds.SyncSchedule == "":
			p.Reason = "manual_schedule"
		case ds.SyncSchedule != defaultGitHubBatchSchedule:
			p.Reason = "custom_schedule"
		default:
			p.Proposed = githubStaggeredSixHourSchedule(p.Repository, p.Mode)
			p.Eligible = true
			p.Reason = "eligible"
		}
		previews = append(previews, p)
	}
	return previews
}

// resolveGitHubBatchSyncPlan deliberately treats an omitted policy as a
// compatibility path. Existing clients used an empty schedule to request the
// six-hour default. New clients must opt into one of two unambiguous modes:
// manual creates a dormant source, while scheduled requires a valid cron.
func resolveGitHubBatchSyncPlan(req *types.GitHubBatchRequest) (githubBatchSyncPlan, error) {
	if req == nil {
		return githubBatchSyncPlan{}, errors.New("GitHub batch request is invalid")
	}

	policy := strings.ToLower(strings.TrimSpace(req.SyncPolicy))
	schedule := strings.TrimSpace(req.SyncSchedule)
	switch policy {
	case "":
		// Preserve the old API contract for callers that have not yet upgraded
		// to sync_policy. An omitted schedule has always meant six-hour syncs.
		if schedule == "" {
			schedule = defaultGitHubBatchSchedule
		}
	case "manual":
		if schedule != "" {
			return githubBatchSyncPlan{}, errors.New("manual GitHub batch sync policy cannot include a schedule")
		}
		if req.StartSync {
			return githubBatchSyncPlan{}, errors.New("manual GitHub batch sync policy cannot queue an initial sync")
		}
		return githubBatchSyncPlan{}, nil
	case "staggered":
		if schedule != "" {
			return githubBatchSyncPlan{}, errors.New("staggered GitHub batch sync policy cannot include a schedule")
		}
		return githubBatchSyncPlan{StartSync: req.StartSync, Staggered: true}, nil
	case "scheduled":
		if schedule == "" {
			return githubBatchSyncPlan{}, errors.New("scheduled GitHub batch sync policy requires a schedule")
		}
	default:
		return githubBatchSyncPlan{}, errors.New("GitHub batch sync policy must be manual or scheduled")
	}

	if err := validateGitHubBatchSchedule(schedule); err != nil {
		return githubBatchSyncPlan{}, err
	}
	return githubBatchSyncPlan{Schedule: schedule, StartSync: req.StartSync}, nil
}

// validateGitHubBatchSchedule uses the exact six-field parser the runtime
// scheduler uses. Validating before rows are created prevents a batch from
// persisting a schedule which would later be silently skipped at startup.
func validateGitHubBatchSchedule(schedule string) error {
	cronScheduler := cron.New(cron.WithSeconds())
	if _, err := cronScheduler.AddFunc(schedule, func() {}); err != nil {
		return fmt.Errorf("GitHub batch sync schedule is invalid: %w", err)
	}
	return nil
}

// githubBatchSettings writes only explicit selection overrides. In particular,
// an empty exclusion list means "use the source defaults", so it must be
// omitted instead of serialized as JSON null. The latter was rejected by
// source-policy validation and made every repository in a batch fail.
func githubBatchSettings(repository, ref, mode string, paths, excludes []string) map[string]interface{} {
	settings := map[string]interface{}{
		"repository": repository,
		"ref":        ref,
		"paths":      append([]string(nil), paths...),
		"mode":       mode,
	}
	if len(excludes) > 0 {
		settings["exclude"] = append([]string(nil), excludes...)
	}
	return settings
}

func belongsToOwner(repository, owner string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && strings.EqualFold(parts[0], strings.TrimSpace(owner))
}

// canonicalGitHubDataSourcePair is the one identity rule used for both a
// batch candidate and an existing data source. Older data sources stored a
// mixture of owner/name, HTTPS URLs, optional .git suffixes, and sometimes no
// mode at all. Treating those representations as different made safe retries
// create duplicates instead of reporting the existing source.
func canonicalGitHubDataSourcePair(repository, mode string) (string, bool) {
	repository = canonicalGitHubRepository(repository)
	mode = storedGitHubDataSourceMode(mode)
	if repository == "" || mode == "" {
		return "", false
	}
	return repository + "\x00" + mode, true
}

// canonicalGitHubRepository returns the case-insensitive owner/name identity
// used for duplicate detection. It deliberately accepts the legacy HTTPS URL
// representation, but requires exactly one owner and one repository segment
// so a tree/blob URL can never be mistaken for a repository source.
func canonicalGitHubRepository(repository string) string {
	config := &types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: map[string]interface{}{
		"repository": repository,
	}}
	canonical, ok := githubConnector.ConfiguredRepository(config)
	if !ok {
		return ""
	}
	return strings.ToLower(canonical)
}

// requestedGitHubBatchMode validates new requests. A missing mode is never
// silently accepted for a new batch: the client must make the user's chosen
// ingestion behavior explicit.
func requestedGitHubBatchMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "documents":
		return "documents"
	case "source":
		return "source"
	default:
		return ""
	}
}

// storedGitHubDataSourceMode preserves the original document-import behavior
// for pre-mode data sources. Source snapshots and document ingestion remain
// intentionally distinct so both can coexist for the same repository.
func storedGitHubDataSourceMode(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return "documents"
	}
	return requestedGitHubBatchMode(mode)
}

func githubDataSourcePairs(rows []*types.DataSource) map[string]string {
	pairs := map[string]string{}
	for _, ds := range rows {
		if ds == nil || ds.Type != types.ConnectorTypeGitHub {
			continue
		}
		config, err := ds.ParseConfig()
		if err != nil || config == nil {
			continue
		}
		repository, _ := config.Settings["repository"].(string)
		mode, _ := config.Settings["mode"].(string)
		key, ok := canonicalGitHubDataSourcePair(repository, mode)
		if !ok {
			continue
		}
		pairs[key] = ds.ID
	}
	return pairs
}
