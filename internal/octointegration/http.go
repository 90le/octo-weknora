package octointegration

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	store    *Store
	platform *platformClient
}

func NewHandler(db *gorm.DB) *Handler {
	return &Handler{store: NewStore(db), platform: newPlatformClient()}
}

func tenant(c *gin.Context) uint64 { return c.GetUint64(types.TenantIDContextKey.String()) }

func respond(c *gin.Context, status int, data interface{}, err error) {
	if err != nil {
		switch {
		case errors.Is(err, ErrEncryption):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Octo credential encryption is unavailable; configure the native SYSTEM_AES_KEY"})
		case errors.Is(err, ErrPlatform):
			c.JSON(http.StatusBadGateway, gin.H{"error": "Octo verification failed; the last verified metadata has been retained"})
		case errors.Is(err, ErrInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid scope configuration"})
		case errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "scope, connection or knowledge base not found in this workspace"})
		case errors.Is(err, gorm.ErrDuplicatedKey):
			c.JSON(http.StatusConflict, gin.H{"error": "scope already exists"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "unable to complete Octo scope operation"})
		}
		return
	}
	c.JSON(status, gin.H{"success": true, "data": data})
}

// WorkspaceRequired also protects against full-access keys lacking a tenant.
func WorkspaceRequired(c *gin.Context) {
	if tenant(c) == 0 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "workspace required"})
		return
	}
	c.Next()
}

func (h *Handler) List(c *gin.Context) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	rows, err := h.store.List(c.Request.Context(), tenant(c), offset)
	respond(c, http.StatusOK, rows, err)
}

func (h *Handler) Create(c *gin.Context) {
	// Explicit DTO excludes tenant, server IDs and platform verification flags.
	var req struct {
		AccountID     string `json:"account_id"`
		GroupID       string `json:"group_id"`
		SubareaID     string `json:"subarea_id"`
		DisplayName   string `json:"display_name"`
		InheritParent bool   `json:"inherit_parent"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	row, err := h.store.CreateVerified(c.Request.Context(), Scope{TenantID: tenant(c), AccountID: req.AccountID, GroupID: req.GroupID, SubareaID: req.SubareaID, InheritParent: req.InheritParent}, h.platform)
	respond(c, http.StatusCreated, row, err)
}

func (h *Handler) Connections(c *gin.Context) {
	rows, err := h.store.Connections(c.Request.Context(), tenant(c))
	respond(c, http.StatusOK, rows, err)
}
func (h *Handler) PutConnection(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	err := h.store.PutConnection(c.Request.Context(), tenant(c), c.Param("account_id"), req.Token)
	respond(c, http.StatusOK, gin.H{"configured": true}, err)
}
func (h *Handler) SyncName(c *gin.Context) {
	row, err := h.store.SyncName(c.Request.Context(), tenant(c), c.Param("scope_id"), h.platform)
	respond(c, http.StatusOK, row, err)
}
func (h *Handler) MemberRole(c *gin.Context) {
	scope, err := h.store.Get(c.Request.Context(), tenant(c), c.Param("scope_id"))
	if err != nil {
		respond(c, 0, nil, err)
		return
	}
	_, token, err := h.store.connection(c.Request.Context(), tenant(c), scope.AccountID)
	if err != nil {
		respond(c, 0, nil, err)
		return
	}
	role, err := h.platform.member(c.Request.Context(), token, *scope, c.Param("uid"))
	respond(c, http.StatusOK, role, err)
}

func (h *Handler) Update(c *gin.Context) {
	var req struct {
		DisplayName   string `json:"display_name"`
		InheritParent *bool  `json:"inherit_parent"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.InheritParent == nil {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	err := h.store.Update(c.Request.Context(), tenant(c), c.Param("scope_id"), req.DisplayName, *req.InheritParent)
	respond(c, http.StatusOK, gin.H{"updated": true}, err)
}

func (h *Handler) Effective(c *gin.Context) {
	rows, err := h.store.Effective(c.Request.Context(), tenant(c), c.Param("scope_id"))
	respond(c, http.StatusOK, rows, err)
}

func (h *Handler) Bind(c *gin.Context)   { h.binding(c, true) }
func (h *Handler) Unbind(c *gin.Context) { h.binding(c, false) }

func (h *Handler) binding(c *gin.Context, enabled bool) {
	err := h.store.SetBinding(c.Request.Context(), tenant(c), c.Param("scope_id"), c.Param("id"), enabled)
	respond(c, http.StatusOK, gin.H{"bound": enabled}, err)
}

func (h *Handler) Uses(c *gin.Context) {
	rows, err := h.store.Uses(c.Request.Context(), tenant(c), c.Param("id"))
	respond(c, http.StatusOK, rows, err)
}
