package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	githubConnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
)

const maxGitHubBatchRepositories = 20
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
	mode := strings.TrimSpace(req.Mode)
	if mode != "source" && mode != "documents" {
		return nil, errors.New("GitHub batch mode must be source or documents")
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
		key := strings.ToLower(repository) + "\x00" + mode
		result := types.GitHubBatchItemResult{Repository: repository}
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
		schedule := strings.TrimSpace(req.SyncSchedule)
		if schedule == "" {
			schedule = defaultGitHubBatchSchedule
		}
		ds := &types.DataSource{
			TenantID: req.TenantID, KnowledgeBaseID: req.KnowledgeBaseID,
			Name: "GitHub · " + repository, Type: types.ConnectorTypeGitHub, Config: blob,
			SyncSchedule: schedule, SyncMode: types.SyncModeIncremental,
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
		if req.StartSync {
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
		if repository == "" || (mode != "source" && mode != "documents") {
			continue
		}
		pairs[strings.ToLower(strings.TrimSuffix(repository, ".git"))+"\x00"+mode] = ds.ID
	}
	return pairs
}
