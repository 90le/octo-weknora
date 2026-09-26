package service

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	githubScopePreviewTimeout   = 12 * time.Second
	githubScopePreviewMaxPaths  = 100
	githubScopePreviewMaxGroup  = 20
	githubScopePreviewMaxSample = 20
)

// PreviewGitHubDocumentScope reads only a fixed-commit GitHub tree. It never
// writes source settings, uses a Git clone/cache, downloads a blob, or enqueues
// a sync. The caller's KB/tenant authority is checked again here so internal
// callers cannot bypass the HTTP route's ownership and KB-write guards.
func (s *DataSourceService) PreviewGitHubDocumentScope(
	ctx context.Context, tenantID uint64, kbID string, req *types.GitHubDocumentScopePreviewRequest,
) (*types.GitHubDocumentScopePreview, error) {
	if tenantID == 0 || kbID == "" || req == nil || req.SourceID == "" || len(req.SourceID) > 128 {
		return nil, datasource.ErrDataSourceInvalid
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil || kb == nil || kb.TenantID != tenantID {
		return nil, datasource.ErrKnowledgeBaseNotFound
	}
	ds, err := s.dsRepo.FindByID(ctx, req.SourceID)
	if err != nil || ds == nil || ds.TenantID != tenantID || ds.KnowledgeBaseID != kbID {
		return nil, datasource.ErrDataSourceNotFound
	}
	if ds.Type != types.ConnectorTypeGitHub {
		return nil, datasource.ErrDataSourceInvalid
	}
	config, err := ds.ParseConfig()
	if err != nil || config == nil {
		return nil, datasource.ErrInvalidConfig
	}
	storedPaths, err := previewStringList(config.Settings, "paths")
	if err != nil {
		return nil, datasource.ErrInvalidConfig
	}
	storedExclude, err := previewStringList(config.Settings, "exclude")
	if err != nil {
		return nil, datasource.ErrInvalidConfig
	}
	paths := append([]string(nil), storedPaths...)
	if req.Paths != nil {
		paths = append([]string(nil), (*req.Paths)...)
	}
	proposedExclude := append([]string(nil), storedExclude...)
	if req.Exclude != nil {
		proposedExclude = append([]string(nil), (*req.Exclude)...)
	}
	if !validPreviewPaths(paths) || len(proposedExclude) > githubScopePreviewMaxPaths {
		return nil, datasource.ErrInvalidConfig
	}
	// ParseConfig returns fresh settings. Clone nevertheless so this read path
	// cannot mutate a cached source config if the implementation changes later.
	config.Settings = maps.Clone(config.Settings)
	if config.Settings == nil {
		config.Settings = map[string]interface{}{}
	}
	config.Settings["paths"] = paths
	config.Settings["exclude"] = proposedExclude
	if err := snapshot.ValidateSettings(config); err != nil {
		return nil, datasource.ErrInvalidConfig
	}
	connector, err := s.connectorRegistry.Get(types.ConnectorTypeGitHub)
	if err != nil {
		return nil, datasource.ErrConnectorNotFound
	}
	previewer, ok := connector.(github.DocumentTreePreviewer)
	if !ok {
		return nil, datasource.ErrConnectorNotFound
	}
	resp := &types.GitHubDocumentScopePreview{
		SourceID: req.SourceID, TreeState: "error", StoredPaths: storedPaths,
		PreviewPaths: paths, PathsOverridden: req.Paths != nil && !slices.Equal(paths, storedPaths),
		FullRepository:  len(paths) == 0 || (len(paths) == 1 && paths[0] == ""),
		ProposedExclude: proposedExclude, ExcludeOverridden: req.Exclude != nil && !slices.Equal(proposedExclude, storedExclude),
		ExclusionsAppliedBySync: false, Warnings: []string{},
	}
	previewCtx, cancel := context.WithTimeout(ctx, githubScopePreviewTimeout)
	defer cancel()
	tree, err := previewer.PreviewDocumentTree(previewCtx, config)
	if err != nil {
		setGitHubPreviewError(resp, previewCtx, err)
		return resp, nil
	}
	resp.Repository, resp.Ref, resp.Commit = tree.Repository, tree.Ref, tree.Commit
	resp.TreeEntries = tree.TreeEntries
	resp.PreviewPaths = tree.Paths // normalized and sorted by the connector
	resp.FullRepository = len(tree.Paths) == 0 || (len(tree.Paths) == 1 && tree.Paths[0] == "")
	resp.TreeState = "complete"
	if tree.Truncated {
		resp.TreeState = "truncated"
		resp.Warnings = append(resp.Warnings, "tree_truncated_counts_are_lower_bounds")
	}
	if resp.FullRepository {
		resp.Warnings = append(resp.Warnings, "full_repository_selection")
	}
	if len(tree.MissingPaths) > 0 {
		resp.TreeState = "missing_path"
		resp.ErrorCode, resp.ErrorMessage = "github_selected_path_missing", "Selected GitHub path no longer exists; review source selection"
		resp.Warnings = append(resp.Warnings, "selected_path_missing")
	}
	resp.ActualSync = summarizeGitHubDocumentScope(tree.Files, nil)
	if len(proposedExclude) > 0 || req.Exclude != nil {
		proposed := summarizeGitHubDocumentScope(tree.Files, proposedExclude)
		resp.ProposedAfterExclude = &proposed
		resp.Warnings = append(resp.Warnings, "exclude_not_applied_by_current_sync")
	}
	return resp, nil
}

func previewStringList(settings map[string]interface{}, key string) ([]string, error) {
	if settings == nil || settings[key] == nil {
		return nil, nil
	}
	b, err := json.Marshal(settings[key])
	if err != nil {
		return nil, err
	}
	var values []string
	if err := json.Unmarshal(b, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func validPreviewPaths(paths []string) bool {
	if len(paths) > githubScopePreviewMaxPaths {
		return false
	}
	for _, p := range paths {
		if len(p) > 1024 || strings.ContainsAny(p, "\x00\r\n") {
			return false
		}
		if strings.TrimSpace(p) == "" && len(paths) > 1 {
			return false // a mixed empty root silently broadens to the whole repo
		}
	}
	return true // the connector applies its canonical safePath validation
}

func setGitHubPreviewError(resp *types.GitHubDocumentScopePreview, ctx context.Context, err error) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		resp.ErrorCode, resp.ErrorMessage = "github_preview_timeout", "GitHub scope preview timed out"
		return
	}
	if errors.Is(err, datasource.ErrInvalidConfig) {
		resp.ErrorCode, resp.ErrorMessage = "github_selection_invalid", "GitHub path selection is invalid"
		return
	}
	var githubErr *github.Error
	if errors.As(err, &githubErr) {
		resp.ErrorCode, resp.ErrorMessage = githubErr.Code, githubErr.Message
		if githubErr.Code == "github_selected_path_missing" {
			resp.TreeState = "missing_path"
		}
		return
	}
	resp.ErrorCode, resp.ErrorMessage = "github_preview_unavailable", "GitHub scope preview is unavailable"
}

func summarizeGitHubDocumentScope(files []github.DocumentPreviewFile, exclude []string) types.GitHubDocumentPreviewSummary {
	result := types.GitHubDocumentPreviewSummary{
		Extensions: []types.GitHubPreviewGroup{}, TopDirectories: []types.GitHubPreviewGroup{},
		SamplePaths: []string{}, Warnings: []string{},
	}
	extensions := make(map[string]types.GitHubPreviewGroup)
	directories := make(map[string]types.GitHubPreviewGroup)
	overflow := false
	for _, file := range files {
		if len(exclude) > 0 && snapshot.Excluded(file.Path, exclude) {
			continue
		}
		bytes := max(file.Size, 0)
		result.CandidateFiles++
		overflow = addPreviewBytes(&result.CandidateBytes, bytes) || overflow
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(file.Path)), ".")
		if ext == "" {
			ext = "(none)"
		}
		group := extensions[ext]
		group.Name, group.Files = ext, group.Files+1
		overflow = addPreviewBytes(&group.Bytes, bytes) || overflow
		extensions[ext] = group
		dir := "(root)"
		if slash := strings.IndexByte(file.Path, '/'); slash >= 0 {
			dir = file.Path[:slash]
		}
		group = directories[dir]
		group.Name, group.Files = dir, group.Files+1
		overflow = addPreviewBytes(&group.Bytes, bytes) || overflow
		directories[dir] = group
		if ext == "png" || ext == "jpg" || ext == "jpeg" || ext == "webp" {
			result.ImageFiles++
			overflow = addPreviewBytes(&result.ImageBytes, bytes) || overflow
		}
		if !isSupportedImportExtension(ext) {
			result.ParserUnsupportedFiles++
			overflow = addPreviewBytes(&result.ParserUnsupportedBytes, bytes) || overflow
		}
		if file.Size < 0 || file.Size > github.DocumentFileLimitBytes {
			result.TooLargeFiles++
			overflow = addPreviewBytes(&result.TooLargeBytes, bytes) || overflow
		}
		if isSupportedImportExtension(ext) && file.Size >= 0 && file.Size <= github.DocumentFileLimitBytes {
			result.EligibleFiles++
			overflow = addPreviewBytes(&result.EligibleBytes, bytes) || overflow
		}
		result.SamplePaths = append(result.SamplePaths, file.Path)
	}
	sort.Strings(result.SamplePaths)
	if len(result.SamplePaths) > githubScopePreviewMaxSample {
		result.SamplePaths = result.SamplePaths[:githubScopePreviewMaxSample]
	}
	result.Extensions = boundedGitHubPreviewGroups(extensions)
	result.TopDirectories = boundedGitHubPreviewGroups(directories)
	if result.CandidateFiles > github.DocumentFileCountLimit {
		result.Warnings = append(result.Warnings, "document_count_limit")
	}
	if result.TooLargeFiles > 0 {
		result.Warnings = append(result.Warnings, "document_file_limit")
	}
	if result.EligibleBytes > github.DocumentBatchLimitBytes {
		result.Warnings = append(result.Warnings, "batch_limit_if_all_changed")
	}
	if result.ParserUnsupportedFiles > 0 {
		result.Warnings = append(result.Warnings, "parser_unsupported", "current_sync_will_fail_unsupported")
	}
	if overflow {
		result.Warnings = append(result.Warnings, "size_overflow")
	}
	return result
}

func addPreviewBytes(total *int64, n int64) bool {
	if n > math.MaxInt64-*total {
		*total = math.MaxInt64
		return true
	}
	*total += n
	return false
}

func boundedGitHubPreviewGroups(values map[string]types.GitHubPreviewGroup) []types.GitHubPreviewGroup {
	groups := make([]types.GitHubPreviewGroup, 0, len(values))
	for _, group := range values {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Files != groups[j].Files {
			return groups[i].Files > groups[j].Files
		}
		return groups[i].Name < groups[j].Name
	})
	if len(groups) > githubScopePreviewMaxGroup {
		other := types.GitHubPreviewGroup{Name: "(other)"}
		for _, group := range groups[githubScopePreviewMaxGroup:] {
			other.Files += group.Files
			addPreviewBytes(&other.Bytes, group.Bytes)
		}
		groups = append(groups[:githubScopePreviewMaxGroup], other)
	}
	return groups
}
