package handler

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalRootManagementNeverGrantsWorkspaceAdminsHostAccess(t *testing.T) {
	for _, action := range []struct {
		name string
		run  func(*LocalRootHandler, *gin.Context)
	}{
		{"spaces", (*LocalRootHandler).Spaces}, {"list", (*LocalRootHandler).List},
		{"probe", (*LocalRootHandler).Probe}, {"create", (*LocalRootHandler).Create},
		{"update", (*LocalRootHandler).Update}, {"delete", (*LocalRootHandler).Delete},
	} {
		t.Run(action.name, func(t *testing.T) {
			for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleAdmin, types.TenantRoleOwner} {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
				ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
				c.Request = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
				action.run(&LocalRootHandler{}, c)
				require.Equal(t, http.StatusForbidden, w.Code)
			}
		})
	}
}
func TestLocalRootSystemAdminRequiresActiveWorkspace(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx := context.WithValue(context.Background(), types.SystemAdminContextKey, true)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	(&LocalRootHandler{}).Spaces(c)
	require.Equal(t, http.StatusForbidden, w.Code)
}
