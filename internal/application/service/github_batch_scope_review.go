package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	githubconnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

const githubBatchScopeReviewTTL = 45 * time.Minute
const githubBatchScopeReviewTimeout = 12 * time.Second

// A request previews exactly one selected repository. This gate bounds live
// GitHub tree reads even when several browser tabs submit explicit batches.
var githubBatchScopeReviewGate = make(chan struct{}, 2)

type githubBatchScopeReviewToken struct {
	Version          int    `json:"v"`
	TenantID         uint64 `json:"t"`
	KnowledgeBaseID  string `json:"k"`
	PrincipalDigest  string `json:"a"`
	RepositoryDigest string `json:"r"`
	Mode             string `json:"m"`
	Selection        string `json:"s"`
	Credential       string `json:"c"`
	Commit           string `json:"h"`
	EligibleFiles    int    `json:"n"`
	ExpiresAtUnix    int64  `json:"e"`
}

func (s *DataSourceService) PreviewGitHubBatchScope(
	ctx context.Context, req *types.GitHubBatchScopePreviewRequest,
) (*types.GitHubBatchScopePreviewResponse, error) {
	if req == nil || req.TenantID == 0 || req.KnowledgeBaseID == "" ||
		!githubconnector.ValidOwner(req.Owner) || requestedGitHubBatchMode(req.Mode) == "" {
		return nil, datasource.ErrInvalidConfig
	}
	// Keep GitHub's display casing in settings: the reviewed create path stores
	// the candidate's casing, and the signed selection must match those bytes.
	// Identity comparisons below still use the canonical case-insensitive form.
	repository := strings.TrimSuffix(strings.TrimSpace(req.Repository), ".git")
	if canonicalGitHubRepository(repository) == "" || !belongsToOwner(repository, req.Owner) || strings.TrimSpace(req.Ref) == "" {
		return nil, datasource.ErrInvalidConfig
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, req.KnowledgeBaseID)
	if err != nil || kb == nil || kb.TenantID != req.TenantID {
		return nil, datasource.ErrKnowledgeBaseNotFound
	}
	if dataSourceDeletePrincipalSubject(ctx) == "" || len(secutils.SystemHMACKey()) == 0 {
		return nil, errors.New("GitHub batch scope preview signing is unavailable")
	}
	candidate := types.GitHubRepositoryCandidate{Repository: repository, DefaultBranch: req.Ref,
		Paths: req.Paths, Exclude: req.Exclude}
	settings := githubBatchEffectiveSettings(repository, strings.TrimSpace(req.Ref), req.Mode, nil, nil, candidate, true)
	config := &types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: settings, Credentials: req.Credentials}
	config.StripNonSecretCredentials(types.ConnectorTypeGitHub)
	if err := snapshot.ValidateSettings(config); err != nil {
		return nil, datasource.ErrInvalidConfig
	}
	// Document previews validate globs in documentExcludes. Source snapshots
	// otherwise ignore malformed patterns, which would make a reviewed exclusion
	// look effective even though the later sync cannot apply it.
	if req.Mode == "source" {
		if rules, ok := settings["exclude"].([]string); ok {
			for _, rule := range rules {
				if _, err := path.Match(rule, ""); err != nil {
					return nil, datasource.ErrInvalidConfig
				}
			}
		}
	}
	if _, valid := githubconnector.ConfiguredRepository(config); !valid {
		return nil, datasource.ErrInvalidConfig
	}
	connector, err := s.connectorRegistry.Get(types.ConnectorTypeGitHub)
	if err != nil {
		return nil, datasource.ErrConnectorNotFound
	}
	github, ok := connector.(*githubconnector.Connector)
	if !ok {
		return nil, datasource.ErrConnectorNotFound
	}
	paths, _ := settings["paths"].([]string)
	excludes, _ := settings["exclude"].([]string)
	_, visiblePaths, visibleExcludes, _ := redactGitHubPreviewPaths(nil, paths, excludes)
	response := &types.GitHubBatchScopePreviewResponse{
		Repository: repository, Ref: strings.TrimSpace(req.Ref), Mode: req.Mode, TreeState: "error",
		Paths: visiblePaths, Exclude: visibleExcludes, Warnings: []string{},
	}
	previewCtx, cancel := context.WithTimeout(ctx, githubBatchScopeReviewTimeout)
	defer cancel()
	select {
	case githubBatchScopeReviewGate <- struct{}{}:
		defer func() { <-githubBatchScopeReviewGate }()
	case <-previewCtx.Done():
		response.ErrorCode, response.ErrorMessage = "github_preview_busy", "GitHub scope preview is busy or timed out; retry this repository"
		return response, nil
	}
	if req.Mode == "documents" {
		tree, fetchErr := github.PreviewDocumentTree(previewCtx, config)
		if fetchErr != nil {
			setGitHubBatchScopeError(response, previewCtx, fetchErr)
			return response, nil
		}
		response.Commit = tree.Commit
		response.Summary = summarizeGitHubDocumentScope(tree.Files, excludes)
		response.TreeState = reviewedGitHubTreeState(tree.Truncated, tree.MissingPaths)
		response.Warnings = append(response.Warnings, response.Summary.Warnings...)
		if response.Summary.ParserUnsupportedFiles > 0 || response.Summary.TooLargeFiles > 0 ||
			response.Summary.CandidateFiles > githubconnector.DocumentFileCountLimit {
			response.ErrorCode = "github_scope_exceeds_import_policy"
			response.ErrorMessage = "Selected documents include unsupported, oversized, or too many files; narrow paths or exclusions before creation"
		}
	} else {
		tree, fetchErr := github.PreviewSourceTree(previewCtx, config)
		if fetchErr != nil {
			setGitHubBatchScopeError(response, previewCtx, fetchErr)
			return response, nil
		}
		response.Commit = tree.Commit
		response.Estimated = true
		response.Summary = summarizeGitHubSourceScope(tree.Files, snapshot.Excludes(config))
		response.TreeState = reviewedGitHubTreeState(tree.Truncated, tree.MissingPaths)
		response.Warnings = append(response.Warnings, "source_text_is_estimated")
		response.Warnings = append(response.Warnings, response.Summary.Warnings...)
		if response.Summary.EligibleFiles > snapshot.MaxFiles || response.Summary.EligibleBytes > snapshot.MaxTotalBytes {
			response.ErrorCode = "github_scope_exceeds_source_policy"
			response.ErrorMessage = "Selected source exceeds its file or text budget; narrow paths or exclusions before creation"
		}
	}
	if response.TreeState != "complete" {
		response.ErrorCode = "github_scope_incomplete"
		response.ErrorMessage = "GitHub tree is incomplete or a selected path is missing; preview again after correcting the selection"
		return response, nil
	}
	if response.ErrorCode != "" {
		return response, nil
	}
	if response.Summary.EligibleFiles == 0 {
		response.Warnings = append(response.Warnings, "empty_source_requires_confirmation")
	}
	expires := time.Now().UTC().Add(githubBatchScopeReviewTTL)
	response.PreviewToken, err = signGitHubBatchScopeReview(ctx, req.TenantID, req.KnowledgeBaseID,
		repository, req.Mode, config, response.Commit, response.Summary.EligibleFiles, expires)
	if err != nil {
		return nil, err
	}
	response.ExpiresAt = expires.Format(time.RFC3339)
	return response, nil
}

func reviewedGitHubTreeState(truncated bool, missing []string) string {
	if truncated {
		return "truncated"
	}
	if len(missing) > 0 {
		return "missing_path"
	}
	return "complete"
}

func setGitHubBatchScopeError(resp *types.GitHubBatchScopePreviewResponse, ctx context.Context, err error) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		resp.ErrorCode, resp.ErrorMessage = "github_preview_timeout", "GitHub scope preview timed out"
		return
	}
	var githubErr *githubconnector.Error
	if errors.As(err, &githubErr) {
		resp.ErrorCode, resp.ErrorMessage = githubErr.Code, githubErr.Message
		return
	}
	resp.ErrorCode, resp.ErrorMessage = "github_preview_unavailable", "GitHub scope preview is unavailable"
}

func summarizeGitHubSourceScope(files []githubconnector.SourcePreviewFile, excludes []string) types.GitHubDocumentPreviewSummary {
	summary := types.GitHubDocumentPreviewSummary{Extensions: []types.GitHubPreviewGroup{},
		TopDirectories: []types.GitHubPreviewGroup{}, SamplePaths: []string{}, Warnings: []string{}}
	extensions := map[string]types.GitHubPreviewGroup{}
	directories := map[string]types.GitHubPreviewGroup{}
	for _, file := range files {
		if file.Sensitive {
			summary.SensitiveCandidateFiles++
			addPreviewBytes(&summary.SensitiveCandidateBytes, max(file.Size, 0))
			continue
		}
		if snapshot.Excluded(file.Path, excludes) {
			summary.UserExcludedFiles++
			addPreviewBytes(&summary.UserExcludedBytes, max(file.Size, 0))
			continue
		}
		bytes := max(file.Size, 0)
		summary.CandidateFiles++
		addPreviewBytes(&summary.CandidateBytes, bytes)
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(file.Path)), ".")
		if ext == "" {
			ext = "(none)"
		}
		group := extensions[ext]
		group.Name, group.Files = ext, group.Files+1
		addPreviewBytes(&group.Bytes, bytes)
		extensions[ext] = group
		dir := "(root)"
		if slash := strings.IndexByte(file.Path, '/'); slash >= 0 {
			dir = file.Path[:slash]
		}
		group = directories[dir]
		group.Name, group.Files = dir, group.Files+1
		addPreviewBytes(&group.Bytes, bytes)
		directories[dir] = group
		if file.Mode != "100644" && file.Mode != "100755" {
			summary.ParserUnsupportedFiles++
			continue
		}
		if file.Size < 0 || file.Size > snapshot.MaxFileBytes {
			summary.TooLargeFiles++
			addPreviewBytes(&summary.TooLargeBytes, bytes)
			continue
		}
		summary.EligibleFiles++
		addPreviewBytes(&summary.EligibleBytes, bytes)
		summary.SamplePaths = append(summary.SamplePaths, file.Path)
	}
	sort.Strings(summary.SamplePaths)
	if len(summary.SamplePaths) > githubScopePreviewMaxSample {
		summary.SamplePaths = summary.SamplePaths[:githubScopePreviewMaxSample]
	}
	summary.Extensions = boundedGitHubPreviewGroups(extensions)
	summary.TopDirectories = boundedGitHubPreviewGroups(directories)
	if summary.TooLargeFiles > 0 {
		summary.Warnings = append(summary.Warnings, "source_files_too_large_skipped")
	}
	if summary.SensitiveCandidateFiles > 0 {
		summary.Warnings = append(summary.Warnings, "sensitive_candidate")
	}
	return summary
}

func githubBatchCredentialDigest(credentials map[string]interface{}, key []byte) string {
	token, _ := credentials["access_token"].(string)
	return githubBatchIdentityDigest(key, "credential", strings.TrimSpace(token))
}

func githubBatchIdentityDigest(key []byte, kind, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("github-batch-" + kind + "-v1\x00" + value))
	return hex.EncodeToString(mac.Sum(nil))
}

func signGitHubBatchScopeReview(
	ctx context.Context, tenantID uint64, kbID, repository, mode string,
	config *types.DataSourceConfig, commit string, eligible int, expires time.Time,
) (string, error) {
	key := secutils.SystemHMACKey()
	if len(key) == 0 {
		return "", errors.New("GitHub batch scope signing is unavailable")
	}
	payload := githubBatchScopeReviewToken{Version: 1, TenantID: tenantID, KnowledgeBaseID: kbID,
		PrincipalDigest:  githubBatchIdentityDigest(key, "principal", dataSourceDeletePrincipalSubject(ctx)),
		RepositoryDigest: githubBatchIdentityDigest(key, "repository", canonicalGitHubRepository(repository)), Mode: mode,
		Selection: snapshot.Selection(config), Credential: githubBatchCredentialDigest(config.Credentials, key),
		Commit: commit, EligibleFiles: eligible, ExpiresAtUnix: expires.Unix()}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyGitHubBatchScopeReview(
	ctx context.Context, req *types.GitHubBatchRequest, candidate types.GitHubRepositoryCandidate,
	config *types.DataSourceConfig,
) error {
	key := secutils.SystemHMACKey()
	if len(key) == 0 || candidate.PreviewToken == "" || len(candidate.PreviewToken) > 4096 {
		return errors.New("GitHub scope preview token is missing or unavailable")
	}
	parts := strings.Split(candidate.PreviewToken, ".")
	if len(parts) != 2 {
		return errors.New("GitHub scope preview token is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("GitHub scope preview token is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("GitHub scope preview token is invalid")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return errors.New("GitHub scope preview token signature is invalid")
	}
	var payload githubBatchScopeReviewToken
	if json.Unmarshal(raw, &payload) != nil || payload.Version != 1 || payload.ExpiresAtUnix <= time.Now().UTC().Unix() ||
		payload.TenantID != req.TenantID || payload.KnowledgeBaseID != req.KnowledgeBaseID ||
		dataSourceDeletePrincipalSubject(ctx) == "" || payload.PrincipalDigest == "" ||
		payload.PrincipalDigest != githubBatchIdentityDigest(key, "principal", dataSourceDeletePrincipalSubject(ctx)) ||
		payload.RepositoryDigest != githubBatchIdentityDigest(key, "repository", canonicalGitHubRepository(candidate.Repository)) || payload.Mode != req.Mode ||
		payload.Selection != snapshot.Selection(config) ||
		payload.Credential != githubBatchCredentialDigest(config.Credentials, key) ||
		payload.Commit == "" || payload.EligibleFiles < 0 ||
		(payload.EligibleFiles == 0 && !candidate.AllowEmpty) {
		return errors.New("GitHub scope preview no longer matches this repository and selection")
	}
	return nil
}
