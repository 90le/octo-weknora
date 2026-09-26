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
	data, _ := json.Marshal(s)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
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
	if snapshot.IsSource(cfg) || cfg == nil || cfg.SyncSource == nil {
		return nil, fmt.Errorf("%w: document plan requires a trusted document source", datasource.ErrInvalidConfig)
	}
	s, err := parseSelection(cfg)
	if err != nil {
		return nil, err
	}
	selectionJSON, _ := json.Marshal(s)
	selectionHash := sha256.Sum256(selectionJSON)
	key := hex.EncodeToString(selectionHash[:])
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
	if !forceFull && prev.Selection == key && prev.Commit == commit {
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
		if allowedDocument(e) && selected(e.Path, s.Paths) {
			plan.files[e.Path] = e
		}
	}
	if len(plan.files) > 2000 {
		return nil, &Error{Code: "github_documents_limit", Message: "GitHub document source exceeds 2000 files; narrow the selected paths or use read-only source mode"}
	}
	paths := make([]string, 0, len(plan.files))
	for path := range plan.files {
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
	data, _ := json.Marshal(struct {
		Selection string           `json:"selection"`
		Commit    string           `json:"commit"`
		Files     map[string]entry `json:"files"`
	}{selection, commit, files})
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
