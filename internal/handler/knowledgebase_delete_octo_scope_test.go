package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type kbDeleteServiceStub struct {
	interfaces.KnowledgeBaseService
	kb      *types.KnowledgeBase
	deletes int
}

func (s *kbDeleteServiceStub) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

func (s *kbDeleteServiceStub) DeleteKnowledgeBase(context.Context, string) error {
	s.deletes++
	return nil
}

type octoScopeImpactStub struct {
	uses  []octointegration.EffectiveUse
	err   error
	calls int
}

func (s *octoScopeImpactStub) EffectiveUses(_ context.Context, _ uint64, _ string) ([]octointegration.EffectiveUse, error) {
	s.calls++
	return s.uses, s.err
}

func deleteKnowledgeBaseTestResponse(svc *kbDeleteServiceStub, impact octoScopeImpactReader) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		c.Set(types.UserIDContextKey.String(), "owner")
		c.Next()
	})
	r.DELETE("/knowledge-bases/:id", (&KnowledgeBaseHandler{service: svc, octoScopeImpact: impact}).DeleteKnowledgeBase)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/knowledge-bases/kb", nil))
	return w
}

func TestKBDeleteBlocksEveryOctoDependencyType(t *testing.T) {
	for _, mode := range []string{"direct", "inherited", "none"} {
		t.Run(mode, func(t *testing.T) {
			svc := &kbDeleteServiceStub{kb: &types.KnowledgeBase{ID: "kb", TenantID: 1, Name: "Product"}}
			impact := &octoScopeImpactStub{uses: []octointegration.EffectiveUse{{ScopeID: "scope", QueryMode: mode, CanManage: mode == "none"}}}
			w := deleteKnowledgeBaseTestResponse(svc, impact)
			require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "解绑")
			require.Contains(t, w.Body.String(), "撤销")
			require.Equal(t, 1, impact.calls)
			require.Zero(t, svc.deletes)
		})
	}
}

func TestKBDeleteAllowsUnreferencedKBAndFailsClosedOnImpactError(t *testing.T) {
	svc := &kbDeleteServiceStub{kb: &types.KnowledgeBase{ID: "kb", TenantID: 1, Name: "Product"}}
	impact := &octoScopeImpactStub{}
	w := deleteKnowledgeBaseTestResponse(svc, impact)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, svc.deletes)

	svc.deletes = 0
	impact.err = errors.New("database unavailable")
	w = deleteKnowledgeBaseTestResponse(svc, impact)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	require.Zero(t, svc.deletes)
	w = deleteKnowledgeBaseTestResponse(svc, nil)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	require.Zero(t, svc.deletes)
}

func TestKBDeleteChecksOwnershipBeforeOctoImpact(t *testing.T) {
	svc := &kbDeleteServiceStub{kb: &types.KnowledgeBase{ID: "kb", TenantID: 2, Name: "Foreign"}}
	impact := &octoScopeImpactStub{uses: []octointegration.EffectiveUse{{ScopeID: "private", QueryMode: "direct"}}}
	w := deleteKnowledgeBaseTestResponse(svc, impact)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	require.Zero(t, impact.calls, "foreign Octo scope metadata must not be read")
	require.Zero(t, svc.deletes)
}
