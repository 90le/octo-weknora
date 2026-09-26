package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

// DocumentPlan is a complete, fixed-commit listing. Upserts can be processed
// in bounded chunks, but Cursor must only be published when every upsert and
// deletion has been acknowledged by the knowledge-base indexer.
type DocumentPlan struct {
	Selection string
	Commit    string
	Digest    string
	Upserts   []DocumentPlanItem
	Deletions []DocumentPlanItem
	Complete  bool // previous fully published cursor already matches this commit

	selection selection
	files     map[string]entry
	cache     *gitCache
}

type DocumentPlanItem struct {
	Path    string
	BlobSHA string
	Size    int64
	Mode    string
	Delete  bool
}

func DocumentSelection(cfg *types.DataSourceConfig) (string, error) {
	s, err := parseSelection(cfg)
	if err != nil {
		return "", err
	}
	excludes, err := documentExcludes(cfg)
	if err != nil {
		return "", err
	}
	return documentSelectionKey(s, excludes), nil
}

// PlanDocuments uses the same allowedDocument + selected predicates as the
// normal connector and the read-only scope preview. The remote ref is checked
// on every call (including pinned retries) before trusting cached Git objects.
// pinCommit is only supplied from a durable in-progress run for this exact
// source/selection/credential scope; it is never a user-provided remote URL.
func (c *Connector) PlanDocuments(
	ctx context.Context, cfg *types.DataSourceConfig, published *types.SyncCursor,
	forceFull bool, pinCommit string,
) (*DocumentPlan, error) {
	var plan *DocumentPlan
	err := c.withSyncGate(ctx, func() error {
		var inner error
		plan, inner = c.planDocuments(ctx, cfg, published, forceFull, pinCommit)
		return inner
	})
	return plan, err
}

func (c *Connector) planDocuments(
	ctx context.Context, cfg *types.DataSourceConfig, published *types.SyncCursor,
	forceFull bool, pinCommit string,
) (*DocumentPlan, error) {
	if cfg != nil {
		cfg.SkippedSensitive = 0
		cfg.SkippedExcluded = 0
	}
	if snapshot.IsSource(cfg) || cfg == nil || cfg.SyncSource == nil {
		return nil, fmt.Errorf("%w: document plan requires a trusted document source", datasource.ErrInvalidConfig)
	}
	s, err := parseSelection(cfg)
	if err != nil {
		return nil, err
	}
	excludes, err := documentExcludes(cfg)
	if err != nil {
		return nil, err
	}
	key := documentSelectionKey(s, excludes)
	prev := cursor{}
	if published != nil {
		b, _ := json.Marshal(published.ConnectorCursor)
		if json.Unmarshal(b, &prev) != nil {
			return nil, fmt.Errorf("GitHub sync cursor is invalid")
		}
	}
	headCommit, _, err := c.head(ctx, cfg, s)
	if err != nil {
		return nil, err
	}
	commit := headCommit
	if pinCommit != "" {
		if !shaPattern.MatchString(pinCommit) {
			return nil, &Error{Code: "github_ref_invalid", Message: "GitHub document checkpoint references an invalid commit"}
		}
		commit = pinCommit
	}
	plan := &DocumentPlan{Selection: key, Commit: commit, selection: s}
	if !forceFull && documentCursorReusable(cfg, prev, key, commit) {
		plan.Complete = true
		plan.files = prev.Files
		plan.Digest = documentPlanDigest(key, commit, prev.Files)
		return plan, nil
	}
	cache, entries, err := c.documentGitCache(ctx, cfg, s, commit)
	if err != nil {
		return nil, err
	}
	plan.cache = cache
	all := make(map[string]entry, len(entries))
	plan.files = make(map[string]entry)
	for _, e := range entries {
		all[e.Path] = e
		if !selected(e.Path, s.Paths) || !allowedDocument(e) {
			continue
		}
		if !allowedGitHubDocument(e) {
			cfg.SkippedSensitive++
			continue
		}
		if !documentInCurrentScope(e, s, excludes) {
			cfg.SkippedExcluded++
			continue
		}
		plan.files[e.Path] = e
	}
	if len(plan.files) > 2000 {
		return nil, &Error{Code: "github_documents_limit", Message: "GitHub document source exceeds 2000 files; narrow the selected paths or use read-only source mode"}
	}
	// Old documents outside the selected path or explicit/mandatory exclusion
	// need a separately reviewed retirement. Retain their cursor lineage even
	// if the scope changed; never infer deletion from a policy edit.
	for path, previous := range prev.Files {
		if documentHistoricalOutOfScope(path, s, excludes) {
			plan.files[path] = previous
		}
	}
	paths := make([]string, 0, len(plan.files))
	for path := range plan.files {
		if documentHistoricalOutOfScope(path, s, excludes) {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		e := plan.files[path]
		if !forceFull && prev.Selection == key && prev.Files[path].SHA == e.SHA {
			continue
		}
		if e.Size < 0 || e.Size > maxFileBytes {
			return nil, &Error{Code: "github_document_file_limit", Message: "A GitHub document exceeds the 16 MiB file limit; narrow the selected paths"}
		}
		plan.Upserts = append(plan.Upserts, DocumentPlanItem{Path: path, BlobSHA: e.SHA, Size: e.Size, Mode: e.Mode})
	}
	// Selection changes are not proof of deletion. The complete fixed-commit
	// Git tree is authoritative only for the same selection as the old cursor.
	if prev.Selection == key {
		for path := range prev.Files {
			if documentHistoricalOutOfScope(path, s, excludes) {
				continue
			}
			if _, exists := all[path]; !exists {
				plan.Deletions = append(plan.Deletions, DocumentPlanItem{Path: path, Delete: true})
			}
		}
		sort.Slice(plan.Deletions, func(i, j int) bool { return plan.Deletions[i].Path < plan.Deletions[j].Path })
	}
	plan.Digest = documentPlanDigest(key, commit, plan.files)
	return plan, nil
}

func documentPlanDigest(selection, commit string, files map[string]entry) string {
	cursor := &types.SyncCursor{ConnectorCursor: map[string]interface{}{
		"selection": selection, "commit": commit, "files": files,
	}}
	data, _ := cursor.ToJSON()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (p *DocumentPlan) Cursor() *types.SyncCursor {
	return &types.SyncCursor{ConnectorCursor: map[string]interface{}{
		"selection": p.Selection, "commit": p.Commit, "files": p.files,
	}}
}

// ReadPlannedDocument rejects any item not in the verified plan. The Git
// object is read from the exact per-data-source cache selected during Plan.
func (c *Connector) ReadPlannedDocument(ctx context.Context, plan *DocumentPlan, item DocumentPlanItem) (types.FetchedItem, error) {
	if plan == nil || plan.cache == nil || item.Delete {
		return types.FetchedItem{}, &Error{Code: "github_document_invalid", Message: "GitHub document plan is not readable"}
	}
	e, exists := plan.files[item.Path]
	if !exists || e.SHA != item.BlobSHA || e.Size != item.Size || e.Mode != item.Mode {
		return types.FetchedItem{}, &Error{Code: "github_document_invalid", Message: "GitHub document plan identity changed"}
	}
	var body []byte
	err := plan.cache.readBlobs(ctx, []gitTreeEntry{{Path: e.Path, SHA: e.SHA, Size: e.Size, Mode: e.Mode}}, maxFileBytes,
		func(_ gitTreeEntry, data []byte) error { body = data; return nil })
	if err != nil {
		return types.FetchedItem{}, err
	}
	return githubDocumentItem(plan.selection, plan.Commit, e, body), nil
}

func (p *DocumentPlan) DeletedItem(item DocumentPlanItem) types.FetchedItem {
	return types.FetchedItem{
		ExternalID: "github:" + strings.ToLower(p.selection.Repository) + ":" + p.selection.Ref + ":" + item.Path,
		Title:      item.Path, IsDeleted: true,
	}
}
