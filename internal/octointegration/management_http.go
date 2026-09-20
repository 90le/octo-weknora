package octointegration

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Both methods are routed through native workspace-admin guards; revocation
// additionally uses native KB write authorization in the router.
func (h *Handler) ManagedKnowledgeBases(c *gin.Context) {
	ids, err := h.store.ManagedKnowledgeBases(c.Request.Context(), tenant(c), c.Param("scope_id"))
	respond(c, http.StatusOK, ids, err)
}
func (h *Handler) RevokeKnowledgeManagement(c *gin.Context) {
	err := h.store.RevokeKnowledgeManagement(c.Request.Context(), tenant(c), c.Param("scope_id"), c.Param("id"))
	respond(c, http.StatusOK, gin.H{"revoked": err == nil}, err)
}
