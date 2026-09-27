package octointegration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestScopeUpdateReturnsPersistedPublicWebPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := testStore(t)
	scope := createScope(t, store, 1, "bot", "group", "", false)
	h := &Handler{store: store}
	r := gin.New()
	r.PUT("/scopes/:scope_id", func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		h.Update(c)
	})
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"display_name":"configured name","inherit_parent":false,"allow_public_web":true}`, true},
		{`{"display_name":"configured name","inherit_parent":false}`, true},
		{`{"display_name":"configured name","inherit_parent":false,"allow_public_web":false}`, false},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/scopes/"+scope.ID, bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("update status %d: %s", w.Code, w.Body.String())
		}
		var response struct {
			Data Scope `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.AllowPublicWeb != tc.want {
			t.Fatalf("readback = %+v: %v", response.Data, err)
		}
		persisted, err := store.Get(context.Background(), 1, scope.ID)
		if err != nil || persisted.AllowPublicWeb != tc.want {
			t.Fatalf("persisted = %+v: %v", persisted, err)
		}
	}
}
