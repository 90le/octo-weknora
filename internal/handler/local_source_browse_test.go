package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWorkspaceDirectoryBrowseIsScopedRelativeAndAdminOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "browse.db")), &gorm.Config{})
	require.NoError(t, err)
	raw, _ := db.DB()
	t.Cleanup(func() { _ = raw.Close() })
	migration, err := os.ReadFile("../../migrations/sqlite/000020_local_source_roots.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "granted", "chapter"), 0o700))
	space := localfolder.Space{ID: "space", Name: "System path", Path: base}
	require.NoError(t, db.Create(&space).Error)
	root := localfolder.Root{ID: "grant", TenantID: 7, SpaceID: space.ID, Directory: "granted", Name: "Safe relative input", Enabled: true}
	require.NoError(t, db.Create(&root).Error)
	t.Setenv("DATASOURCE_LOCAL_SPACES", "")
	t.Setenv("DATASOURCE_LOCAL_ROOTS", "")
	registry, err := localfolder.NewRegistry(db)
	require.NoError(t, err)
	h := NewLocalRootHandler(registry, nil)
	request := func(tenant uint64, role types.TenantRole, directory string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenant)
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
		c.Request = httptest.NewRequest(http.MethodGet, "/?directory="+directory, nil).WithContext(ctx)
		c.Params = gin.Params{{Key: "root_id", Value: root.ID}}
		h.BrowseGrantedRoot(c)
		return w
	}
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		require.Equal(t, 403, request(7, role, "").Code)
	}
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		w := request(7, role, "")
		require.Equal(t, 200, w.Code)
		require.Contains(t, w.Body.String(), "chapter")
		require.NotContains(t, w.Body.String(), base)
		require.NotContains(t, w.Body.String(), "granted")
		require.NotContains(t, w.Body.String(), "space_id")
	}
	require.Equal(t, 404, request(8, types.TenantRoleAdmin, "").Code)
	w := request(7, types.TenantRoleAdmin, "..%2Foutside")
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "invalid_folder_input")
	require.NoError(t, db.Model(&root).Update("enabled", false).Error)
	require.Equal(t, 404, request(7, types.TenantRoleAdmin, "").Code)
	// Submitted physical paths do not become registration properties.
	var req localfolder.SpaceRegistration
	require.NoError(t, json.NewDecoder(strings.NewReader(`{"directory":"selected","name":"Name","path":"/etc","space_id":"guessed"}`)).Decode(&req))
	require.Equal(t, localfolder.SpaceRegistration{Name: "Name", Directory: "selected"}, req)
}
