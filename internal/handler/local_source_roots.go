package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type LocalRootHandler struct {
	registry *localfolder.Registry
	audit    interfaces.AuditLogService
}

func NewLocalRootHandler(registry *localfolder.Registry, audit interfaces.AuditLogService) *LocalRootHandler {
	return &LocalRootHandler{registry: registry, audit: audit}
}

func (h *LocalRootHandler) authorize(c *gin.Context) (uint64, bool) {
	tenant, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenant == 0 || !types.IsSystemAdminFromContext(c.Request.Context()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "system administrator and active workspace required"})
		return 0, false
	}
	return tenant, true
}
func rootError(c *gin.Context, err error) {
	status, message, code := http.StatusInternalServerError, "server folder operation failed", "folder_operation_failed"
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, message = http.StatusNotFound, "server folder not found in this workspace"
		code = "folder_not_found"
	case errors.Is(err, localfolder.ErrRootInvalid):
		status, message = http.StatusBadRequest, "choose a mounted space, a relative directory and a name"
		code = "invalid_folder_input"
	case errors.Is(err, localfolder.ErrRootUnsafe):
		status, message, code = http.StatusBadRequest, "symbolic links and unsafe directory paths are not allowed", "unsafe_folder"
	case errors.Is(err, localfolder.ErrRootUnavailable):
		status, message = http.StatusBadRequest, "folder unavailable: check the mount, read permissions and symbolic links"
		code = "folder_unavailable"
	case errors.Is(err, localfolder.ErrRootInUse):
		status, message = http.StatusConflict, "this folder is used by a data source; remove that source first, or disable this folder to stop access"
		code = "folder_in_use"
	case errors.Is(err, gorm.ErrDuplicatedKey), errors.Is(err, localfolder.ErrRootExists):
		status, message = http.StatusConflict, "this directory is already registered in this workspace"
		code = "folder_already_registered"
	}
	c.JSON(status, gin.H{"error": message, "code": code})
}
func (h *LocalRootHandler) Spaces(c *gin.Context) {
	if _, ok := h.authorize(c); !ok {
		return
	}
	v, err := h.registry.SpaceSummaries(c.Request.Context())
	if err != nil {
		rootError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *LocalRootHandler) List(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	v, err := h.registry.RootSummaries(c.Request.Context(), tenant)
	if err != nil {
		rootError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *LocalRootHandler) Probe(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	var req localfolder.Registration
	if c.ShouldBindJSON(&req) != nil {
		rootError(c, localfolder.ErrRootInvalid)
		return
	}
	if err := h.registry.Probe(c.Request.Context(), tenant, req); err != nil {
		rootError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"readable": true})
}
func (h *LocalRootHandler) Create(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	var req localfolder.Registration
	if c.ShouldBindJSON(&req) != nil {
		rootError(c, localfolder.ErrRootInvalid)
		return
	}
	v, err := h.registry.Create(c.Request.Context(), tenant, req)
	if err != nil {
		rootError(c, err)
		return
	}
	h.log(c, tenant, "created", v.ID)
	c.JSON(http.StatusCreated, v)
}
func (h *LocalRootHandler) Update(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Enabled == nil {
		rootError(c, localfolder.ErrRootInvalid)
		return
	}
	v, err := h.registry.Update(c.Request.Context(), tenant, c.Param("root_id"), req.Name, *req.Enabled)
	if err != nil {
		rootError(c, err)
		return
	}
	h.log(c, tenant, "updated", v.ID)
	c.JSON(http.StatusOK, v)
}
func (h *LocalRootHandler) Delete(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	id := c.Param("root_id")
	if err := h.registry.Delete(c.Request.Context(), tenant, id); err != nil {
		rootError(c, err)
		return
	}
	h.log(c, tenant, "deleted", id)
	c.JSON(http.StatusOK, gin.H{"deleted": true, "source_files_preserved": true})
}
func (h *LocalRootHandler) log(c *gin.Context, tenant uint64, action, id string) {
	if h.audit == nil {
		return
	}
	actor, _ := types.UserIDFromContext(c.Request.Context())
	_ = h.audit.Log(c.Request.Context(), &types.AuditLog{TenantID: tenant, ActorUserID: actor, Action: types.AuditAction("datasource.root." + action), TargetType: "local_source_root", TargetID: id, Outcome: types.AuditOutcomeSuccess})
}

func (h *LocalRootHandler) DiscoverSpaces(c *gin.Context) {
	if _, ok := h.authorize(c); !ok {
		return
	}
	v, err := h.registry.DiscoverSpaces(c.Request.Context())
	if err != nil {
		rootError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *LocalRootHandler) RegisterSpace(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	var req localfolder.SpaceRegistration
	if c.ShouldBindJSON(&req) != nil {
		rootError(c, localfolder.ErrRootInvalid)
		return
	}
	v, err := h.registry.RegisterDiscoveredSpace(c.Request.Context(), req)
	if err != nil {
		rootError(c, err)
		return
	}
	if h.audit != nil {
		actor, _ := types.UserIDFromContext(c.Request.Context())
		_ = h.audit.Log(c.Request.Context(), &types.AuditLog{TenantID: tenant, ActorUserID: actor, Action: types.AuditAction("datasource.space.created"), TargetType: "local_source_space", TargetID: v.ID, Outcome: types.AuditOutcomeSuccess})
	}
	c.JSON(http.StatusCreated, v)
}
func browseOffset(c *gin.Context) (int, error) {
	value, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil {
		return 0, localfolder.ErrRootInvalid
	}
	return value, nil
}
func (h *LocalRootHandler) BrowseSpace(c *gin.Context) {
	if _, ok := h.authorize(c); !ok {
		return
	}
	offset, err := browseOffset(c)
	if err != nil {
		rootError(c, err)
		return
	}
	v, err := h.registry.BrowseSpace(c.Request.Context(), c.Param("space_id"), c.Query("directory"), offset)
	if err != nil {
		rootError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *LocalRootHandler) BrowseRoot(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	h.browseTenantRoot(c, tenant)
}

// Separate from the host-administration endpoints: only a current enabled
// grant in this workspace can be traversed, and all output paths are relative.
func (h *LocalRootHandler) BrowseGrantedRoot(c *gin.Context) {
	ctx := c.Request.Context()
	tenant, ok := types.TenantIDFromContext(ctx)
	if !ok || tenant == 0 || (!types.TenantRoleFromContext(ctx).HasPermission(types.TenantRoleAdmin) && !types.IsSystemAdminFromContext(ctx)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "workspace data source administrator required", "code": "folder_access_denied"})
		return
	}
	h.browseTenantRoot(c, tenant)
}
func (h *LocalRootHandler) browseTenantRoot(c *gin.Context, tenant uint64) {
	offset, err := browseOffset(c)
	if err != nil {
		rootError(c, err)
		return
	}
	v, err := h.registry.BrowseRoot(c.Request.Context(), tenant, c.Param("root_id"), c.Query("directory"), offset)
	if err != nil {
		rootError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
