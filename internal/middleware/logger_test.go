package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

func TestLoggerKeepsHTTPBodiesAndOmitsTheirContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	t.Cleanup(logger.ConfigureFromEnv)

	const query = "QA-QUESTION-SENTINEL-8472"
	const quote = "QUOTED-MESSAGE-SENTINEL-8472"
	const attachment = "ATTACHMENT-SENTINEL-8472"
	const answer = "GENERATED-ANSWER-SENTINEL-8472"
	const urlQuery = "URL-QUERY-SENTINEL-8472"
	const requestID = "CLIENT-ID-SENTINEL-8472"
	const credential = "BEARER-TOKEN-SENTINEL-8472"
	body := `{"query":"` + query + `","quote":"` + quote + `","attachment":"` + attachment + `"}`
	var received string
	r := gin.New()
	r.Use(RequestID(), Logger())
	r.POST("/api/v1/agent-chat/:session_id", func(c *gin.Context) {
		payload, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Errorf("handler could not read request: %v", err)
		}
		received = string(payload)
		c.JSON(http.StatusCreated, gin.H{"answer": answer})
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-chat/session-1?query="+urlQuery, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("Authorization", "Bearer "+credential)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if received != body || w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), answer) {
		t.Fatalf("middleware changed request/response behavior: body_ok=%t status=%d answer_ok=%t",
			received == body, w.Code, strings.Contains(w.Body.String(), answer))
	}
	if got := w.Header().Get("X-Request-ID"); got != requestID {
		t.Fatalf("middleware changed X-Request-ID: %q", got)
	}
	logOutput := captured.String()
	for _, secret := range []string{query, quote, attachment, answer, urlQuery, requestID, credential} {
		if strings.Contains(logOutput, secret) {
			t.Fatalf("routine access log leaked request or response content")
		}
	}
	for _, field := range []string{"request_body", "response_body", " method=", " status_code=", " latency=", "request_id_sha256="} {
		if field == "request_body" || field == "response_body" {
			if strings.Contains(logOutput, field) {
				t.Fatalf("access log still records body field %s", field)
			}
		} else if !strings.Contains(logOutput, field) {
			t.Fatalf("access log omitted diagnostic field %s", field)
		}
	}
	if !strings.Contains(logOutput, "/api/v1/agent-chat/:session_id") {
		t.Fatal("access log omitted registered route template")
	}
	if !strings.Contains(logOutput, secutils.HashRequestIDForLog(requestID)) {
		t.Fatal("access log omitted stable request ID digest")
	}
}

func TestLoggerOmitsUnmatchedPathAndErrorText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	t.Cleanup(logger.ConfigureFromEnv)

	const pathSentinel = "UNMATCHED-PATH-SENTINEL-7342"
	const errorSentinel = "PROVIDER-ERROR-SENTINEL-7342"
	r := gin.New()
	r.Use(RequestID(), Logger())
	r.POST("/fail", func(c *gin.Context) {
		_ = c.Error(errors.New(errorSentinel))
		c.Status(http.StatusBadGateway)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/fail", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing/"+pathSentinel, nil))
	logOutput := captured.String()
	if strings.Contains(logOutput, pathSentinel) || strings.Contains(logOutput, errorSentinel) {
		t.Fatal("access log leaked unmatched path or error text")
	}
	if !strings.Contains(logOutput, "[unmatched route]") || !strings.Contains(logOutput, "error_count=") {
		t.Fatal("access log omitted safe diagnostic metadata")
	}
}
