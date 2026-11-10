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
	p := directoryQuery{limit: limit, offset: offset, q: strings.TrimSpace(c.Query("q")),
		account: c.Query("account_id"), group: c.Query("group_id"), subarea: c.Query("subarea_id"), filter: c.Query("filter")}
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

func (h *Handler) DirectoryGroups(c *gin.Context) {
	p, err := parseDirectoryQuery(c)
	if err != nil {
		respond(c, 0, nil, err)
		return
	}
	page, err := h.store.groups(c.Request.Context(), tenant(c), p)
	respond(c, http.StatusOK, page, err)
}

func (h *Handler) DirectoryBots(c *gin.Context) {
	p, err := parseDirectoryQuery(c)
	if err != nil {
		respond(c, 0, nil, err)
		return
	}
	page, err := h.store.bots(c.Request.Context(), tenant(c), p)
	respond(c, http.StatusOK, page, err)
}

func (h *Handler) DirectoryAreas(c *gin.Context) {
	p, err := parseDirectoryQuery(c)
	if err != nil || p.group == "" {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	page, err := h.store.areas(c.Request.Context(), tenant(c), p)
	respond(c, http.StatusOK, page, err)
}

func (h *Handler) DirectoryComparison(c *gin.Context) {
	p, err := parseDirectoryQuery(c)
	if err != nil || p.group == "" {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	page, err := h.store.compare(c.Request.Context(), tenant(c), p)
	respond(c, http.StatusOK, page, err)
}

func (h *Handler) Configuration(c *gin.Context) {
	snapshot, err := h.store.configuration(c.Request.Context(), tenant(c), c.Param("scope_id"))
	respond(c, http.StatusOK, snapshot, err)
}

func (h *Handler) PreviewConfiguration(c *gin.Context) { h.changeConfiguration(c, false) }
func (h *Handler) SaveConfiguration(c *gin.Context)    { h.changeConfiguration(c, true) }

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
	preview, err := h.store.changeConfiguration(c.Request.Context(), types.CallerFromContext(c.Request.Context()), tenant(c),
		c.Param("scope_id"), req.Revision, req.Token, req.Configuration, save)
	respond(c, http.StatusOK, preview, err)
}

func (h *Handler) RemoveScope(c *gin.Context) {
	revision := c.Query("revision")
	if len(revision) != 64 {
		respond(c, 0, nil, ErrInvalid)
		return
	}
	err := h.store.removeScope(c.Request.Context(), types.CallerFromContext(c.Request.Context()), tenant(c), c.Param("scope_id"), revision)
	respond(c, http.StatusOK, gin.H{"removed": true}, err)
}
