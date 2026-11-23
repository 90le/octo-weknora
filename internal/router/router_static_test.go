package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFrontendStaticDoesNotInterceptResourceGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	webDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(webDir, "index.html"), []byte("spa"), 0o600))
	t.Setenv("WEKNORA_WEB_DIR", webDir)

	r := gin.New()
	serveFrontendStatic(r)
	r.GET("/r/:token", func(c *gin.Context) { c.String(http.StatusOK, "resource") })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/r/token", nil)
	r.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "resource", recorder.Body.String())
}

func TestFrontendStaticLicenseNotices(t *testing.T) {
	gin.SetMode(gin.TestMode)
	webDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(webDir, "index.html"), []byte("spa"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(webDir, "assets"), 0o700))
	t.Setenv("WEKNORA_WEB_DIR", webDir)

	r := gin.New()
	serveFrontendStatic(r)
	for _, filename := range []string{"weknora-licenses.txt", "npm-licenses.md"} {
		request := httptest.NewRequest(http.MethodGet, "/assets/"+filename, nil)
		missing := httptest.NewRecorder()
		r.ServeHTTP(missing, request)
		require.Equal(t, http.StatusNotFound, missing.Code)
		require.NotContains(t, missing.Body.String(), "spa")

		require.NoError(t, os.WriteFile(filepath.Join(webDir, "assets", filename), []byte("license text"), 0o600))
		present := httptest.NewRecorder()
		r.ServeHTTP(present, request)
		require.Equal(t, http.StatusOK, present.Code)
		require.Equal(t, "license text", present.Body.String())
		require.Equal(t, "no-cache, must-revalidate", present.Header().Get("Cache-Control"))
	}
}
