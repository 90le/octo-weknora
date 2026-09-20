package octobusiness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConsoleRequiresAuthenticatedWorkspaceAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := testService(t)
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor, types.TenantRoleAdmin} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1, UserID: "admin", Role: role})
		c.Request = httptest.NewRequest(http.MethodGet, "/issues", nil).WithContext(ctx)
		NewHandler(service).ListIssues(c)
		if role == types.TenantRoleAdmin {
			require.Equal(t, http.StatusOK, rec.Code)
		} else {
			require.Equal(t, http.StatusForbidden, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1, UserID: "admin", Role: types.TenantRoleAdmin})
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{FullAccess: false, KnowledgeBaseIDs: []string{"kb-a"}})
	c.Request = httptest.NewRequest(http.MethodGet, "/issues", nil).WithContext(ctx)
	NewHandler(service).ListIssues(c)
	require.Equal(t, http.StatusForbidden, rec.Code)
}
