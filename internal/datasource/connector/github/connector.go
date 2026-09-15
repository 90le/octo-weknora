// Package github syncs repository documents through the native data source pipeline.
// Source-code browsing is a separate capability; this connector never imports code as text.
package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
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

type Connector struct{ http *http.Client }

func NewConnector() *Connector {
	c := datasource.NewConnectorHTTPClient(30 * time.Second)
	// Credentials are scoped to api.github.com. Do not follow redirects, even
	// public-to-public redirects accepted by the generic SSRF client.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Connector{http: c}
}
func (*Connector) Type() string { return types.ConnectorTypeGitHub }

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
	s.Repository = strings.TrimSuffix(strings.TrimSpace(s.Repository), ".git")
	s.Repository = strings.TrimPrefix(s.Repository, "https://github.com/")
	s.Ref = strings.TrimSpace(s.Ref)
	if !repoPattern.MatchString(s.Repository) || strings.HasSuffix(s.Repository, "/.") || strings.HasSuffix(s.Repository, "/..") {
		return s, fmt.Errorf("%w: repository must be owner/name or an HTTPS GitHub repository URL", datasource.ErrInvalidConfig)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+endpoint, nil)
	if err != nil {
		return fmt.Errorf("GitHub request is invalid")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "octo-weknora")
	if t := token(cfg); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned HTTP %d; check repository access and rate limits", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return fmt.Errorf("GitHub response is incomplete or exceeds the sync limit")
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("GitHub response is invalid")
	}
	return nil
}
func (c *Connector) Validate(ctx context.Context, cfg *types.DataSourceConfig) error {
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
		return nil, fmt.Errorf("GitHub tree is truncated; sync stopped without advancing its cursor")
	}
	for _, e := range t.Tree {
		if !safePath(e.Path) || !shaPattern.MatchString(e.SHA) {
			return nil, fmt.Errorf("GitHub tree contains an invalid path or object ID")
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
				return nil, nil, fmt.Errorf("Selected GitHub path no longer exists; review source selection")
			}
		}
	}
	if len(files) > 2000 {
		return nil, nil, fmt.Errorf("GitHub source exceeds 2000 documents; select a smaller directory")
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
			return nil, nil, fmt.Errorf("GitHub document exceeds 16 MiB file limit: %s", p)
		}
		var blob struct {
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
			SHA      string `json:"sha"`
		}
		if err := c.get(ctx, cfg, "/repos/"+s.Repository+"/git/blobs/"+e.SHA, &blob, 24<<20); err != nil {
			return nil, nil, err
		}
		if blob.Encoding != "base64" || blob.SHA != e.SHA {
			return nil, nil, fmt.Errorf("GitHub blob identity or encoding is invalid")
		}
		body, err := base64.StdEncoding.DecodeString(blob.Content)
		if err != nil || len(body) > maxFileBytes {
			return nil, nil, fmt.Errorf("GitHub document content is invalid")
		}
		bytesRead += len(body)
		if bytesRead > maxBatchBytes {
			return nil, nil, fmt.Errorf("GitHub sync exceeds 64 MiB; select a smaller directory")
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
