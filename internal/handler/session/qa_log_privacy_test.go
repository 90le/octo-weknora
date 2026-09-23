package session

import (
	"bytes"
	"context"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type privacySessionServiceStub struct {
	interfaces.SessionService
	seenSessionID string
	seenQuery     string
}

func (s *privacySessionServiceStub) GetOwnedSession(_ context.Context, id string) (*types.Session, error) {
	s.seenSessionID = id
	return nil, stderrors.New("missing test session")
}

func (s *privacySessionServiceStub) SearchKnowledge(
	_ context.Context, _ []string, _ []string, _ []types.TagScope, query string,
) ([]*types.SearchResult, error) {
	s.seenQuery = query
	return nil, nil
}

func TestQAAndKnowledgeSearchDoNotLogUserQuestion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	t.Cleanup(logger.ConfigureFromEnv)
	service := &privacySessionServiceStub{}
	h := &Handler{sessionService: service}
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.Logger())
	r.POST("/api/v1/agent-chat/:session_id", func(c *gin.Context) {
		_, _, err := h.parseQARequest(c, "AgentQA")
		if err == nil {
			t.Error("expected synthetic session lookup failure")
		}
		c.Status(http.StatusNotFound)
	})
	r.POST("/api/v1/knowledge-search", h.SearchKnowledge)

	const question = "QA-QUESTION-CANARY-6291"
	const quote = "QUOTED-MESSAGE-CANARY-6291"
	const attachment = "ATTACHMENT-CANARY-6291"
	const requestID = "CLIENT-REQUEST-ID-CANARY-6291"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-chat/session-test",
		strings.NewReader(`{"query":"`+question+`","quote":"`+quote+`","attachment":"`+attachment+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || service.seenSessionID != "session-test" {
		t.Fatalf("QA request was not parsed/routed: status=%d session=%q", w.Code, service.seenSessionID)
	}

	search := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-search",
		strings.NewReader(`{"query":"`+question+`","knowledge_base_ids":["kb-test"]}`))
	search.Header.Set("Content-Type", "application/json")
	searchResult := httptest.NewRecorder()
	r.ServeHTTP(searchResult, search)
	if searchResult.Code != http.StatusOK || service.seenQuery != question {
		t.Fatalf("knowledge search lost query: status=%d query_ok=%t", searchResult.Code, service.seenQuery == question)
	}

	logOutput := captured.String()
	for _, canary := range []string{question, quote, attachment, requestID} {
		if strings.Contains(logOutput, canary) {
			t.Fatal("QA/search routine logs leaked user content")
		}
	}
	if !strings.Contains(logOutput, "query_bytes=") {
		t.Fatal("QA/search log omitted safe query size")
	}
}
