// Package github syncs repository documents through the native data source pipeline.
// Source-code browsing is a separate capability; this connector never imports code as text.
package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

const apiBase = "https://api.github.com"
const maxFileBytes = 16 << 20
const maxBatchBytes = 64 << 20

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type Connector struct {
	http        *http.Client
	apiBase     string
	syncGate    chan struct{}
	useGitCache bool
}

func NewConnector() *Connector {
	c := datasource.NewConnectorHTTPClient(30 * time.Second)
	// Credentials are scoped to api.github.com. Do not follow redirects, even
	// public-to-public redirects accepted by the generic SSRF client.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Connector{http: c, apiBase: apiBase, syncGate: make(chan struct{}, githubSyncConcurrency()), useGitCache: true}
}

// NewConnectorWithHTTPClient constructs the same restricted GitHub connector
// with an injected transport/base URL. It is primarily useful for isolated
// integration tests; callers still cannot change endpoint or credentials from
// a user-facing data-source request.
func NewConnectorWithHTTPClient(client *http.Client, base string) *Connector {
	c := NewConnector()
	if client != nil {
		c.http = client
	}
	if strings.TrimSpace(base) != "" {
		c.apiBase = strings.TrimRight(strings.TrimSpace(base), "/")
	}
	return c
}
func (*Connector) Type() string { return types.ConnectorTypeGitHub }

func githubSyncConcurrency() int {
	const defaultConcurrency = 2
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("DATASOURCE_GITHUB_MAX_CONCURRENT")))
	if err != nil || v < 1 {
		return defaultConcurrency
	}
	if v > 8 {
		return 8
	}
	return v
}

func (c *Connector) withSyncGate(ctx context.Context, fn func() error) error {
	if c == nil || c.syncGate == nil {
		return fn()
	}
	select {
	case c.syncGate <- struct{}{}:
		defer func() { <-c.syncGate }()
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// VerifyCommit is used by approved local mirrors before emitting a remote
// citation. A local-only commit or inaccessible private repository gets no URL.
func VerifyCommit(ctx context.Context, repository, commit string, credentials map[string]interface{}) bool {
	cfg := &types.DataSourceConfig{Credentials: credentials, Settings: map[string]interface{}{"repository": repository, "ref": commit}}
	s, err := parseSelection(cfg)
	if err != nil {
		return false
	}
	if token(cfg) == "" {
		return NewConnector().publicCommitExists(ctx, s.Repository, commit)
	}
	actual, _, err := NewConnector().head(ctx, cfg, s)
	return err == nil && actual == commit
}

type selection struct {
	Repository string   `json:"repository"`
	Ref        string   `json:"ref"`
	Paths      []string `json:"paths"`
}
type entry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}
type tree struct {
	Tree      []entry `json:"tree"`
	Truncated bool    `json:"truncated"`
}
type cursor struct {
	Selection string           `json:"selection"`
	Commit    string           `json:"commit"`
	Files     map[string]entry `json:"files"`
}

func parseSelection(cfg *types.DataSourceConfig) (selection, error) {
	var s selection
	if cfg == nil {
		return s, datasource.ErrInvalidConfig
	}
	b, err := json.Marshal(cfg.Settings)
	if err != nil {
		return s, datasource.ErrInvalidConfig
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, datasource.ErrInvalidConfig
	}
	s.Repository = normalizeGitHubRepository(s.Repository)
	s.Ref = strings.TrimSpace(s.Ref)
	if !repoPattern.MatchString(s.Repository) || strings.HasSuffix(s.Repository, "/.") || strings.HasSuffix(s.Repository, "/..") {
		return s, fmt.Errorf("%w: repository must be owner/name or a GitHub repository URL", datasource.ErrInvalidConfig)
	}
	if len(s.Ref) > 256 || strings.ContainsAny(s.Ref, "\x00\r\n") {
		return s, datasource.ErrInvalidConfig
	}
	for i, p := range s.Paths {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p != "" && !safePath(p) {
			return s, fmt.Errorf("%w: invalid repository path", datasource.ErrInvalidConfig)
		}
		s.Paths[i] = p
	}
	sort.Strings(s.Paths)
	return s, nil
}

// normalizeGitHubRepository accepts the repository URL forms that earlier
// datasource versions persisted. The connector still validates the resulting
// owner/name pair below, so paths such as /tree/main never become a source.
func normalizeGitHubRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	lower := strings.ToLower(repository)
	for _, prefix := range []string{
		"https://github.com/",
		"http://github.com/",
		"https://www.github.com/",
		"http://www.github.com/",
		"github.com/",
		"git@github.com:",
		"git@github.com/",
		"ssh://git@github.com/",
	} {
		if strings.HasPrefix(lower, prefix) {
			repository = repository[len(prefix):]
			break
		}
	}
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	if len(repository) >= len(".git") && strings.EqualFold(repository[len(repository)-len(".git"):], ".git") {
		repository = repository[:len(repository)-len(".git")]
	}
	return repository
}

// ConfiguredRepository returns the canonical GitHub owner/repository identity after the
// same validation used by sync and source snapshots. It exposes no credential,
// ref or path data and lets other scoped capabilities avoid maintaining a
// second parser for repository URLs.
func ConfiguredRepository(cfg *types.DataSourceConfig) (string, bool) {
	s, err := parseSelection(cfg)
	if err != nil {
		return "", false
	}
	return s.Repository, true
}
func safePath(p string) bool {
	return p != "" && p != "." && p != ".." && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") && path.Clean(p) == p && !strings.ContainsAny(p, "\\\x00\r\n")
}
func token(cfg *types.DataSourceConfig) string {
	if cfg == nil {
		return ""
	}
	s, _ := cfg.Credentials["access_token"].(string)
	return strings.TrimSpace(s)
}
func (c *Connector) get(ctx context.Context, cfg *types.DataSourceConfig, endpoint string, out interface{}, limit int64) error {
	_, err := c.getWithHeaders(ctx, cfg, endpoint, out, limit)
	return err
}

func (c *Connector) getWithHeaders(ctx context.Context, cfg *types.DataSourceConfig, endpoint string, out interface{}, limit int64) (http.Header, error) {
	base := apiBase
	if c != nil && c.apiBase != "" {
		base = c.apiBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+endpoint, nil)
	if err != nil {
		return nil, &Error{Code: "github_request", Message: "GitHub request is invalid"}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "octo-weknora")
	if t := token(cfg); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Code: "github_connection", Message: "GitHub connection failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.Header, githubHTTPError(resp)
	}
	stage := githubResponseStage(endpoint)
	if resp.ContentLength > limit {
		return resp.Header, &Error{Code: "github_response_limit", Stage: stage, LimitBytes: limit,
			Message: fmt.Sprintf("GitHub %s response exceeds the %d-byte sync limit", stage, limit)}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if ctx.Err() != nil {
		return resp.Header, ctx.Err()
	}
	if int64(len(b)) > limit {
		return resp.Header, &Error{Code: "github_response_limit", Stage: stage, BytesRead: int64(len(b)), LimitBytes: limit,
			Message: fmt.Sprintf("GitHub %s response exceeds the %d-byte sync limit (read %d bytes)", stage, limit, len(b))}
	}
	if err != nil || (resp.ContentLength > 0 && int64(len(b)) != resp.ContentLength) {
		return resp.Header, &Error{Code: "github_response_incomplete", Stage: stage, BytesRead: int64(len(b)), LimitBytes: limit,
			Message: fmt.Sprintf("GitHub %s response ended before completion (read %d bytes); retry later", stage, len(b))}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return resp.Header, &Error{Code: "github_response_invalid", Message: "GitHub response is invalid"}
	}
	return resp.Header, nil
}

// Only fixed stage labels are exposed in sync diagnostics, never API paths.
func githubResponseStage(endpoint string) string {
	switch {
	case strings.Contains(endpoint, "/git/blobs/"):
		return "blob"
	case strings.Contains(endpoint, "/git/trees/"):
		return "tree"
	default:
		return "metadata"
	}
}

// A document blob is an immutable Git object. A bounded retry of transient
// transport/server failures is safe; malformed content and configured size
// limits are deliberately not retried. Long rate-limit windows are surfaced
// to the scheduler instead of occupying a sync worker.
func (c *Connector) getDocumentBlob(ctx context.Context, cfg *types.DataSourceConfig, endpoint string, out interface{}) error {
	const attempts = 3
	const maxWait = 2 * time.Second
	for attempt := 1; attempt <= attempts; attempt++ {
		err := c.get(ctx, cfg, endpoint, out, 24<<20)
		if err == nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		var apiErr *Error
		if !errors.As(err, &apiErr) || !transientBlobError(apiErr) || attempt == attempts {
			return err
		}
		wait := time.Duration(100<<(attempt-1)) * time.Millisecond
		if apiErr.RetryAfter != nil {
			if hinted := time.Until(*apiErr.RetryAfter); hinted > wait {
				wait = hinted
			}
		}
		if wait > maxWait {
			return err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func transientBlobError(err *Error) bool {
	switch err.Code {
	case "github_connection", "github_response_incomplete", "github_secondary_rate_limit":
		return true
	case "github_rate_limit":
		return err.RetryAfter != nil
	case "github_http":
		return err.StatusCode == http.StatusRequestTimeout || err.StatusCode == http.StatusTooManyRequests || err.StatusCode >= 500
	case "github_forbidden":
		return err.StatusCode == http.StatusTooManyRequests
	default:
		return false
	}
}
func (c *Connector) Validate(ctx context.Context, cfg *types.DataSourceConfig) error {
	if err := snapshot.ValidateSettings(cfg); err != nil {
		return err
	}
	if cfg != nil && snapshot.IsSource(cfg) {
		if _, err := snapshot.FromEnvironment(); err != nil {
			return err
		}
	}
	if cfg == nil {
		return datasource.ErrInvalidConfig
	}
	if strings.ContainsAny(token(cfg), "\r\n\x00") {
		return datasource.ErrInvalidConfig
	}
	if _, ok := cfg.Settings["repository"]; ok {
		s, err := parseSelection(cfg)
		if err != nil {
			return err
		}
		if snapshot.IsSource(cfg) && token(cfg) == "" {
			_, err = c.publicHead(ctx, s)
			return err
		}
		_, _, err = c.head(ctx, cfg, s)
		return err
	}
	endpoint := "/rate_limit"
	if token(cfg) != "" {
		endpoint = "/user"
	}
	var result map[string]interface{}
	return c.get(ctx, cfg, endpoint, &result, 1<<20)
}
func (c *Connector) head(ctx context.Context, cfg *types.DataSourceConfig, s selection) (string, string, error) {
	var repo struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.get(ctx, cfg, "/repos/"+s.Repository, &repo, 1<<20); err != nil {
		return "", "", err
	}
	if !strings.EqualFold(repo.FullName, s.Repository) {
		return "", "", fmt.Errorf("GitHub repository identity mismatch")
	}
	ref := s.Ref
	if ref == "" {
		ref = repo.DefaultBranch
	}
	if ref == "" {
		return "", "", fmt.Errorf("GitHub repository has no branch")
	}
	var commit struct {
		SHA    string `json:"sha"`
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if err := c.get(ctx, cfg, "/repos/"+s.Repository+"/commits/"+url.PathEscape(ref), &commit, 2<<20); err != nil {
		return "", "", err
	}
	if !shaPattern.MatchString(commit.SHA) || !shaPattern.MatchString(commit.Commit.Tree.SHA) {
		return "", "", fmt.Errorf("GitHub commit identity is invalid")
	}
	return commit.SHA, commit.Commit.Tree.SHA, nil
}
func (c *Connector) entries(ctx context.Context, cfg *types.DataSourceConfig, repository, sha string) ([]entry, error) {
	var t tree
	if err := c.get(ctx, cfg, "/repos/"+repository+"/git/trees/"+sha+"?recursive=1", &t, 8<<20); err != nil {
		return nil, err
	}
	// A partial tree must never drive deletion detection. Fail explicitly;
	// very large repositories can be split or get a paged-tree implementation later.
	if t.Truncated {
		return nil, &Error{Code: "github_tree_truncated", Message: "GitHub repository tree is truncated; sync stopped without advancing its cursor"}
	}
	for _, e := range t.Tree {
		if !safePath(e.Path) || !shaPattern.MatchString(e.SHA) {
			return nil, &Error{Code: "github_tree_invalid", Message: "GitHub repository tree contains an invalid path or object ID"}
		}
	}
	return t.Tree, nil
}
func (c *Connector) ListResources(ctx context.Context, cfg *types.DataSourceConfig, parent string) ([]types.Resource, error) {
	s, err := parseSelection(cfg)
	if err != nil {
		return nil, err
	}
	_, sha, err := c.head(ctx, cfg, s)
	if err != nil {
		return nil, err
	}
	es, err := c.entries(ctx, cfg, s.Repository, sha)
	if err != nil {
		return nil, err
	}
	out := []types.Resource{}
	for _, e := range es {
		dir := path.Dir(e.Path)
		if dir == "." {
			dir = ""
		}
		if e.Type == "tree" && dir == parent {
			out = append(out, types.Resource{ExternalID: e.Path, Name: path.Base(e.Path), Type: "directory", ParentID: parent, HasChildren: true})
		}
	}
	return out, nil
}
func (*Connector) ResolveResourceAncestors(context.Context, *types.DataSourceConfig, []string) ([]string, error) {
	return []string{}, nil
}
func (c *Connector) FetchAll(ctx context.Context, cfg *types.DataSourceConfig, _ []string) ([]types.FetchedItem, error) {
	items, _, err := c.FetchIncremental(ctx, cfg, nil)
	return items, err
}

// allowedDocument deliberately excludes source code, dotfiles, dependencies,
// archives, symlinks and submodules. Other file modes need explicit support.
func allowedDocument(e entry) bool {
	if e.Type != "blob" || (e.Mode != "100644" && e.Mode != "100755") {
		return false
	}
	for _, part := range strings.Split(e.Path, "/") {
		if strings.HasPrefix(part, ".") || part == "node_modules" || part == "vendor" || part == "dist" {
			return false
		}
	}
	switch strings.ToLower(path.Ext(e.Path)) {
	case ".md", ".markdown", ".mdx", ".txt", ".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx", ".csv", ".html", ".htm", ".epub", ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}
func selected(p string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	for _, root := range roots {
		if root == "" || p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}
func (c *Connector) FetchIncremental(ctx context.Context, cfg *types.DataSourceConfig, old *types.SyncCursor) ([]types.FetchedItem, *types.SyncCursor, error) {
	var items []types.FetchedItem
	var next *types.SyncCursor
	err := c.withSyncGate(ctx, func() error {
		var err error
		items, next, err = c.fetchIncremental(ctx, cfg, old)
		return err
	})
	return items, next, err
}

func (c *Connector) fetchIncremental(ctx context.Context, cfg *types.DataSourceConfig, old *types.SyncCursor) ([]types.FetchedItem, *types.SyncCursor, error) {
	if snapshot.IsSource(cfg) {
		return nil, nil, fmt.Errorf("%w: source mode must use the snapshot pipeline", datasource.ErrInvalidConfig)
	}
	s, err := parseSelection(cfg)
	if err != nil {
		return nil, nil, err
	}
	commit, treeSHA, err := c.head(ctx, cfg, s)
	if err != nil {
		return nil, nil, err
	}
	es, err := c.entries(ctx, cfg, s.Repository, treeSHA)
	if err != nil {
		return nil, nil, err
	}
	selectionJSON, _ := json.Marshal(s)
	h := sha256.Sum256(selectionJSON)
	key := hex.EncodeToString(h[:])
	prev := cursor{}
	if old != nil {
		b, _ := json.Marshal(old.ConnectorCursor)
		if json.Unmarshal(b, &prev) != nil {
			return nil, nil, fmt.Errorf("GitHub sync cursor is invalid")
		}
	}
	all := map[string]entry{}
	files := map[string]entry{}
	for _, e := range es {
		all[e.Path] = e
		if allowedDocument(e) && selected(e.Path, s.Paths) {
			files[e.Path] = e
		}
	}
	for _, root := range s.Paths {
		if root != "" {
			if _, ok := all[root]; !ok {
				return nil, nil, &Error{Code: "github_selected_path_missing", Message: "Selected GitHub path no longer exists; review source selection"}
			}
		}
	}
	if len(files) > 2000 {
		return nil, nil, &Error{Code: "github_documents_limit", Message: "GitHub document source exceeds 2000 files; narrow the selected paths or use read-only source mode"}
	}
	next := cursor{Selection: key, Commit: commit, Files: files}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	items := []types.FetchedItem{}
	bytesRead := 0
	for _, p := range paths {
		e := files[p]
		if prev.Selection == key && prev.Files[p].SHA == e.SHA {
			continue
		}
		if e.Size < 0 || e.Size > maxFileBytes {
			return nil, nil, &Error{Code: "github_document_file_limit", Message: "A GitHub document exceeds the 16 MiB file limit; narrow the selected paths"}
		}
		var blob struct {
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
			SHA      string `json:"sha"`
		}
		if err := c.getDocumentBlob(ctx, cfg, "/repos/"+s.Repository+"/git/blobs/"+e.SHA, &blob); err != nil {
			return nil, nil, err
		}
		if blob.Encoding != "base64" || blob.SHA != e.SHA {
			return nil, nil, fmt.Errorf("GitHub blob identity or encoding is invalid")
		}
		body, err := base64.StdEncoding.DecodeString(blob.Content)
		if err != nil || len(body) > maxFileBytes {
			return nil, nil, &Error{Code: "github_document_invalid", Message: "GitHub document content is invalid or incomplete"}
		}
		bytesRead += len(body)
		if bytesRead > maxBatchBytes {
			return nil, nil, &Error{Code: "github_documents_batch_limit", Message: "GitHub document sync exceeds the 64 MiB batch limit; narrow the selected paths"}
		}
		parts := strings.Split(p, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		id := "github:" + strings.ToLower(s.Repository) + ":" + s.Ref + ":" + p
		items = append(items, types.FetchedItem{ExternalID: id, Title: p, FileName: path.Join(s.Repository, p), Content: body, SourceResourceID: s.Repository,
			Metadata: map[string]string{"channel": "github", "source_type": "github", "github_repository": s.Repository, "github_ref": s.Ref, "github_commit": commit, "github_blob_sha": e.SHA, "github_path": p, "github_url": "https://github.com/" + s.Repository + "/blob/" + commit + "/" + strings.Join(parts, "/")}})
	}
	// A changed selection is not proof of remote deletion. Only a complete
	// same-selection tree can report a previously imported path as deleted.
	if prev.Selection == key {
		deleted := []string{}
		for p := range prev.Files {
			if _, exists := all[p]; !exists {
				deleted = append(deleted, p)
			}
		}
		sort.Strings(deleted)
		for _, p := range deleted {
			items = append(items, types.FetchedItem{ExternalID: "github:" + strings.ToLower(s.Repository) + ":" + s.Ref + ":" + p, IsDeleted: true})
		}
	}
	return items, &types.SyncCursor{ConnectorCursor: map[string]interface{}{"selection": next.Selection, "commit": next.Commit, "files": next.Files}}, nil
}
