package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

func (s *DataSourceService) processSourceSnapshot(ctx context.Context, ds *types.DataSource, cfg *types.DataSourceConfig, connector datasource.Connector, log *types.SyncLog, paused bool) error {
	store, err := snapshot.FromEnvironment()
	var current *types.SourceSnapshot
	if err == nil {
		var builder *snapshot.Builder
		builder, err = store.Begin(ds, cfg)
		if err == nil {
			if provider, ok := connector.(datasource.SnapshotConnector); ok {
				err = provider.BuildSnapshot(ctx, cfg, builder)
			} else {
				err = errors.New("connector does not support source snapshots")
			}
			if err == nil {
				current, err = builder.Finish()
			}
		}
	}
	if err == nil {
		latest, loadErr := s.dsRepo.FindByID(ctx, ds.ID)
		if loadErr != nil {
			_ = store.Delete(ds)
			err = errors.New("source removed during sync")
		} else {
			latestCfg, parseErr := latest.ParseConfig()
			if parseErr != nil || latestCfg == nil || snapshot.Selection(latestCfg) != snapshot.Selection(cfg) {
				err = errors.New("source settings changed during sync; run sync again")
			}
		}
	}
	result := &types.SyncResult{}
	if err == nil {
		previous := map[string]string{}
		if c, _ := ds.ParseSyncCursor(); c != nil {
			if id, ok := c.ConnectorCursor["snapshot_id"].(string); ok {
				if old, e := store.Load(ds, cfg, id); e == nil {
					for _, f := range old.Files {
						previous[f.Path] = f.Object
					}
				}
			}
		}
		for _, f := range current.Files {
			if h, ok := previous[f.Path]; !ok {
				result.Created++
			} else if h != f.Object {
				result.Updated++
			} else {
				result.Skipped++
			}
			delete(previous, f.Path)
		}
		result.Deleted = len(previous)
		result.Total = len(current.Files)
		ds.LastSyncCursor, _ = (&types.SyncCursor{ConnectorCursor: map[string]interface{}{"snapshot_id": current.ID, "revision": current.Revision}}).ToJSON()
		ds.LastSyncAt = timePtr(time.Now().UTC())
		ds.ErrorMessage = ""
		if !paused {
			ds.Status = types.DataSourceStatusActive
		}
		if e := s.dsRepo.UpdateSyncState(ctx, ds); e != nil {
			err = e
		}
	}
	log.Status = types.SyncLogStatusSuccess
	if err != nil {
		log.Status = types.SyncLogStatusFailed
		log.ErrorMessage = err.Error()
		ds.ErrorMessage = err.Error()
		if !paused {
			ds.Status = types.DataSourceStatusError
		}
		_ = s.dsRepo.UpdateSyncState(ctx, ds)
	}
	log.ItemsTotal = result.Total
	log.ItemsCreated = result.Created
	log.ItemsUpdated = result.Updated
	log.ItemsDeleted = result.Deleted
	log.ItemsSkipped = result.Skipped
	log.Result, _ = result.ToJSON()
	log.FinishedAt = timePtr(time.Now().UTC())
	if e := s.syncLogRepo.Update(ctx, log); e != nil && err == nil {
		err = e
	}
	return err
}

// Source reads consume an explicit native KB grant. A tenant ID alone is not
// permission; adapters must resolve the KB for their authenticated principal.
func (s *DataSourceService) sourceKB(ctx context.Context, kbID string) (*types.KnowledgeBase, error) {
	kb, err := s.kbService.GetKnowledgeBaseByIDOnly(ctx, kbID)
	if err != nil || kb == nil {
		return nil, access.ErrNotFound
	}
	if !access.HasKBGrant(ctx, kb.ID, kb.TenantID, types.OrgRoleViewer) {
		return nil, access.ErrForbidden
	}
	if err = types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, kb.ID); err != nil {
		return nil, err
	}
	return kb, nil
}
func (s *DataSourceService) ListSourceSnapshots(ctx context.Context, kbID string) ([]types.SourceSummary, error) {
	kb, err := s.sourceKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	rows, err := s.dsRepo.FindByKnowledgeBase(ctx, kb.ID)
	if err != nil {
		return nil, err
	}
	out := []types.SourceSummary{}
	for _, ds := range rows {
		if ds.TenantID != kb.TenantID {
			continue
		}
		cfg, e := ds.ParseConfig()
		if e != nil || !snapshot.IsSource(cfg) {
			continue
		}
		info := types.SourceSummary{ID: ds.ID, Name: ds.Name, Type: ds.Type, Status: ds.Status}
		_, _, m, e := s.sourceSnapshot(ctx, kb.ID, ds.ID, "")
		if e == nil {
			info.SnapshotID = m.ID
			info.Revision = m.Revision
			info.FileCount = len(m.Files)
			info.Skipped = m.Skipped
			info.CreatedAt = &m.CreatedAt
		} else {
			info.Status = "sync_required"
		}
		out = append(out, info)
	}
	return out, nil
}
func (s *DataSourceService) sourceSnapshot(ctx context.Context, kbID, sourceID, version string) (*types.DataSource, *snapshot.Store, *types.SourceSnapshot, error) {
	kb, err := s.sourceKB(ctx, kbID)
	if err != nil {
		return nil, nil, nil, err
	}
	ds, err := s.dsRepo.FindByID(ctx, sourceID)
	if err != nil || ds == nil || ds.KnowledgeBaseID != kb.ID || ds.TenantID != kb.TenantID {
		return nil, nil, nil, access.ErrNotFound
	}
	cfg, err := ds.ParseConfig()
	if err != nil || !snapshot.IsSource(cfg) {
		return nil, nil, nil, snapshot.ErrUnavailable
	}
	if ds.Type == localfolder.Type {
		connector, lookupErr := s.connectorRegistry.Get(localfolder.Type)
		local, ok := connector.(*localfolder.Connector)
		if lookupErr != nil || !ok {
			return nil, nil, nil, access.ErrForbidden
		}
		if _, e := local.AuthorizedRoot(ctx, ds.TenantID, cfg); e != nil {
			return nil, nil, nil, access.ErrForbidden
		}
	}
	if version == "" {
		if c, _ := ds.ParseSyncCursor(); c != nil {
			version, _ = c.ConnectorCursor["snapshot_id"].(string)
		}
	}
	store, err := snapshot.FromEnvironment()
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := store.Load(ds, cfg, version)
	return ds, store, m, err
}
func (s *DataSourceService) SourceTree(ctx context.Context, kbID, sourceID, version, directory string, offset int) (*types.SourceTree, error) {
	if directory != "" && !snapshot.SafePath(directory) {
		return nil, errors.New("invalid source directory")
	}
	_, _, m, err := s.sourceSnapshot(ctx, kbID, sourceID, version)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if directory != "" {
		prefix = directory + "/"
	}
	entries := map[string]types.SourceTreeEntry{}
	for _, f := range m.Files {
		if !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(f.Path, prefix)
		parts := strings.SplitN(rest, "/", 2)
		p := prefix + parts[0]
		e := types.SourceTreeEntry{Path: p, Directory: len(parts) > 1, Size: f.Size}
		if e.Directory {
			e.Size = 0
		}
		entries[p] = e
	}
	list := make([]types.SourceTreeEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Directory != list[j].Directory {
			return list[i].Directory
		}
		return list[i].Path < list[j].Path
	})
	if offset < 0 {
		offset = 0
	}
	if offset > len(list) {
		offset = len(list)
	}
	end := offset + 200
	if end > len(list) {
		end = len(list)
	}
	return &types.SourceTree{SnapshotID: m.ID, Entries: list[offset:end], Total: len(list)}, nil
}
func sourceLines(body []byte) []string {
	if len(body) == 0 {
		return []string{}
	}
	return strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
}
func lineURL(base string, start, end int) string {
	if base == "" {
		return ""
	}
	if end < start {
		return base
	}
	return fmt.Sprintf("%s#L%d-L%d", base, start, end)
}
func (s *DataSourceService) ReadSourceFile(ctx context.Context, kbID, sourceID, version, p string, start, end int) (*types.SourceRead, error) {
	if !snapshot.SafePath(p) {
		return nil, access.ErrNotFound
	}
	ds, store, m, err := s.sourceSnapshot(ctx, kbID, sourceID, version)
	if err != nil {
		return nil, err
	}
	i := sort.Search(len(m.Files), func(i int) bool { return m.Files[i].Path >= p })
	if i == len(m.Files) || m.Files[i].Path != p {
		return nil, access.ErrNotFound
	}
	f := m.Files[i]
	body, err := store.Content(ds, f)
	if err != nil {
		return nil, err
	}
	lines := sourceLines(body)
	if start < 1 {
		start = 1
	}
	if end < start {
		end = start + 99
	}
	truncated := false
	if end-start+1 > 200 {
		end = start + 199
		truncated = true
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > len(lines)+1 {
		return nil, errors.New("line start exceeds file length")
	}
	content := ""
	if end >= start {
		content = strings.Join(lines[start-1:end], "\n")
	}
	if len(content) > 65536 {
		content = truncateSourceText(content, 65536)
		truncated = true
	}
	revision := f.Revision
	if revision == "" {
		revision = "snapshot:" + m.ID
	}
	preview := "/platform/knowledge-bases/" + url.PathEscape(kbID) + "?source_id=" + url.QueryEscape(sourceID) + "&source_path=" + url.QueryEscape(p) + "&snapshot_id=" + m.ID + fmt.Sprintf("&source_line=%d", start)
	return &types.SourceRead{DataSourceID: sourceID, SnapshotID: m.ID, Path: p, Revision: revision, StartLine: start, EndLine: end, TotalLines: len(lines), Content: content, Truncated: truncated, SourceURL: lineURL(f.SourceURL, start, end), PreviewURL: preview}, nil
}
func (s *DataSourceService) SearchSourceFiles(ctx context.Context, kbID, sourceID, version, q, prefix string) (*types.SourceSearch, error) {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 256 {
		return nil, errors.New("search text must contain 1 to 256 bytes")
	}
	if prefix != "" && !snapshot.SafePath(prefix) {
		return nil, errors.New("invalid path filter")
	}
	ds, store, m, err := s.sourceSnapshot(ctx, kbID, sourceID, version)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := &types.SourceSearch{SnapshotID: m.ID, Matches: []types.SourceMatch{}, Complete: true}
	needle := strings.ToLower(q)
	for _, f := range m.Files {
		if prefix != "" && f.Path != prefix && !strings.HasPrefix(f.Path, prefix+"/") {
			continue
		}
		if ctx.Err() != nil {
			out.Complete = false
			break
		}
		body, err := store.Content(ds, f)
		if err != nil {
			return nil, err
		}
		out.ScannedFiles++
		if strings.Contains(strings.ToLower(path.Base(f.Path)), needle) {
			out.Matches = append(out.Matches, types.SourceMatch{Path: f.Path, Line: 1, Text: f.Path, SourceURL: lineURL(f.SourceURL, 1, 1)})
			if len(out.Matches) >= 40 {
				out.Complete = false
				return out, nil
			}
		}
		for i, line := range sourceLines(body) {
			if !strings.Contains(strings.ToLower(line), needle) {
				continue
			}
			if len(line) > 1200 {
				line = truncateSourceText(line, 1200)
			}
			out.Matches = append(out.Matches, types.SourceMatch{Path: f.Path, Line: i + 1, Text: line, SourceURL: lineURL(f.SourceURL, i+1, i+1)})
			if len(out.Matches) >= 40 {
				out.Complete = false
				return out, nil
			}
		}
	}
	return out, nil
}

func truncateSourceText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}

func removeSourceCache(ds *types.DataSource) {
	if store, err := snapshot.FromEnvironment(); err == nil {
		_ = store.Delete(ds)
	}
}

// LocalSourceRoots returns only the current workspace's database-backed grants.
func (s *DataSourceService) LocalSourceRoots(ctx context.Context, tenant uint64) ([]localfolder.Root, error) {
	connector, err := s.connectorRegistry.Get(localfolder.Type)
	if err != nil {
		return nil, err
	}
	local, ok := connector.(*localfolder.Connector)
	if !ok {
		return nil, errors.New("server folder registry unavailable")
	}
	return local.Roots(ctx, tenant)
}
