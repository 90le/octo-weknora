package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type channelArtifactMessageStub struct {
	*stubMessageServiceForArtifacts
	message *types.Message
	denied  bool
}

func (s *channelArtifactMessageStub) GetChannelArtifactMessage(_ context.Context, sessionID, messageID string, index int) (*types.Message, error) {
	if s.denied || sessionID != "s" || messageID != "m" || index != 0 {
		return nil, errors.New("denied")
	}
	return s.message, nil
}

func TestChannelArtifactDownloadDoesNotGrantSessionAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	msg := &channelArtifactMessageStub{stubMessageServiceForArtifacts: &stubMessageServiceForArtifacts{}, message: &types.Message{ID: "m", SessionID: "s", Artifacts: types.MessageArtifacts{{URL: "local://42/out/report.txt", FileName: "report.txt", FileSize: 4}}}}
	files := &fakeArtifactFileService{url: "local://42/out/report.txt", data: []byte("test")}
	h := &Handler{messageService: msg, fileService: files}
	r := gin.New()
	r.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(42)))
		c.Next()
	})
	r.GET("/artifacts/channels/:id/messages/:message_id/:index/download", h.DownloadChannelArtifact)
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/artifacts/channels/s/messages/m/0/download", nil))
		return w
	}
	w := request()
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "test", w.Body.String())
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	msg.denied = true
	w = request()
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Equal(t, 1, files.calls)
}
