package octointegration

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func parseDirectoryQuery(c *gin.Context) (directoryQuery, error) {
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, offsetErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	p := directoryQuery{
		limit: limit, offset: offset, q: strings.TrimSpace(c.Query("q")),
		account: c.Query("account_id"), group: c.Query("group_id"),
		subarea: c.Query("subarea_id"), filter: c.Query("filter"),
	}
	if limitErr != nil || offsetErr != nil || limit < 1 || limit > 100 || offset < 0 || len(p.q) > 256 {
		return p, ErrInvalid
	}
	switch p.filter {
	case "", "multiple", "pending", "verified", "connected":
	default:
		return p, ErrInvalid
	}
	for _, id := range []string{p.account, p.group, p.subarea} {
		if id != "" && !validID(id) {
			return p, ErrInvalid
		}
	}
	return p, nil
}

// DirectoryGroups lists matching groups in the authenticated workspace.
func (h *Handler) DirectoryGroups(c *gin.Context) {
	p, err := parseDirectoryQuery(c)
	if err != nil {
		respond(c, 0, nil, err)
		return
	}
	page, err := h.store.groups(c.Request.Context(), tenant(c), p)
	respond(c, http.StatusOK, page, err)
}

// DirectoryBots lists saved Bot connections and configured scope accounts.
func (h *Handler) DirectoryBots(c *gin.Context) {
	p, err := parseDirectoryQuery(c)
	if err != nil {
		respond(c, 0, nil, err)
		return
	}
	page, err := h.store.bots(c.Request.Context(), tenant(c), p)
	respond(c, http.StatusOK, page, err)
}

// DirectoryAreas lists configured subareas of a native group.
func (h *Handler) DirectoryAreas(c *gin.Context) {
	p, err := parseDirectoryQuery(c)
	if err != nil || p.group == "" {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	page, err := h.store.areas(c.Request.Context(), tenant(c), p)
	respond(c, http.StatusOK, page, err)
}

// DirectoryComparison compares effective permissions in the chosen region.
func (h *Handler) DirectoryComparison(c *gin.Context) {
	p, err := parseDirectoryQuery(c)
	if err != nil || p.group == "" {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	page, err := h.store.compare(c.Request.Context(), tenant(c), p)
	respond(c, http.StatusOK, page, err)
}

// Configuration reads one region's configuration and its revision.
func (h *Handler) Configuration(c *gin.Context) {
	snapshot, err := h.store.configuration(c.Request.Context(), tenant(c), c.Param("scope_id"))
	respond(c, http.StatusOK, snapshot, err)
}

// PreviewConfiguration returns the effects without changing data.
func (h *Handler) PreviewConfiguration(c *gin.Context) { h.changeConfiguration(c, false) }

// SaveConfiguration commits a still-current reviewed configuration.
func (h *Handler) SaveConfiguration(c *gin.Context) { h.changeConfiguration(c, true) }

func (h *Handler) changeConfiguration(c *gin.Context, save bool) {
	var req struct {
		Revision      string             `json:"revision"`
		Token         string             `json:"token"`
		Configuration ScopeConfiguration `json:"configuration"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Revision) != 64 || (save && len(req.Token) != 64) {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	ctx := c.Request.Context()
	preview, err := h.store.changeConfiguration(ctx, types.CallerFromContext(ctx), tenant(c),
		c.Param("scope_id"), req.Revision, req.Token, req.Configuration, save)
	respond(c, http.StatusOK, preview, err)
}

// RemoveScope removes only the reviewed region and its local grants.
func (h *Handler) RemoveScope(c *gin.Context) {
	revision := c.Query("revision")
	if len(revision) != 64 {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	ctx := c.Request.Context()
	err := h.store.removeScope(ctx, types.CallerFromContext(ctx), tenant(c), c.Param("scope_id"), revision)
	respond(c, http.StatusOK, gin.H{"removed": true}, err)
}
