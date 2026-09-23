package tools

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/sirupsen/logrus"
)

func TestKnowledgeSearchLogsOmitQueriesAndResultBody(t *testing.T) {
	const secret = "PRIVATE_SENTINEL_knowledge_search_query_and_result"
	var output bytes.Buffer
	log := logrus.New()
	log.SetOutput(&output)
	ctx := context.WithValue(context.Background(), types.LoggerContextKey, logrus.NewEntry(log))
	logKnowledgeSearchQueries(ctx, []string{secret})
	logKnowledgeSearchOutput(ctx, &types.ToolResult{Success: true, Output: "source body: " + secret})
	line := output.String()
	if strings.Contains(line, secret) || strings.Contains(line, "source body") {
		t.Fatalf("private query or retrieved content appeared in KnowledgeSearch log: %s", line)
	}
	for _, want := range []string{"Queries: count=1 bytes=", "Output: present=true success=true bytes="} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing diagnostic %q in %s", want, line)
		}
	}
}
