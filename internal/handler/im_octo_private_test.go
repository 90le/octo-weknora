package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestOctoPrivateSaveRepairsKnowledgeWhenLegacyBindingWasDeleted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "private.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&octointegration.Connection{}))
	for _, sql := range []string{
		`CREATE TABLE im_channels (id TEXT PRIMARY KEY, tenant_id INTEGER, agent_id TEXT, platform TEXT, name TEXT,
		  enabled BOOLEAN, mode TEXT, output_mode TEXT, locale TEXT, session_mode TEXT, knowledge_base_id TEXT,
		  credentials TEXT, bot_identity TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE knowledge_bases (id TEXT PRIMARY KEY, tenant_id INTEGER, deleted_at DATETIME)`,
		`INSERT INTO knowledge_bases VALUES ('removed',1,CURRENT_TIMESTAMP), ('active',1,NULL), ('foreign',2,NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	require.NoError(t, db.Create(&octointegration.Connection{
		TenantID: 1, AccountID: "acct", Token: "test-credential",
		VerifiedIdentity: types.JSON(`{"bot_uid":"bot","owner_uid":"owner"}`),
	}).Error)
	channel := &im.IMChannel{
		ID: "channel", TenantID: 1, AgentID: "agent", Platform: "octo", Name: "fixture",
		Mode: "websocket", SessionMode: "user", KnowledgeBaseID: "removed",
		Credentials: types.JSON(`{"account_id":"acct","bot_uid":"bot","allowed_dm_uids":["owner"],
			"dm_knowledge_base_ids":["removed"],"allowed_bot_uids":["for-group"]}`),
	}
	require.NoError(t, db.Create(channel).Error)
	service := im.NewService(db, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(service.Stop)
	handler := NewIMHandler(service)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: 1, Role: types.TenantRoleAdmin})
		ctx = context.WithValue(ctx, types.TenantIDContextKey, uint64(1))
		c.Request = c.Request.WithContext(ctx)
	})
	r.POST("/preview", handler.PreviewOctoPrivatePolicy)
	r.PUT("/channels/:id", handler.UpdateIMChannel)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(data))))
		return w
	}
	policy := map[string]any{
		"account_id": "acct", "bot_uid": "bot", "dm_access_mode": "allowlist", "dm_allowlist_uids": []string{"owner"},
		"dm_knowledge_base_ids": []string{"removed"}, "allowed_bot_uids": []string{"for-group"},
	}
	input := map[string]any{"channel_id": "channel", "test_uid": "owner", "credentials": policy}
	for _, invalid := range []string{"removed", "foreign"} {
		policy["dm_knowledge_base_ids"] = []string{invalid}
		w := call(http.MethodPost, "/preview", input)
		require.Equal(t, http.StatusOK, w.Code)
		require.Contains(t, w.Body.String(), `"problem":"invalid_knowledge"`)
		require.NotContains(t, w.Body.String(), `"token":`)
	}
	policy["dm_knowledge_base_ids"] = []string{"active"}
	w := call(http.MethodPost, "/preview", input)
	require.Equal(t, http.StatusOK, w.Code)
	var preview struct {
		Data im.OctoPrivatePreview `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &preview))
	require.NotEmpty(t, preview.Data.Token)
	require.Equal(t, "allowed", preview.Data.Decision)
	update := map[string]any{
		"knowledge_base_id": "new-unknown-binding", "credentials": policy, "private_preview": preview.Data.Token,
	}
	w = call(http.MethodPut, "/channels/channel", update)
	require.Equal(t, http.StatusBadRequest, w.Code)
	// Compatibility with older editors that echo the stored generic binding.
	update["knowledge_base_id"] = "removed"
	w = call(http.MethodPut, "/channels/channel", update)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	saved, err := service.GetChannelByIDAndTenant("channel", 1)
	require.NoError(t, err)
	require.Equal(t, "removed", saved.KnowledgeBaseID)
	config, err := im.ParseCredentials(saved.Credentials)
	require.NoError(t, err)
	require.Equal(t, []any{"active"}, config["dm_knowledge_base_ids"])
	require.Equal(t, []any{"for-group"}, config["allowed_bot_uids"])
}
