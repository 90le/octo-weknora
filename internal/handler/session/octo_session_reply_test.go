package session

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeOctoBridge struct {
	sentCount int
	lastBody  string
	lastQuote bool
}

func (f *fakeOctoBridge) ResolveSessionOrigins(_ context.Context, _ uint64, ids []string) map[string]*types.SessionIMOrigin {
	out := map[string]*types.SessionIMOrigin{}
	for _, id := range ids {
		out[id] = &types.SessionIMOrigin{
			Platform:    "octo",
			ChannelID:   "ch-1",
			ChannelName: "秒针业问",
			ChatType:    "subarea",
			SourceName:  "bot对话测试专区",
			UserUID:     "u-1",
			UserName:    "丘彬彬",
		}
	}
	return out
}

func (f *fakeOctoBridge) SendToSessionOrigin(_ context.Context, _ uint64, _ string, content string, quoteLatest bool) (string, *types.SessionIMOrigin, error) {
	f.sentCount++
	f.lastBody = content
	f.lastQuote = quoteLatest
	return "rcpt-999", &types.SessionIMOrigin{Platform: "octo", ChatType: "subarea", SourceName: "bot对话测试专区"}, nil
}

func (f *fakeOctoBridge) SendFileToSessionOrigin(_ context.Context, _ uint64, _, fileName string, _ []byte, _ string, _ bool) (string, string, *types.SessionIMOrigin, error) {
	f.sentCount++
	return "rcpt-file-1", fileName, &types.SessionIMOrigin{Platform: "octo", ChatType: "subarea", SourceName: "bot对话测试专区"}, nil
}

type bridgeSessionStub struct {
	interfaces.SessionService
	sess *types.Session
}

func (s bridgeSessionStub) GetSession(_ context.Context, _ string) (*types.Session, error) {
	return s.sess, nil
}
func (s bridgeSessionStub) GetOwnedSession(_ context.Context, _ string) (*types.Session, error) {
	return nil, http.ErrNoLocation
}

type bridgeMessageStub struct {
	interfaces.MessageService
	created *types.Message
	msg     *types.Message
	updated *types.Message
}

func (m *bridgeMessageStub) CreateMessage(_ context.Context, msg *types.Message) (*types.Message, error) {
	m.created = msg
	return msg, nil
}
func (m *bridgeMessageStub) GetMessage(_ context.Context, _, _ string) (*types.Message, error) {
	return m.msg, nil
}
func (m *bridgeMessageStub) UpdateMessage(_ context.Context, msg *types.Message) error {
	m.updated = msg
	return nil
}

func TestOctoManualReplyAndRelayPermissionsAndIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bridge := &fakeOctoBridge{}
	msgStub := &bridgeMessageStub{
		msg: &types.Message{ID: "m-1", SessionID: "s-1", Role: "assistant", Content: "AI 回答内容", IsCompleted: true, Channel: "web"},
	}
	h := &Handler{
		sessionService: bridgeSessionStub{sess: &types.Session{ID: "s-1", TenantID: 1, IMPlatform: "octo"}},
		messageService: msgStub,
		octoBridge:     bridge,
	}

	// Non-admin viewer is rejected with 403
	rViewer := gin.New()
	rViewer.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(1))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "viewer-1")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	rViewer.POST("/sessions/:session_id/octo-reply", h.SendOctoManualReply)
	w := httptest.NewRecorder()
	rViewer.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sessions/s-1/octo-reply", bytes.NewBufferString(`{"content":"hello"}`)))
	require.Equal(t, http.StatusForbidden, w.Code)

	// Admin manual reply succeeds & creates octo_manual message
	rAdmin := gin.New()
	rAdmin.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(1))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "admin-1")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	rAdmin.POST("/sessions/:session_id/octo-reply", h.SendOctoManualReply)
	rAdmin.POST("/sessions/:session_id/messages/:message_id/relay-octo", h.RelayMessageToOcto)

	w2 := httptest.NewRecorder()
	rAdmin.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/sessions/s-1/octo-reply", bytes.NewBufferString(`{"content":"人工回复正文"}`)))
	require.Equal(t, http.StatusOK, w2.Code)
	require.Equal(t, 1, bridge.sentCount)
	require.Equal(t, "人工回复正文", bridge.lastBody)
	require.NotNil(t, msgStub.created)
	require.Equal(t, "octo_manual", msgStub.created.Channel)

	// Admin relay succeeds once, second relay returns 409 Conflict
	w3 := httptest.NewRecorder()
	rAdmin.ServeHTTP(w3, httptest.NewRequest(http.MethodPost, "/sessions/s-1/messages/m-1/relay-octo", nil))
	require.Equal(t, http.StatusOK, w3.Code)
	require.Equal(t, 2, bridge.sentCount)
	require.Equal(t, "octo_relayed", msgStub.msg.Channel)

	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Receipt string `json:"receipt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &body))
	require.True(t, body.Success)
	require.Equal(t, "rcpt-999", body.Data.Receipt)

	w4 := httptest.NewRecorder()
	rAdmin.ServeHTTP(w4, httptest.NewRequest(http.MethodPost, "/sessions/s-1/messages/m-1/relay-octo", nil))
	require.Equal(t, http.StatusConflict, w4.Code)
}
