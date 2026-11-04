package session

import (
	"context"
	stderrors "errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

// DeleteChannelArtifact deletes only the addressed channel artifact; it does
// not give the caller permission to open or modify its conversation.
func (h *Handler) DeleteChannelArtifact(c *gin.Context) {
	ctx := c.Request.Context()
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 0 {
		_ = c.Error(errors.NewBadRequestError("invalid artifact index"))
		return
	}
	user, ok := types.PrincipalFromContext(ctx)
	if !ok || user.Type != types.PrincipalWebUser || h.userService == nil {
		_ = c.Error(errors.NewNotFoundError("artifact not accessible"))
		return
	}
	token := sandboxTerminalBearerToken(c)
	if token == "" {
		_ = c.Error(errors.NewNotFoundError("artifact not accessible"))
		return
	}
	record, err := h.userService.GetAccessTokenByValue(ctx, token)
	if err != nil || record == nil || record.ID == "" ||
		service.AssertAccessTokenStillActive(record, user.ID, time.Now()) != nil {
		_ = c.Error(errors.NewNotFoundError("artifact not accessible"))
		return
	}
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		_ = c.Error(errors.NewNotFoundError("artifact not accessible"))
		return
	}
	expiry, err := claims.GetExpirationTime()
	if err != nil || expiry == nil || !expiry.Time.After(time.Now()) {
		_ = c.Error(errors.NewNotFoundError("artifact not accessible"))
		return
	}
	deleter, ok := h.messageService.(interface {
		DeleteChannelArtifact(
			context.Context, *types.ArtifactDeleteRequest, string, time.Time,
		) (*types.ArtifactDeleteResult, error)
	})
	if !ok {
		_ = c.Error(errors.NewNotFoundError("artifact not accessible"))
		return
	}
	result, err := deleter.DeleteChannelArtifact(ctx, &types.ArtifactDeleteRequest{
		SessionID: paramSessionID(c), MessageID: c.Param("message_id"), Index: index,
		AllVersions: artifactAllVersions(c, true),
	}, record.ID, expiry.Time)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) || stderrors.Is(err, service.ErrArtifactNotFound) {
			_ = c.Error(errors.NewNotFoundError("artifact not accessible"))
		} else {
			logger.Errorf(ctx, "channel artifact deletion failed: %v", err)
			_ = c.Error(errors.NewInternalServerError("artifact deletion failed"))
		}
		return
	}
	// A legacy provider path cannot prove whether knowledge or another owner
	// uses the same bytes. Retain it unless the resource catalog can account
	// for the object; the existing reclaim path checks its remaining bindings.
	tenant := types.MustTenantIDFromContext(ctx)
	var safe []types.ArtifactBlobRef
	if h.resourceCatalog != nil {
		for _, ref := range result.Reclaim {
			if !strings.HasPrefix(ref.URL, types.ResourceScheme) {
				continue
			}
			_, resource, err := h.resourceCatalog.ResolvePath(ctx, ref.URL)
			if err == nil && resource != nil && resource.TenantID == tenant {
				safe = append(safe, ref)
			}
		}
	}
	h.reclaimArtifactBlobs(ctx, tenant, safe)
	c.JSON(http.StatusOK, gin.H{
		"success": true, "data": gin.H{"file_name": result.FileName, "deleted": result.Deleted},
	})
}
