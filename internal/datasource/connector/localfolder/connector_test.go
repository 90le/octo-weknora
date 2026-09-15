package localfolder

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func localFixture(t *testing.T) (context.Context, string, *types.DataSourceConfig) {
	t.Helper()
	root := t.TempDir()
	cache := t.TempDir()
	b, _ := json.Marshal([]Root{{ID: "product", Name: "Product", Path: root, TenantID: 7}})
	t.Setenv("DATASOURCE_LOCAL_ROOTS", string(b))
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", cache)
	return context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7)), root, &types.DataSourceConfig{Settings: map[string]interface{}{"root_id": "product", "mode": "source"}}
}
func TestLocalFolderProtectsBoundariesAndKeepsCode(t *testing.T) {
	ctx, root, cfg := localFixture(t)
	for _, name := range []string{"a.js", "b.php", "c.py", ".env"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("hello\n"), 0o600))
	}
	outside := filepath.Join(t.TempDir(), "private.py")
	require.NoError(t, os.WriteFile(outside, []byte("outside secret"), 0o600))
	if err := os.Symlink(outside, filepath.Join(root, "alias.py")); err != nil {
		t.Skip("symlinks unavailable")
	}
	c := NewConnector()
	require.NoError(t, c.Validate(ctx, cfg))
	store, err := snapshot.FromEnvironment()
	require.NoError(t, err)
	builder, err := store.Begin(&types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb"}, cfg)
	require.NoError(t, err)
	require.NoError(t, c.BuildSnapshot(ctx, cfg, builder))
	m, err := builder.Finish()
	require.NoError(t, err)
	require.Len(t, m.Files, 3)
	require.Equal(t, 1, m.Skipped["symlink"])
	require.Equal(t, 1, m.Skipped["excluded"])
	other := context.WithValue(ctx, types.TenantIDContextKey, uint64(8))
	require.Error(t, c.Validate(other, cfg))
	cfg.Settings["directory"] = "../"
	require.Error(t, c.Validate(ctx, cfg))
}
func TestLocalDocumentsIncrementalChanges(t *testing.T) {
	ctx, root, cfg := localFixture(t)
	cfg.Settings["mode"] = "documents"
	p := filepath.Join(root, "README.md")
	require.NoError(t, os.WriteFile(p, []byte("first"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.py"), []byte("not a document"), 0o600))
	c := NewConnector()
	items, old, err := c.FetchIncremental(ctx, cfg, nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, os.WriteFile(p, []byte("second"), 0o600))
	items, next, err := c.FetchIncremental(ctx, cfg, old)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "second", string(items[0].Content))
	require.NoError(t, os.Remove(p))
	items, _, err = c.FetchIncremental(ctx, cfg, next)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.True(t, items[0].IsDeleted)
}
func TestLocalGitCitationOnlyMatchesCommittedBytes(t *testing.T) {
	ctx, root, _ := localFixture(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "user.name", "Fixture")
	body := []byte("print('source')\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.py"), body, 0o600))
	run("add", "main.py")
	run("commit", "-m", "fixture")
	run("remote", "add", "origin", "git@github.com:test/source.git")
	g := inspectGit(ctx, root)
	u, revision := g.reference("main.py", body)
	require.Contains(t, u, "https://github.com/test/source/blob/")
	require.Len(t, revision, 40)
	u, revision = g.reference("main.py", []byte("local modification"))
	require.Empty(t, u)
	require.Empty(t, revision)
}
