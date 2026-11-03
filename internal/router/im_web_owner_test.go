package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIMWebOwnerRoutesRejectAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	g.ensureAPIKeyAuthorizer()
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ctx := types.WithTenantAPIKeyScope(c.Request.Context(), types.TenantAPIKeyScope{KeyID: 1, FullAccess: true})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}, g.apiKeyAuthorizer.Middleware())
	RegisterIMChannelRoutes(v1, &handler.IMHandler{}, g)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		_, registered := g.apiKeyAuthorizer.Lookup(method, "/api/v1/im-channels/:id/web-owner")
		require.False(t, registered)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/im-channels/channel/web-owner", nil))
		require.Equal(t, http.StatusForbidden, w.Code)
	}
}
