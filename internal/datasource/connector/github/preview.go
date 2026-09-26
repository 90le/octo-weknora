package github

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// DocumentPreviewTree is tree metadata for the connector's current document
// selector. It contains no blob content or credentials and is never persisted.
type DocumentPreviewTree struct {
	Repository   string
	Ref          string
	Commit       string
	Paths        []string
	TreeEntries  int
	Truncated    bool
	MissingPaths []string
	Files        []DocumentPreviewFile
}

type DocumentPreviewFile struct {
	Path string
	Size int64
}

// These are the current document-sync product limits. Preview reports them as
// warnings without changing or attempting a source sync.
const (
	DocumentFileLimitBytes  = maxFileBytes
	DocumentBatchLimitBytes = maxBatchBytes
	DocumentFileCountLimit  = 2000 // keep in step with fetchIncremental's guard
)

// DocumentTreePreviewer is the optional connector capability used by the
// knowledge-base-scoped dry-run endpoint. It deliberately uses the REST tree
// API instead of the document sync's Git cache: preview never clones a repo,
// reads a blob, creates a cache, or advances a source cursor.
type DocumentTreePreviewer interface {
	PreviewDocumentTree(context.Context, *types.DataSourceConfig) (*DocumentPreviewTree, error)
}

func (c *Connector) PreviewDocumentTree(ctx context.Context, cfg *types.DataSourceConfig) (*DocumentPreviewTree, error) {
	s, err := parseSelection(cfg)
	if err != nil {
		return nil, err
	}
	commit, treeSHA, err := c.head(ctx, cfg, s)
	if err != nil {
		return nil, err
	}
	var response tree
	if err := c.get(ctx, cfg, "/repos/"+s.Repository+"/git/trees/"+treeSHA+"?recursive=1", &response, 8<<20); err != nil {
		return nil, err
	}
	result := &DocumentPreviewTree{
		Repository: s.Repository, Ref: s.Ref, Commit: commit,
		Paths: append([]string(nil), s.Paths...), TreeEntries: len(response.Tree), Truncated: response.Truncated,
		Files: make([]DocumentPreviewFile, 0),
	}
	found := make(map[string]bool, len(s.Paths))
	for _, e := range response.Tree {
		if !safePath(e.Path) || !shaPattern.MatchString(e.SHA) {
			return nil, &Error{Code: "github_tree_invalid", Message: "GitHub repository tree contains an invalid path or object ID"}
		}
		for _, root := range s.Paths {
			if e.Path == root || strings.HasPrefix(e.Path, root+"/") {
				found[root] = true
			}
		}
		if allowedDocument(e) && selected(e.Path, s.Paths) {
			result.Files = append(result.Files, DocumentPreviewFile{Path: e.Path, Size: e.Size})
		}
	}
	// A truncated tree cannot prove absence. The result remains visibly partial;
	// callers may still use its lower-bound counts but cannot approve a scope
	// based on it as a complete list.
	if !response.Truncated {
		for _, root := range s.Paths {
			if root != "" && !found[root] {
				result.MissingPaths = append(result.MissingPaths, root)
			}
		}
	}
	return result, nil
}
