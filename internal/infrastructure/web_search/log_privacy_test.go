package web_search

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
)

func TestProviderErrorLogsOmitEchoedQuery(t *testing.T) {
	const query = "SEARCH-QUERY-CANARY-1734"
	const echoedResponse = "UPSTREAM-ECHO-CANARY-1734"
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	t.Cleanup(logger.ConfigureFromEnv)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), query) {
			t.Errorf("provider request lost original search query: err=%v", err)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, echoedResponse+":"+query)
	}))
	defer srv.Close()

	p := &KeenableProvider{client: srv.Client(), baseURL: srv.URL}
	_, err := p.Search(context.Background(), query, 3, false)
	if err == nil || !strings.Contains(err.Error(), echoedResponse) {
		t.Fatal("provider business error unexpectedly changed")
	}
	logOutput := captured.String()
	if strings.Contains(logOutput, query) || strings.Contains(logOutput, echoedResponse) {
		t.Fatal("routine provider logs leaked the query or echoed error body")
	}
	if !strings.Contains(logOutput, "response_bytes=") || !strings.Contains(logOutput, "status 502") {
		t.Fatal("provider log omitted status and response size")
	}
}
