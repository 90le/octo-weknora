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
	session       *types.Session
}

func (s *privacySessionServiceStub) GetOwnedSession(_ context.Context, id string) (*types.Session, error) {
	s.seenSessionID = id
	if s.session != nil {
		return s.session, nil
	}
	return nil, stderrors.New("missing test session")
}

type privacyAgentServiceStub struct {
	interfaces.CustomAgentService
	seenAgentID string
}

func (s *privacyAgentServiceStub) GetAgentByID(_ context.Context, id string) (*types.CustomAgent, error) {
	s.seenAgentID = id
	return nil, stderrors.New("lookup failed for " + id)
}

func TestAgentQAInvalidAgentIDDoesNotLeakRequestValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	t.Cleanup(logger.ConfigureFromEnv)

	const agentID = "B074-PRIVACY-BADAGENT-0717"
	const sessionID = "B074-PRIVACY-SESSION-0717"
	const question = "B074-PRIVACY-QUESTION-0717"
	service := &privacySessionServiceStub{session: &types.Session{ID: sessionID, TenantID: 1}}
	agents := &privacyAgentServiceStub{}
	h := &Handler{sessionService: service, customAgentService: agents}
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.Logger())
	r.POST("/api/v1/agent-chat/:session_id", func(c *gin.Context) {
		h.AgentQA(c)
		if len(c.Errors) != 0 {
			c.Status(http.StatusBadRequest)
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-chat/"+sessionID,
		strings.NewReader(`{"query":"`+question+`","agent_enabled":true,"agent_id":"`+agentID+`","disable_title":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || service.seenSessionID != sessionID || agents.seenAgentID != agentID {
		t.Fatalf("invalid agent request changed: status=%d session_ok=%t agent_ok=%t",
			w.Code, service.seenSessionID == sessionID, agents.seenAgentID == agentID)
	}
	logs := captured.String()
	for _, marker := range []string{agentID, sessionID, question} {
		if strings.Contains(logs, marker) {
			t.Fatal("invalid agent request leaked user-controlled value to routine logs")
		}
	}
	if !strings.Contains(logs, "resolvable agent_id") || !strings.Contains(logs, "error_type=") {
		t.Fatal("invalid agent request lost safe error diagnostics")
	}
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
