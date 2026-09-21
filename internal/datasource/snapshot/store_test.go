package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSnapshotsKeepTextLanguagesAndBindScope(t *testing.T) {
	s := &Store{Base: t.TempDir()}
	ds := &types.DataSource{ID: "source", TenantID: 7, KnowledgeBaseID: "kb"}
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"mode": "source"}}
	b, err := s.Begin(ds, cfg)
	require.NoError(t, err)
	for _, p := range []string{"a.js", "b.php", "c.py", "nested/program.unknown", ".github/workflows/build.yml"} {
		require.NoError(t, b.Add(context.Background(), p, []byte("first\nsecond\n"), "", ""))
	}
	require.NoError(t, b.Add(context.Background(), "binary", []byte{0, 1, 2}, "", ""))
	m, err := b.Finish()
	require.NoError(t, err)
	require.Len(t, m.Files, 5)
	require.Equal(t, 1, m.Skipped["binary_or_unsupported_encoding"])
	m2, err := s.Load(ds, cfg, m.ID)
	require.NoError(t, err)
	require.Equal(t, m.ID, m2.ID)
	other := *ds
	other.TenantID = 8
	_, err = s.Load(&other, cfg, m.ID)
	require.Error(t, err)
	cfg.Settings["exclude"] = []string{"nested"}
	_, err = s.Load(ds, cfg, m.ID)
	require.Error(t, err, "old broad snapshot must not survive a policy change")
	_, err = s.Content(ds, types.SourceFile{Object: "../../secret"})
	require.Error(t, err)
	object := m.Files[0]
	require.NoError(t, os.WriteFile(filepath.Join(s.scope(ds), "objects", object.Object), []byte("tampered"), 0o600))
	_, err = s.Content(ds, object)
	require.Error(t, err)
}
func TestSnapshotPolicyDoesNotWhitelistLanguages(t *testing.T) {
	for _, p := range []string{"src/main.js", "app.php", "nested/handler.py", ".github/workflows/check.yml", "config.toml"} {
		require.False(t, Excluded(p, DefaultExcludes), p)
	}
	for _, p := range []string{"../escape", "/etc/passwd", "src/.env", ".git/config", "secrets/x.py", "node_modules/lib/a.js"} {
		require.True(t, Excluded(p, DefaultExcludes), p)
	}
	require.False(t, Excluded("vendor/owned-library/main.php", []string{}))
	require.True(t, Excluded("secrets/x.py", []string{}))
}

func TestSnapshotReuseKeepsObjectScopedAndUpdatesGitMetadata(t *testing.T) {
	s := &Store{Base: t.TempDir()}
	ds := &types.DataSource{ID: "source", TenantID: 7, KnowledgeBaseID: "kb"}
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"mode": "source"}}
	first, err := s.Begin(ds, cfg)
	require.NoError(t, err)
	require.NoError(t, first.AddGit(context.Background(), "src/app.go", []byte("package main\n"), "https://example.invalid/old", "old", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	old, err := first.Finish()
	require.NoError(t, err)

	next, err := s.Begin(ds, cfg)
	require.NoError(t, err)
	file := old.Files[0]
	file.SourceURL = "https://example.invalid/new"
	file.Revision = "new"
	require.NoError(t, next.Reuse(file))
	current, err := next.Finish()
	require.NoError(t, err)
	require.Equal(t, old.Files[0].Object, current.Files[0].Object)
	require.Equal(t, "new", current.Files[0].Revision)

	other := *ds
	other.TenantID = 8
	foreign, err := s.Begin(&other, cfg)
	require.NoError(t, err)
	require.Error(t, foreign.Reuse(file), "objects cannot cross a data-source scope")
}

func TestClearPrivateDirectoryDoesNotRemovePublishedSnapshot(t *testing.T) {
	s := &Store{Base: t.TempDir()}
	ds := &types.DataSource{ID: "source", TenantID: 7, KnowledgeBaseID: "kb"}
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"mode": "source"}}
	b, err := s.Begin(ds, cfg)
	require.NoError(t, err)
	require.NoError(t, b.Add(context.Background(), "README.md", []byte("hello\n"), "", ""))
	manifest, err := b.Finish()
	require.NoError(t, err)
	private, err := b.PrivateDirectory("git")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(private, "cache"), []byte("temporary"), 0o600))

	require.NoError(t, s.ClearPrivateDirectory(ds, "git"))
	_, err = os.Stat(private)
	require.True(t, os.IsNotExist(err))
	loaded, err := s.Load(ds, cfg, manifest.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Files, 1)
}
