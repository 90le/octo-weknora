package localfolder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func registryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "roots.db")+"?_foreign_keys=on"), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	migration, err := os.ReadFile("../../../../migrations/sqlite/000020_local_source_roots.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	require.NoError(t, db.Exec("CREATE TABLE tenants (id BIGINT PRIMARY KEY, deleted_at TIMESTAMP)").Error)
	require.NoError(t, db.Exec("INSERT INTO tenants(id) VALUES (7),(8)").Error)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}))
	return db
}
func registryWithSpace(t *testing.T, root string) *Registry {
	t.Helper()
	r := &Registry{db: registryDB(t)}
	b, err := json.Marshal([]Space{{ID: "docs", Name: "Mounted documents", Path: root}})
	require.NoError(t, err)
	require.NoError(t, r.initialize(string(b), ""))
	return r
}
func TestRegistryImportRunsOnceAndCannotResurrectRevokedRoots(t *testing.T) {
	db := registryDB(t)
	p := t.TempDir()
	r := &Registry{db: db}
	legacy, _ := json.Marshal([]map[string]any{{"id": "legacy", "name": "Existing", "path": p, "tenant_id": 7}})
	require.NoError(t, r.initialize("", string(legacy)))
	rows, err := r.Roots(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, p, rows[0].Path)
	require.NoError(t, r.Delete(context.Background(), 7, "legacy"))
	require.NoError(t, r.initialize("", string(legacy)))
	// Even malformed old config is ignored after migration: it is no longer read.
	require.NoError(t, r.initialize("", "not json"))
	rows, err = r.Roots(context.Background(), 7)
	require.NoError(t, err)
	require.Empty(t, rows)
	reopened := &Registry{db: db}
	require.NoError(t, reopened.initialize("", string(legacy)))
	rows, err = reopened.Roots(context.Background(), 7)
	require.NoError(t, err)
	require.Empty(t, rows)
}
func TestRegistryContainsPathsIsolatesWorkspacesAndPreservesFiles(t *testing.T) {
	ctx := context.Background()
	mounted := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(mounted, "product"), 0o700))
	original := filepath.Join(mounted, "product", "README.md")
	require.NoError(t, os.WriteFile(original, []byte("source evidence"), 0o600))
	r := registryWithSpace(t, mounted)
	for _, p := range []string{"../secret", "/etc", "C:\\Windows", "x/../../other", "product/../other", "product\\nested"} {
		err := r.Probe(ctx, 7, Registration{Name: "Invalid", SpaceID: "docs", Directory: p})
		require.Error(t, err, "path: %s", p)
	}
	_, err := r.Create(ctx, 7, Registration{Name: "Unknown mount", SpaceID: "guess"})
	require.Error(t, err)
	root, err := r.Create(ctx, 7, Registration{Name: "Product", SpaceID: "docs", Directory: "product"})
	require.NoError(t, err)
	other, err := r.Roots(ctx, 8)
	require.NoError(t, err)
	require.Empty(t, other)
	_, err = r.Update(ctx, 8, root.ID, "Steal", true)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.ErrorIs(t, r.Delete(ctx, 8, root.ID), gorm.ErrRecordNotFound)
	_, err = r.Update(ctx, 7, root.ID, "Disabled", false)
	require.NoError(t, err)
	visible, err := r.Roots(ctx, 7)
	require.NoError(t, err)
	require.Empty(t, visible)
	_, err = r.Update(ctx, 7, root.ID, "Enabled", true)
	require.NoError(t, err)
	visible, err = r.Roots(ctx, 7)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	cfg, err := (&types.DataSourceConfig{Settings: map[string]interface{}{"root_id": root.ID}}).ToJSON()
	require.NoError(t, err)
	ds := types.DataSource{ID: "using-folder", TenantID: 7, KnowledgeBaseID: "kb", Type: Type, Config: cfg}
	require.NoError(t, r.db.Create(&ds).Error)
	require.ErrorIs(t, r.Delete(ctx, 7, root.ID), ErrRootInUse)
	require.NoError(t, r.db.Delete(&ds).Error)
	require.NoError(t, r.Delete(ctx, 7, root.ID))
	body, err := os.ReadFile(original)
	require.NoError(t, err)
	require.Equal(t, "source evidence", string(body))
}
func TestRegistryRejectsSymlinkRootReplacement(t *testing.T) {
	ctx := context.Background()
	mounted := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(mounted, "product"), 0o700))
	r := registryWithSpace(t, mounted)
	root, err := r.Create(ctx, 7, Registration{Name: "Product", SpaceID: "docs", Directory: "product"})
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(mounted, "product")))
	if err = os.Symlink(outside, filepath.Join(mounted, "product")); err != nil {
		t.Skip("symlink unavailable")
	}
	require.ErrorIs(t, r.Probe(ctx, 7, Registration{Name: "Product", SpaceID: "docs", Directory: "product"}), ErrRootUnavailable)
	connector := NewConnector(r)
	ctx = context.WithValue(ctx, types.TenantIDContextKey, uint64(7))
	err = connector.Validate(ctx, &types.DataSourceConfig{Settings: map[string]interface{}{"root_id": root.ID, "mode": "documents"}})
	require.Error(t, err)
}
func TestRegistryCannotRetargetApprovedSpace(t *testing.T) {
	mounted := t.TempDir()
	r := registryWithSpace(t, mounted)
	changed, _ := json.Marshal([]Space{{ID: "docs", Name: "Retarget", Path: t.TempDir()}})
	require.Error(t, r.initialize(string(changed), ""))
	spaces, err := r.Spaces(context.Background())
	require.NoError(t, err)
	require.Len(t, spaces, 1)
	require.Equal(t, mounted, spaces[0].Path)
}
func TestConnectorWithoutDatabaseFailsClosed(t *testing.T) {
	t.Setenv("DATASOURCE_LOCAL_ROOTS", `[{"id":"secret","name":"Secret","path":"/etc","tenant_id":7}]`)
	_, err := NewConnector().Roots(context.Background(), 7)
	require.Error(t, err)
}
