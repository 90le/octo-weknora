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

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIMWebOwnerAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "owner.db")), &gorm.Config{})
	require.NoError(t, err)
	for _, sql := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, is_active BOOLEAN NOT NULL, deleted_at DATETIME)`,
		`CREATE TABLE tenant_members (id INTEGER PRIMARY KEY, user_id ` +
			`TEXT, tenant_id INTEGER, role TEXT, status TEXT, deleted_at ` +
			`DATETIME)`,
		`CREATE TABLE im_channels (id TEXT PRIMARY KEY, tenant_id INTEGER,` +
			` platform TEXT, deleted_at DATETIME, credentials TEXT)`,
		`CREATE TABLE im_channel_sessions (id TEXT PRIMARY KEY, tenant_id ` +
			`INTEGER, im_channel_id TEXT, platform TEXT, deleted_at DATETIME)`,
		`CREATE TABLE audit_logs (id INTEGER PRIMARY KEY, tenant_id ` +
			`INTEGER, actor_user_id TEXT, actor_role TEXT, action TEXT, ` +
			`scope_type TEXT, scope_id TEXT, target_type TEXT, target_id TEXT,` +
			` target_user_id TEXT, request_path TEXT, request_method TEXT, ` +
			`outcome TEXT, details TEXT, created_at DATETIME)`,
		`INSERT INTO users VALUES ('owner',1,NULL), ('other',1,NULL), ('viewer',1,NULL), ('inactive',0,NULL)`,
		`INSERT INTO tenant_members VALUES (1,'owner',1,'owner','active',` +
			`NULL), (2,'other',1,'admin','active',NULL), (3,'viewer',1,` +
			`'viewer','active',NULL), (4,'inactive',1,'admin','active',NULL), ` +
			`(5,'owner',2,'owner','active',NULL)`,
		`INSERT INTO im_channels VALUES ('octo',1,'octo',NULL,` +
			`'{"account_id":"a","bot_uid":"b"}'), ('foreign',2,'octo',NULL,` +
			`'{}'), ('slack',1,'slack',NULL,'{}'), ('deleted',1,'octo',` +
			`CURRENT_TIMESTAMP,'{}')`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	migration, err := os.ReadFile("../../migrations/sqlite/000052_im_channel_web_owners.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	svc := im.NewService(db, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := NewIMHandler(svc)
	var user, principalType string
	var tenant uint64 = 1
	var apiKey bool
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := types.WithPrincipal(c.Request.Context(), types.Principal{Type: principalType, ID: user})
		ctx = types.WithCaller(ctx, types.Caller{TenantID: tenant, UserID: user, Role: types.TenantRoleAdmin})
		if apiKey {
			ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 1, FullAccess: true})
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.GET("/im-channels/:id/web-owner", h.GetWebOwner)
	r.PUT("/im-channels/:id/web-owner", h.BindWebOwner)
	r.DELETE("/im-channels/:id/web-owner", h.UnbindWebOwner)
	call := func(method, channel, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/im-channels/"+channel+"/web-owner", strings.NewReader(body)))
		return w
	}
	user, principalType = "owner", types.PrincipalWebUser
	w := call(http.MethodGet, "octo", "")
	require.Equal(t, http.StatusOK, w.Code)
	var response map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, map[string]any{"bound": false}, response)
	require.Equal(t, http.StatusNoContent, call(http.MethodPut, "octo", `{"web_user_id":"other"}`).Code)
	bound, err := im.WebOwnerExists(context.Background(), db, 1, "octo", "owner")
	require.NoError(t, err)
	require.True(t, bound)
	bound, err = im.WebOwnerExists(context.Background(), db, 1, "octo", "other")
	require.NoError(t, err)
	require.False(t, bound)
	require.JSONEq(t, `{"bound":true}`, call(http.MethodGet, "octo", "").Body.String())
	require.NoError(t, db.Exec(`UPDATE users SET is_active=0 WHERE id='owner'`).Error)
	bound, err = im.WebOwnerExists(context.Background(), db, 1, "octo", "owner")
	require.NoError(t, err)
	require.False(t, bound)
	require.Equal(t, http.StatusForbidden, call(http.MethodGet, "octo", "").Code)
	require.NoError(t, db.Exec(`UPDATE users SET is_active=1 WHERE id='owner'`).Error)
	tenant = 2
	require.Equal(t, http.StatusForbidden, call(http.MethodGet, "octo", "").Code)
	require.Equal(t, http.StatusForbidden, call(http.MethodPut, "octo", "").Code)
	tenant = 1
	user = "other"
	require.JSONEq(t, `{"bound":false}`, call(http.MethodGet, "octo", "").Body.String())
	require.Equal(t, http.StatusNoContent, call(http.MethodDelete, "octo", "").Code)
	user = "viewer"
	require.Equal(t, http.StatusForbidden, call(http.MethodPut, "octo", "").Code)
	user = "inactive"
	require.Equal(t, http.StatusForbidden, call(http.MethodPut, "octo", "").Code)
	require.Equal(t, http.StatusForbidden, call(http.MethodGet, "octo", "").Code)
	require.Equal(t, http.StatusForbidden, call(http.MethodDelete, "octo", "").Code)
	user = "owner"
	for _, channel := range []string{"foreign", "slack", "deleted"} {
		require.Equal(t, http.StatusForbidden, call(http.MethodPut, channel, "").Code)
		require.Equal(t, http.StatusForbidden, call(http.MethodGet, channel, "").Code)
	}
	apiKey = true
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		require.Equal(t, http.StatusForbidden, call(method, "octo", "").Code)
	}
	apiKey = false
	principalType = types.PrincipalAPIExternalUser
	require.Equal(t, http.StatusForbidden, call(http.MethodPut, "octo", "").Code)
	principalType = types.PrincipalWebUser
	require.NoError(t,
		db.Exec(`UPDATE tenant_members SET status='suspended' WHERE user_id='owner' AND tenant_id=1`).Error,
	)
	require.Equal(t, http.StatusForbidden, call(http.MethodGet, "octo", "").Code)
	bound, err = im.WebOwnerExists(context.Background(), db, 1, "octo", "owner")
	require.NoError(t, err)
	require.False(t, bound)
	require.NoError(t, db.Exec(`UPDATE tenant_members SET status='active' WHERE user_id='owner' AND tenant_id=1`).Error)
	require.Equal(t, http.StatusNoContent, call(http.MethodDelete, "octo", "").Code)
	require.JSONEq(t, `{"bound":false}`, call(http.MethodGet, "octo", "").Body.String())
	var actions []string
	require.NoError(t, db.Table("audit_logs").Order("id").Pluck("action", &actions).Error)
	require.Equal(t, []string{"im_channel.web_owner_bound", "im_channel.web_owner_unbound"}, actions)
	// Each identity change permanently revokes existing grants, including if the
	// old identity is later restored. Recreating the same ID cannot revive them.
	require.Equal(t, http.StatusNoContent, call(http.MethodPut, "octo", "").Code)
	// Ordinary settings, formatting, and JSON key order are not identity changes.
	require.NoError(t,
		db.Exec(`UPDATE im_channels SET credentials='{"bot_uid":"b",`+
			`"allow_file_delivery":true,"account_id":"a"}' WHERE id='octo'`).Error,
	)
	require.JSONEq(t, `{"bound":true}`, call(http.MethodGet, "octo", "").Body.String())
	require.NoError(t,
		db.Exec(`UPDATE im_channels SET credentials='{"account_id":"a",`+
			`"bot_uid":"b","allow_file_delivery":false}' WHERE id='octo'`).Error,
	)
	require.JSONEq(t, `{"bound":true}`, call(http.MethodGet, "octo", "").Body.String())
	require.NoError(t,
		db.Exec(`UPDATE im_channels SET credentials='{"account_id":"new","bot_uid":"b"}' WHERE id='octo'`).Error,
	)
	require.JSONEq(t, `{"bound":false}`, call(http.MethodGet, "octo", "").Body.String())
	require.NoError(t,
		db.Exec(`UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"b"}' WHERE id='octo'`).Error,
	)
	require.JSONEq(t, `{"bound":false}`, call(http.MethodGet, "octo", "").Body.String())
	require.Equal(t, http.StatusNoContent, call(http.MethodPut, "octo", "").Code)
	require.NoError(t,
		db.Exec(`UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"new"}' WHERE id='octo'`).Error,
	)
	require.JSONEq(t, `{"bound":false}`, call(http.MethodGet, "octo", "").Body.String())
	require.Equal(t, http.StatusNoContent, call(http.MethodPut, "octo", "").Code)
	require.NoError(t, db.Exec(`DELETE FROM im_channels WHERE id='octo'`).Error)
	require.NoError(t,
		db.Exec(`INSERT INTO im_channels(id,tenant_id,platform,deleted_at,`+
			`credentials) VALUES ('octo',1,'octo',NULL,'{"account_id":"a",`+
			`"bot_uid":"new"}')`).Error,
	)
	require.JSONEq(t, `{"bound":false}`, call(http.MethodGet, "octo", "").Body.String())
	actions = nil
	require.NoError(t,
		db.Table("audit_logs").Where("action = ?", "im_channel.web_owner_revoked").Pluck("action", &actions).Error,
	)
	require.Len(t, actions, 3)
	for _, invalid := range []string{
		`{`, `{}`, `[]`, `null`, `{"account_id":3,"bot_uid":"b"}`,
		`{"account_id":"a","bot_uid":null}`, `{"account_id":"","bot_uid":"b"}`,
		`{"account_id":" \t\r\n","bot_uid":"b"}`, `{"account_id":"\u00a0","bot_uid":"b"}`,
		`{"account_id":"ok\u0001","bot_uid":"b"}`, `{"account_id":"a","bot_uid":"\u2003"}`,
		`{"account_id":"\u200b","bot_uid":"b"}`,
		`{"account_id":"a","account_id":"evil","bot_uid":"b"}`,
	} {
		require.Equal(t, http.StatusNoContent, call(http.MethodPut, "octo", "").Code)
		require.NoError(t, db.Exec(`UPDATE im_channels SET credentials=? WHERE id='octo'`, invalid).Error)
		require.JSONEq(t, `{"bound":false}`, call(http.MethodGet, "octo", "").Body.String())
		require.Equal(t, http.StatusForbidden, call(http.MethodPut, "octo", "").Code)
		require.NoError(t,
			db.Exec(`UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"new"}' WHERE id='octo'`).Error,
		)
	}
	require.NoError(t,
		db.Exec(`UPDATE im_channels SET credentials='{"account_id":"账户","bot_uid":"机器人"}' WHERE id='octo'`).Error,
	)
	require.Equal(t, http.StatusNoContent, call(http.MethodPut, "octo", "").Code)
	require.Equal(t, http.StatusNoContent, call(http.MethodDelete, "octo", "").Code)
	require.NoError(t,
		db.Exec(`UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"new"}' WHERE id='octo'`).Error,
	)
	// Channel changes also roll back when revocation cannot be audited.
	require.Equal(t, http.StatusNoContent, call(http.MethodPut, "octo", "").Code)
	require.NoError(t, db.Exec(`DROP TABLE audit_logs`).Error)
	require.Error(t,
		db.Exec(`UPDATE im_channels SET credentials='{"account_id":"other","bot_uid":"other"}' WHERE id='octo'`).Error,
	)
	bound, err = im.WebOwnerExists(context.Background(), db, 1, "octo", "owner")
	require.NoError(t, err)
	require.True(t, bound)
	// A failed audit insert must roll the binding back.
	require.Equal(t, http.StatusInternalServerError, call(http.MethodDelete, "octo", "").Code)
	bound, err = im.WebOwnerExists(context.Background(), db, 1, "octo", "owner")
	require.NoError(t, err)
	require.True(t, bound)
}
