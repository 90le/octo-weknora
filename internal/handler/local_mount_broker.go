package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/gin-gonic/gin"
)

// brokerCall never accepts a caller-supplied socket, URL, or host. The
// SystemAdmin handler forwards only a small set of documented operations over
// the deployment-configured local Unix socket.
func brokerCall(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	socket := strings.TrimSpace(os.Getenv("OCTO_MOUNT_BROKER_SOCKET"))
	if socket == "" || !strings.HasPrefix(socket, "/") {
		return 0, nil, errMountBrokerUnavailable
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, "http://mount-broker"+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return 0, nil, errMountBrokerUnavailable
	}
	return response.StatusCode, data, nil
}

type brokerUnavailable struct{}

func (brokerUnavailable) Error() string { return "managed server mounts are unavailable" }

var errMountBrokerUnavailable error = brokerUnavailable{}

func (h *LocalRootHandler) brokerGet(c *gin.Context, endpoint string, query url.Values) {
	if _, ok := h.authorize(c); !ok {
		return
	}
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	status, data, err := brokerCall(c.Request.Context(), http.MethodGet, endpoint, nil)
	if err != nil || status >= 500 {
		c.JSON(
			http.StatusServiceUnavailable,
			gin.H{"error": "managed server mounts are unavailable", "code": "mount_broker_unavailable"},
		)
		return
	}
	if status != http.StatusOK || !json.Valid(data) {
		c.JSON(
			http.StatusBadRequest,
			gin.H{"error": "server directory is unavailable or unsafe", "code": "mount_directory_unavailable"},
		)
		return
	}
	c.Data(status, "application/json", data)
}

// ManagedMountRoots lists the broker configured server roots.
func (h *LocalRootHandler) ManagedMountRoots(c *gin.Context) {
	h.brokerGet(c, "/v1/roots", nil)
}

// ManagedMounts lists managed read-only mounts.
func (h *LocalRootHandler) ManagedMounts(c *gin.Context) {
	h.brokerGet(c, "/v1/mounts", nil)
}

// ManagedMountBrowse lists directories in an allowed root.
func (h *LocalRootHandler) ManagedMountBrowse(c *gin.Context) {
	h.brokerGet(
		c,
		"/v1/browse",
		url.Values{
			"root":      {c.Query("root")},
			"directory": {c.Query("directory")},
			"offset":    {c.DefaultQuery("offset", "0")},
		},
	)
}

// CreateManagedMount creates a broker-managed read-only mount.
func (h *LocalRootHandler) CreateManagedMount(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	var input struct {
		Root      string `json:"root"`
		Directory string `json:"directory"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Root) > 32 || len(input.Directory) > 2048 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mount selection"})
		return
	}
	b, _ := json.Marshal(input)
	status, data, err := brokerCall(c.Request.Context(), http.MethodPost, "/v1/mounts", b)
	if err != nil || status >= 500 {
		c.JSON(
			http.StatusServiceUnavailable,
			gin.H{"error": "managed server mounts are unavailable", "code": "mount_broker_unavailable"},
		)
		return
	}
	if status != http.StatusCreated || !json.Valid(data) {
		c.JSON(
			status,
			gin.H{
				"error": "mount could not be created; check the selected directory and existing mounts",
				"code":  "mount_create_failed",
			},
		)
		return
	}
	var result struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &result) != nil || result.ID == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid mount response"})
		return
	}
	h.log(c, tenant, "mount_created", result.ID)
	c.Data(status, "application/json", data)
}

// DeleteManagedMount removes an unused managed mount.
func (h *LocalRootHandler) DeleteManagedMount(c *gin.Context) {
	tenant, ok := h.authorize(c)
	if !ok {
		return
	}
	id := c.Param("mount_id")
	err := h.registry.WithUnusedManagedMount(c.Request.Context(), id, func() error {
		status, _, err := brokerCall(c.Request.Context(), http.MethodDelete, "/v1/mounts/"+url.PathEscape(id), nil)
		if err != nil || status != http.StatusOK {
			return errMountBrokerUnavailable
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, localfolder.ErrRootInUse) || errors.Is(err, localfolder.ErrRootInvalid) {
			rootError(c, err)
			return
		}
		c.JSON(
			http.StatusConflict,
			gin.H{
				"error": "mount could not be removed; it may be in use or unavailable",
				"code":  "mount_remove_failed",
			},
		)
		return
	}
	h.log(c, tenant, "mount_removed", id)
	c.JSON(http.StatusOK, gin.H{"removed": true, "source_files_preserved": true})
}
