package github

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

// documentGitCache reads the same immutable Git objects used by source-mode
// snapshots, but keeps the transport inside this document data source's own
// private namespace. Document and source projections never share an index,
// manifest, credential, or cache directory. A shared transport needs an
// explicit ownership/lease model before it can safely replace this boundary.
func (c *Connector) documentGitCache(ctx context.Context, cfg *types.DataSourceConfig, s selection, commit string) (*gitCache, []entry, error) {
	identity := cfg.SyncSource
	if identity == nil || identity.TenantID == 0 || identity.KnowledgeBaseID == "" || identity.DataSourceID == "" {
		return nil, nil, &Error{Code: "github_git_cache_unavailable", Message: "GitHub document cache has no trusted data-source identity"}
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil, &Error{Code: "github_git_cache_unavailable", Message: "GitHub document sync requires Git on this server"}
	}
	store, err := snapshot.FromEnvironment()
	if err != nil {
		return nil, nil, &Error{Code: "github_git_cache_unavailable", Message: "GitHub document sync requires a private persistent cache directory"}
	}
	ds := &types.DataSource{TenantID: identity.TenantID, KnowledgeBaseID: identity.KnowledgeBaseID, ID: identity.DataSourceID}
	root, err := store.PrivateDirectory(ds, "git")
	if err != nil {
		return nil, nil, &Error{Code: "github_git_cache_unavailable", Message: "GitHub document cache could not be prepared"}
	}
	cache := &gitCache{dir: filepath.Join(root, repositoryCacheKey(s.Repository)), remote: "https://github.com/" + s.Repository + ".git", token: token(cfg)}
	if err := cache.fetchCommit(ctx, commit); err != nil {
		return nil, nil, err
	}
	if size, sizeErr := directorySize(cache.dir, githubGitCacheLimit()+1); sizeErr != nil {
		return nil, nil, &Error{Code: "github_git_cache_unavailable", Message: "GitHub document cache cannot be inspected"}
	} else if size > githubGitCacheLimit() {
		_ = os.RemoveAll(cache.dir)
		return nil, nil, &Error{Code: "github_cache_limit", Message: "GitHub document cache exceeds its per-source limit; select narrower paths"}
	}
	gitRoots := s.Paths
	for _, root := range s.Paths {
		if root == "" {
			// Legacy settings may contain an explicit empty root. The document
			// selector treats that as the whole repository, not an empty Git
			// pathspec (which would hide every file and suggest false deletions).
			gitRoots = nil
			break
		}
	}
	localEntries, err := cache.tree(ctx, commit, gitRoots...)
	if err != nil {
		return nil, nil, err
	}
	entries := make([]entry, 0, len(localEntries))
	found := make(map[string]bool, len(s.Paths))
	for _, e := range localEntries {
		entries = append(entries, entry{Path: e.Path, Type: "blob", Mode: e.Mode, SHA: e.SHA, Size: e.Size})
		for _, root := range s.Paths {
			if e.Path == root || strings.HasPrefix(e.Path, root+"/") {
				found[root] = true
			}
		}
	}
	// A directory containing only submodules has no ordinary blobs. Check the
	// selected Git path itself before deciding it disappeared upstream.
	for _, root := range s.Paths {
		if root != "" && !found[root] {
			if _, pathErr := cache.run(ctx, "cat-file", "-e", commit+":"+root); pathErr != nil {
				return nil, nil, &Error{Code: "github_selected_path_missing", Message: "Selected GitHub path no longer exists; review source selection"}
			}
		}
	}
	return cache, entries, nil
}
