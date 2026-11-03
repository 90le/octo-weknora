package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExternalFilePathsRejectMalformedLocalForms(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// The legacy tenant parser can take a later embedded scheme as its
	// starting point; this is not evidence of a valid provider locator.
	require.Equal(t, uint64(7), secutils.ParseTenantIDFromStoragePath("local:/7/exports/x://7/file.pdf"))
	engine := gin.New()
	opens := 0
	global := &stubFileService{getFile: func(context.Context, string) (io.ReadCloser, error) {
		opens++
		return io.NopCloser(strings.NewReader("bytes")), nil
	}}
	engine.GET("/files", newFileServeHandler(global, nil))
	for _, ref := range []string{
		"local:/7/exports/file.pdf", "local:/7/exports/x://7/file.pdf",
		"storage:/backend/local://7/exports/file.pdf", "storage://backend/local:/7/exports/x://7/file.pdf",
		"/data/files/7/exports/file.pdf", "/data/files/7/exports/x://7/file.pdf",
		"7/exports/file.pdf", "7/exports/x://7/file.pdf", `C:\files\7\x://7/file.pdf`,
	} {
		t.Run(ref, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/files?file_path="+url.QueryEscape(ref), nil)
			ctx := context.WithValue(req.Context(), types.TenantInfoContextKey, &types.Tenant{ID: 7})
			out := httptest.NewRecorder()
			engine.ServeHTTP(out, req.WithContext(ctx))
			require.Equal(t, http.StatusBadRequest, out.Code)
		})
	}
	require.Zero(t, opens)
}

func TestExternalFilePathFormsPreserveRecognizedLocators(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/check", func(c *gin.Context) {
		if _, ok := requireFilePathQuery(c); ok {
			c.Status(http.StatusNoContent)
		}
	})
	for _, ref := range []string{
		"local://7/exports/file.pdf", "storage://backend/local://7/exports/file.pdf",
		"resource://abcdefghijklmnopqrstuv", "s3://bucket/7/exports/file.pdf",
		"storage://backend/s3://bucket/7/exports/file.pdf", "https://example.com/file.pdf",
	} {
		t.Run(ref, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/check?file_path="+url.QueryEscape(ref), nil)
			out := httptest.NewRecorder()
			engine.ServeHTTP(out, req)
			require.Equal(t, http.StatusNoContent, out.Code)
		})
	}
}
