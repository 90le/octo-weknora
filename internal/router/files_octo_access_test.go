package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type octoRouterCatalog struct {
	*stubResourceCatalog
	matched, allowed bool
	err              error
}

func TestOctoFilesProxyUnregisteredArtifact(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:router_unregistered_octo?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.StoredResource{}, &types.ResourceBinding{}, &types.ResourceAccessGrant{}))
	for _, sql := range []string{
		`CREATE TABLE sessions (id text, tenant_id integer)`,
		`CREATE TABLE messages (id text, session_id text)`,
		`CREATE TABLE message_artifacts (message_id text, session_id text, url text)`,
		`CREATE TABLE im_channel_sessions (session_id text, tenant_id integer, im_channel_id text, platform text)`,
		`CREATE TABLE im_channel_web_owners (
			tenant_id integer, im_channel_id text, web_user_id text, deleted_at datetime)`,
		`CREATE TABLE im_channels (id text, tenant_id integer, deleted_at datetime)`,
		`CREATE TABLE tenant_members (tenant_id integer, user_id text, status text, deleted_at datetime)`,
		`CREATE TABLE users (id text, is_active boolean, deleted_at datetime)`,
		`INSERT INTO sessions VALUES ('s',7)`, `INSERT INTO messages VALUES ('m','s')`,
		`INSERT INTO message_artifacts VALUES ('m','s','local://7/exports/./private.pdf')`,
		`INSERT INTO im_channel_sessions VALUES ('s',7,'c','octo')`,
		`INSERT INTO im_channels VALUES ('c',7,NULL)`,
		`INSERT INTO users VALUES ('alice',1,NULL),('bob',1,NULL)`,
		`INSERT INTO tenant_members VALUES (7,'alice','active',NULL),(7,'bob','active',NULL)`,
		`INSERT INTO im_channel_web_owners VALUES (7,'c','alice',NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	catalog := service.NewResourceCatalog(repository.NewResourceRepository(db))
	opens := 0
	fs := &stubFileService{getFile: func(context.Context, string) (io.ReadCloser, error) {
		opens++
		return io.NopCloser(strings.NewReader("secret")), nil
	}}
	engine := gin.New()
	engine.GET("/files", newFileServeHandler(fs, nil, catalog))
	request := func(user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet,
			"/files?file_path="+url.QueryEscape("local://7/exports/private.pdf"), nil)
		ctx := context.WithValue(req.Context(), types.TenantInfoContextKey, &types.Tenant{ID: 7})
		ctx = types.WithCaller(ctx, types.Caller{TenantID: 7, UserID: user})
		ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalWebUser, ID: user})
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	require.Equal(t, http.StatusForbidden, request("bob").Code)
	require.Zero(t, opens)
	response := request("alice")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "secret", response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
}

func (s *octoRouterCatalog) CheckOctoArtifactAccess(context.Context, uint64, string) (bool, bool, error) {
	return s.matched, s.allowed, s.err
}

func TestOctoFilesProxyChecksBeforeStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const ref = "resource://abcdefghijklmnopqrstuv"
	catalog := &octoRouterCatalog{
		stubResourceCatalog: &stubResourceCatalog{
			resource: &types.StoredResource{TenantID: 7, PhysicalPath: "local://7/report.pdf"},
		},
		matched: true,
	}
	opens := 0
	fs := &stubFileService{getFile: func(context.Context, string) (io.ReadCloser, error) {
		opens++
		return io.NopCloser(strings.NewReader("ok")), nil
	}}
	engine := gin.New()
	engine.GET("/files", newFileServeHandler(fs, nil, catalog))
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/files?file_path="+url.QueryEscape(path), nil)
		ctx := context.WithValue(req.Context(), types.TenantInfoContextKey, &types.Tenant{ID: 7})
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	for _, path := range []string{ref, "local://7/report.pdf"} {
		require.Equal(t, http.StatusForbidden, request(path).Code)
	}
	require.Zero(t, opens)
	// Same-tenant parent traversal reaches normal origin checks; leaving the
	// tenant root stays blocked even if the final path would re-enter it.
	inside := "local://7/exports/sub/../report.pdf"
	require.Equal(t, http.StatusForbidden, request(inside).Code)
	require.Zero(t, opens)
	catalog.allowed = true
	require.Equal(t, http.StatusOK, request(inside).Code)
	catalog.matched = false // ordinary Web resource
	require.Equal(t, http.StatusOK, request(inside).Code)
	require.Equal(t, http.StatusBadRequest, request("local://7/exports/../../7/report.pdf").Code)
	require.Equal(t, http.StatusBadRequest, request("s3://bucket/7/exports/sub/../report.pdf").Code)
	catalog.matched = true
	response := request(ref)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, 3, opens)
}
