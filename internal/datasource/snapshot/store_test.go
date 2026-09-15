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
