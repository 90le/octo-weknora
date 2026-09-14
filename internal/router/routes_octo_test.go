package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestOctoRejectsNonAdminEvenWithLegacyRBACDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run(string(role),func(t *testing.T){
			off := false
			g := &rbacGuards{cfg:&config.Config{Tenant:&config.TenantConfig{EnableRBAC:&off}}}
			r:=gin.New()
			r.Use(func(c *gin.Context){
				ctx:=context.WithValue(c.Request.Context(),types.TenantRoleContextKey,role)
				c.Request=c.Request.WithContext(ctx)
				c.Set(types.TenantIDContextKey.String(),uint64(1))
			})
			RegisterOctoRoutes(r.Group("/api/v1"),&octointegration.Handler{},g)
			w:=httptest.NewRecorder()
			r.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/octo/scopes",nil))
			if w.Code!=http.StatusForbidden {t.Fatalf("status = %d",w.Code)}
			if *g.cfg.Tenant.EnableRBAC {t.Fatal("registration mutated global RBAC config")}
		})
	}
}

func TestOctoRequiresWorkspace(t *testing.T) {
	r:=gin.New()
	r.Use(func(c *gin.Context){c.Request=c.Request.WithContext(context.WithValue(c.Request.Context(),types.TenantRoleContextKey,types.TenantRoleAdmin))})
	RegisterOctoRoutes(r.Group("/api/v1"),&octointegration.Handler{},&rbacGuards{})
	w:=httptest.NewRecorder()
	r.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/octo/scopes",nil))
	if w.Code!=http.StatusUnauthorized {t.Fatalf("status = %d",w.Code)}
}
