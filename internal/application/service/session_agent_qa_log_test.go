package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/sirupsen/logrus"
)

func TestAgentQAStartLogOmitsUserContent(t *testing.T) {
	const secret = "PRIVATE_SENTINEL_agent_qa_question_and_quote"
	var output bytes.Buffer
	log := logrus.New()
	log.SetOutput(&output)
	ctx := context.WithValue(context.Background(), types.LoggerContextKey, logrus.NewEntry(log))
	ctx = context.WithValue(ctx, types.RequestIDContextKey, "01234567-89ab-cdef-0123-456789abcdef")
	req := &types.QARequest{
		Session:       &types.Session{ID: "01234567-89ab-cdef-0123-456789abcdef"},
		Query:         secret,
		QuotedContext: secret,
	}
	logAgentQAStart(ctx, req, 42)
	line := output.String()
	if strings.Contains(line, secret) {
		t.Fatalf("private question or quote appeared in AgentQA log: %s", line)
	}
	for _, want := range []string{"stage=AgentQA action=start", "request_id=", "query_len=", "quoted_context_len=", "tenant_id=42"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing diagnostic %q in %s", want, line)
		}
	}
}
