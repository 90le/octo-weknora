package common

import (
	"encoding/json"
	"strings"
	"testing"

	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func TestPipelineLogRedactsContentAndKeepsDiagnostics(t *testing.T) {
	const secret = "PRIVATE_SENTINEL_user_question_quote_attachment_tool_output"
	const token = "SECRETABC123"
	line := PipelineLog("AgentTool", "execute_done", map[string]interface{}{
		"request_id":   "01234567-89ab-cdef-0123-456789abcdef",
		"session_id":   "abcdef01-2345-6789-abcd-ef0123456789",
		"tool":         token,
		"reason":       token,
		"status":       "failed",
		"error_code":   "timeout",
		"duration_ms":  125,
		"result_count": 3,
		"success":      false,
		"query":        secret,
		"error":        "upstream echoed " + secret,
		"args":         json.RawMessage(`{"nested":{"query":"` + secret + `"}}`),
		"new_string":   token,
		"nested": map[string]any{
			"quoted_context": secret,
			"attachments":    []string{secret},
		},
		"results": []any{map[string]any{"content": secret}},
	})
	if strings.Contains(line, secret) || strings.Contains(line, token) || strings.Contains(line, "upstream echoed") {
		t.Fatalf("private content appeared in pipeline log: %s", line)
	}
	for _, want := range []string{
		"stage=AgentTool action=execute_done", "request_id=\"" + secutils.HashRequestIDForLog("01234567-89ab-cdef-0123-456789abcdef") + "\"",
		"tool=\"[redacted len=12]\"", "reason=\"[redacted len=12]\"",
		"status=\"failed\"", "error_code=\"timeout\"",
		"duration_ms=125", "result_count=3", "success=false", "query=\"[redacted len=59]\"",
		"nested=\"[redacted count=2]\"", "results=\"[redacted count=1]\"",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing diagnostic %q in %s", want, line)
		}
	}
	if strings.Contains(line, "01234567-89ab-cdef-0123-456789abcdef") {
		t.Fatal("pipeline log exposed client-controlled request ID")
	}
}
