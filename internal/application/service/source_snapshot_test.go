package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type sourceKBStub struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *sourceKBStub) GetKnowledgeBaseByIDOnly(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type sourceDSStub struct {
	interfaces.DataSourceRepository
	ds *types.DataSource
}

func (s *sourceDSStub) FindByID(_ context.Context, id string) (*types.DataSource, error) {
	if id != s.ds.ID {
		return nil, errors.New("missing")
	}
	return s.ds, nil
}
func (s *sourceDSStub) FindByKnowledgeBase(context.Context, string) ([]*types.DataSource, error) {
	return []*types.DataSource{s.ds}, nil
}
func TestSourceReadsRequireNativeKBGrantAndReturnPinnedLines(t *testing.T) {
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", t.TempDir())
	kb := &types.KnowledgeBase{ID: "kb", TenantID: 7}
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"mode": "source"}}
	config, err := cfg.ToJSON()
	require.NoError(t, err)
	ds := &types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: kb.ID, Type: "github", Config: config}
	store, err := snapshot.FromEnvironment()
	require.NoError(t, err)
	builder, err := store.Begin(ds, cfg)
	require.NoError(t, err)
	u := "https://github.com/test/repo/blob/" + strings.Repeat("a", 40) + "/src/main.py"
	require.NoError(t, builder.Add(context.Background(), "src/main.py", []byte("first\nsecond\nthird\n"), u, strings.Repeat("a", 40)))
	m, err := builder.Finish()
	require.NoError(t, err)
	ds.LastSyncCursor, err = (&types.SyncCursor{ConnectorCursor: map[string]interface{}{"snapshot_id": m.ID}}).ToJSON()
	require.NoError(t, err)
	svc := &DataSourceService{kbService: &sourceKBStub{kb: kb}, dsRepo: &sourceDSStub{ds: ds}}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "fixture")
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
	_, err = svc.ReadSourceFile(ctx, kb.ID, ds.ID, "", "src/main.py", 2, 2)
	require.ErrorIs(t, err, access.ErrForbidden)
	grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: types.CallerFromContext(ctx)}, kb, types.OrgRoleViewer, nil, nil)
	require.NoError(t, err)
	ctx = grant.Context(ctx)
	read, err := svc.ReadSourceFile(ctx, kb.ID, ds.ID, "", "src/main.py", 2, 2)
	require.NoError(t, err)
	require.Equal(t, "second", read.Content)
	require.Equal(t, u+"#L2-L2", read.SourceURL)
	require.Contains(t, read.PreviewURL, "snapshot_id="+m.ID)
	_, err = svc.ReadSourceFile(ctx, kb.ID, "other", m.ID, "src/main.py", 1, 1)
	require.Error(t, err)
	_, err = svc.ReadSourceFile(ctx, kb.ID, ds.ID, m.ID, "../main.py", 1, 1)
	require.Error(t, err)
	found, err := svc.SearchSourceFiles(ctx, kb.ID, ds.ID, m.ID, "second", "")
	require.NoError(t, err)
	require.Len(t, found.Matches, 1)
	require.Equal(t, 2, found.Matches[0].Line)
	cfg.Settings["exclude"] = []string{"src"}
	ds.Config, err = cfg.ToJSON()
	require.NoError(t, err)
	_, err = svc.ReadSourceFile(ctx, kb.ID, ds.ID, m.ID, "src/main.py", 1, 1)
	require.Error(t, err)
}
