package localfolder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func sourceUsingRoot(t *testing.T, r *Registry, id string, tenant uint64, rootID string) *types.DataSource {
	t.Helper()
	cfg, err := (&types.DataSourceConfig{Settings: map[string]any{"root_id": rootID}}).ToJSON()
	require.NoError(t, err)
	s := &types.DataSource{ID: id, TenantID: tenant, KnowledgeBaseID: "kb", Type: Type, Config: cfg}
	require.NoError(t, r.db.Create(s).Error)
	return s
}

func TestSpaceCatalogCountsOnlyMatchingLiveSources(t *testing.T) {
	ctx := context.Background()
	r := registryWithSpace(t, t.TempDir())
	a, err := r.Create(ctx, 7, Registration{Name: "A", SpaceID: "docs"})
	require.NoError(t, err)
	b, err := r.Create(ctx, 8, Registration{Name: "B", SpaceID: "docs"})
	require.NoError(t, err)
	_, err = r.Update(ctx, 8, b.ID, "B", false)
	require.NoError(t, err)
	sourceUsingRoot(t, r, "a-live", 7, a.ID)
	sourceUsingRoot(t, r, "b-live", 8, b.ID)
	deleted := sourceUsingRoot(t, r, "a-deleted", 7, a.ID)
	require.NoError(t, r.db.Delete(deleted).Error)
	sourceUsingRoot(t, r, "wrong-tenant-id", 8, a.ID)
	other := sourceUsingRoot(t, r, "other-type", 7, a.ID)
	require.NoError(t, r.db.Model(other).Update("type", "github").Error)
	spaces, err := r.SpaceSummaries(ctx)
	require.NoError(t, err)
	require.Len(t, spaces, 1)
	require.Equal(t, "ready", spaces[0].Status)
	require.True(t, spaces[0].Readable)
	require.Equal(t, 2, spaces[0].AuthorizedRootCount)
	require.Equal(t, 1, spaces[0].EnabledRootCount)
	require.Equal(t, 2, spaces[0].DataSourceCount)
	require.True(t, spaces[0].UsageComplete)
	roots, err := r.RootSummaries(ctx, 7)
	require.NoError(t, err)
	require.Len(t, roots, 1)
	require.Equal(t, 1, roots[0].DataSourceCount)
	malformed := sourceUsingRoot(t, r, "malformed", 7, a.ID)
	require.NoError(t, r.db.Model(malformed).Update("config", types.JSON(`{broken`)).Error)
	spaces, err = r.SpaceSummaries(ctx)
	require.NoError(t, err)
	require.False(t, spaces[0].UsageComplete)
	roots, err = r.RootSummaries(ctx, 8)
	require.NoError(t, err)
	require.True(t, roots[0].UsageComplete, "a different tenant's malformed config must not change scoped root statistics")
}

func TestSpaceCatalogDistinguishesMissingAndUnsafeWithoutRawError(t *testing.T) {
	ctx := context.Background()
	r := registryWithSpace(t, t.TempDir())
	require.NoError(t, r.db.Create(&Space{ID: "missing", Name: "Missing", Path: filepath.Join(t.TempDir(), "absent")}).Error)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skip("symlinks not available")
	}
	require.NoError(t, r.db.Create(&Space{ID: "linked", Name: "Linked", Path: link}).Error)
	spaces, err := r.SpaceSummaries(ctx)
	require.NoError(t, err)
	status := map[string]string{}
	for _, space := range spaces {
		status[space.ID] = space.Status
	}
	require.Equal(t, "ready", status["docs"])
	require.Equal(t, "unavailable", status["missing"])
	require.Equal(t, "unsafe", status["linked"])
}

func TestDiscoveryRegistersOnlyRealDirectMountedDirectoriesAndPersists(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	r := registryWithSpace(t, filepath.Join(base, "already"))
	r.discoveryPath = base
	for _, dir := range []string{"already", "new", "new/chapter", ".git"} {
		require.NoError(t, os.MkdirAll(filepath.Join(base, filepath.FromSlash(dir)), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(base, "not-a-directory"), []byte("test"), 0o600))
	outside := t.TempDir()
	hasLink := os.Symlink(outside, filepath.Join(base, "link")) == nil
	result, err := r.DiscoverSpaces(ctx)
	require.NoError(t, err)
	require.True(t, result.Available)
	require.Equal(t, "ready", result.Status)
	require.Equal(t, []DirectoryEntry{{Name: "new", Directory: "new"}}, result.Entries)
	for _, dir := range []string{"", ".", "..", "../outside", "/etc", "new/chapter", "new\\chapter", "C:/Windows", "C:Windows", ".git", ".private", strings.Repeat("x", 2049)} {
		_, err = r.RegisterDiscoveredSpace(ctx, SpaceRegistration{Name: "Invalid", Directory: dir})
		require.ErrorIs(t, err, ErrRootInvalid, "%s", dir)
	}
	if hasLink {
		_, err = r.RegisterDiscoveredSpace(ctx, SpaceRegistration{Name: "Link", Directory: "link"})
		require.ErrorIs(t, err, ErrRootUnsafe)
	}
	_, err = r.RegisterDiscoveredSpace(ctx, SpaceRegistration{Name: "Already", Directory: "already"})
	require.ErrorIs(t, err, ErrRootExists)
	space, err := r.RegisterDiscoveredSpace(ctx, SpaceRegistration{Name: "New space", Directory: "new"})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(base, "new"), space.Path)
	_, err = r.RegisterDiscoveredSpace(ctx, SpaceRegistration{Name: "Duplicate", Directory: "new"})
	require.ErrorIs(t, err, ErrRootExists)
	reopened := &Registry{db: r.db, discoveryPath: base}
	require.NoError(t, reopened.initialize("", "ignored legacy config after first start"))
	root, err := reopened.Create(ctx, 7, Registration{Name: "Granted", SpaceID: space.ID})
	require.NoError(t, err)
	page, err := reopened.BrowseRoot(ctx, 7, root.ID, "", 0)
	require.NoError(t, err)
	require.Equal(t, []DirectoryEntry{{Name: "chapter", Directory: "chapter"}}, page.Entries)
	result, err = reopened.DiscoverSpaces(ctx)
	require.NoError(t, err)
	require.Empty(t, result.Entries)
	_, err = os.Stat(outside)
	require.NoError(t, err, "discovery and registration must not change original folders")
}

func TestDirectoryBrowserRejectsTraversalSymlinksAndRevokedGrants(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "product", "chapter"), 0o700))
	r := registryWithSpace(t, base)
	root, err := r.Create(ctx, 7, Registration{Name: "Product", SpaceID: "docs", Directory: "product"})
	require.NoError(t, err)
	for _, dir := range []string{"../product", "chapter/../other", "/etc", "C:/Windows", "C:relative", "chapter\\nested", "chapter//nested", "\x00", strings.Repeat("a", 2049)} {
		_, err = r.BrowseRoot(ctx, 7, root.ID, dir, 0)
		require.ErrorIs(t, err, ErrRootInvalid)
		_, err = r.BrowseSpace(ctx, "docs", dir, 0)
		require.ErrorIs(t, err, ErrRootInvalid)
	}
	_, err = r.BrowseRoot(ctx, 8, root.ID, "", 0)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	page, err := r.BrowseRoot(ctx, 7, root.ID, "", 0)
	require.NoError(t, err)
	serialized, _ := json.Marshal(page)
	require.NotContains(t, string(serialized), base)
	require.NotContains(t, string(serialized), "space_path")
	_, err = r.Update(ctx, 7, root.ID, "Disabled", false)
	require.NoError(t, err)
	_, err = r.BrowseRoot(ctx, 7, root.ID, "", 0)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = r.Update(ctx, 7, root.ID, "Enabled", true)
	require.NoError(t, err)
	link := filepath.Join(base, "product", "outside")
	if e := os.Symlink(t.TempDir(), link); e != nil {
		t.Skip("symlinks not available")
	}
	page, err = r.BrowseRoot(ctx, 7, root.ID, "", 0)
	require.NoError(t, err)
	require.Equal(t, []DirectoryEntry{{Name: "chapter", Directory: "chapter"}}, page.Entries)
	_, err = r.BrowseRoot(ctx, 7, root.ID, "outside", 0)
	require.ErrorIs(t, err, ErrRootUnsafe)
	require.NoError(t, os.RemoveAll(filepath.Join(base, "product", "chapter")))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(base, "product", "chapter")))
	_, err = r.BrowseRoot(ctx, 7, root.ID, "chapter", 0)
	require.ErrorIs(t, err, ErrRootUnsafe)
}

func TestDirectoryBrowserOrdersPagesAndDoesNotReadFiles(t *testing.T) {
	base := t.TempDir()
	r := registryWithSpace(t, base)
	ctx := context.Background()
	for i := 204; i >= 0; i-- {
		require.NoError(t, os.Mkdir(filepath.Join(base, fmt.Sprintf("folder-%03d", i)), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(base, "binary-secret"), []byte{0xff, 0, 0xfe}, 0o000))
	require.NoError(t, os.Mkdir(filepath.Join(base, ".git"), 0o700))
	a, err := r.BrowseSpace(ctx, "docs", "", 0)
	require.NoError(t, err)
	require.Len(t, a.Entries, 200)
	require.Equal(t, "folder-000", a.Entries[0].Name)
	require.True(t, a.HasMore)
	b, err := r.BrowseSpace(ctx, "docs", "", a.NextOffset)
	require.NoError(t, err)
	require.Len(t, b.Entries, 5)
	require.Equal(t, "folder-200", b.Entries[0].Name)
	require.False(t, b.HasMore)
	_, err = r.BrowseSpace(ctx, "docs", "", -1)
	require.ErrorIs(t, err, ErrRootInvalid)
	_, err = r.BrowseSpace(ctx, "docs", "", directoryScanLimit+1)
	require.ErrorIs(t, err, ErrRootInvalid)
}
