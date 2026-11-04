package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type continueSessionStub struct {
	interfaces.SessionService
	owned bool
}

type continueMessageStub struct {
	interfaces.MessageService
	bound bool
}

func (s continueMessageStub) ReadChannelChat(
	_ context.Context, id string, _ int, _ *time.Time, _ string,
) (*types.Session, []*types.Message, error) {
	if !s.bound {
		return nil, nil, errors.New("not bound")
	}
	return &types.Session{ID: id}, nil, nil
}

func (s continueSessionStub) GetSession(_ context.Context, id string) (*types.Session, error) {
	return &types.Session{ID: id, IMPlatform: "octo"}, nil
}

func (s continueSessionStub) GetOwnedSession(_ context.Context, id string) (*types.Session, error) {
	if !s.owned {
		return nil, errors.New("not owner")
	}
	return &types.Session{ID: id}, nil
}

func TestChannelContinueRequiresOwnedSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ owned, bound bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
		h := &Handler{
			sessionService: continueSessionStub{owned: tc.owned},
			messageService: continueMessageStub{bound: tc.bound},
		}
		r := gin.New()
		r.GET("/sessions/:id", h.GetSession)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions/test-session", nil))
		require.Equal(t, 200, w.Code)
		var body struct {
			Data struct {
				CanContinue bool `json:"can_continue"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, tc.owned && tc.bound, body.Data.CanContinue)
	}
}
