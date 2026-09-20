package octobusiness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func focusedToolByName(t *testing.T, s *Service, name string) types.Tool {
	t.Helper()
	for _, tool := range NewTools(s) {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("missing focused tool %s", name)
	return nil
}
func TestFocusedToolSchemasSeparateIssueAndKnowledgeInputs(t *testing.T) {
	tools := NewTools(nil)
	require.Len(t, tools, 6)
	schemas := map[string]struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Additional bool                       `json:"additionalProperties"`
	}{}
	for _, tool := range tools {
		require.NotEqual(t, "octo_knowledge_operations", tool.Name())
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
			Additional bool                       `json:"additionalProperties"`
		}
		require.NoError(t, json.Unmarshal(tool.Parameters(), &schema))
		require.False(t, schema.Additional)
		require.NotContains(t, schema.Properties, "operation")
		schemas[tool.Name()] = schema
	}
	require.Empty(t, schemas[ToolConfiguration].Properties)
	issue := schemas[ToolIssues]
	require.Equal(t, []string{"action"}, issue.Required)
	require.Contains(t, issue.Properties, "description")
	require.Contains(t, issue.Properties, "input_clarifications_needed")
	require.NotContains(t, issue.Properties, "content")
	require.NotContains(t, issue.Properties, "missing_fields")
	preview := schemas[ToolKnowledgePreview]
	require.Equal(t, []string{"action"}, preview.Required)
	for _, field := range []string{"kind", "title", "description", "issue_id", "input_clarifications_needed", "retrieval_status"} {
		require.NotContains(t, preview.Properties, field)
	}
	require.Contains(t, preview.Properties, "content")
	require.Equal(t, []string{"proposal_id", "decision"}, schemas[ToolKnowledgeConfirm].Required)
	require.Contains(t, schemas[ToolReport].Properties, "format")
	require.NotContains(t, schemas[ToolReport].Properties, "report_format")
}
func TestFocusedToolsRejectUnknownArgumentsBeforeDispatch(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{ToolConfiguration, `{"operation":"configuration"}`},
		{ToolContacts, `{"user_id":"someone"}`},
		{ToolIssues, `{"action":"create","content":"report"}`},
		{ToolIssues, `{"action":"create","missing_fields":["missing price answer"]}`},
		{ToolKnowledgePreview, `{"action":"create_kb","name":"New","kind":"bug"}`},
		{ToolKnowledgeConfirm, `{"proposal_id":"OP-id","decision":"confirm","user_id":"owner"}`},
		{ToolReport, `{"format":"html","channel_id":"another-group"}`},
	} {
		t.Run(tc.name+tc.payload, func(t *testing.T) {
			result, err := focusedToolByName(t, &Service{}, tc.name).Execute(principalContext(testPrincipal()), json.RawMessage(tc.payload))
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Equal(t, "invalid_arguments", result.Data["error_code"])
		})
	}
}
func TestFocusedIssueClarificationsAreInputFactsAndReuseIdempotency(t *testing.T) {
	service := testService(t)
	tool := focusedToolByName(t, service, ToolIssues)
	payload := json.RawMessage(`{"action":"create","kind":"missing","title":"年度采购价","description":"用户清楚地询问年度采购价，已检索但没有对应资料。","input_clarifications_needed":[],"retrieval_status":"no_answer"}`)
	first, err := tool.Execute(principalContext(testPrincipal()), payload)
	require.NoError(t, err)
	require.True(t, first.Success, first.Error)
	second, err := tool.Execute(principalContext(testPrincipal()), payload)
	require.NoError(t, err)
	require.True(t, second.Success, second.Error)
	var a, b Issue
	require.NoError(t, json.Unmarshal([]byte(first.Output), &a))
	require.NoError(t, json.Unmarshal([]byte(second.Output), &b))
	require.Equal(t, a.ID, b.ID)
	require.Equal(t, "kb-a", a.KnowledgeBaseID)
	result, err := tool.Execute(principalContext(testPrincipal()), json.RawMessage(`{"action":"create","kind":"bug","title":"导出失败","description":"用户说导出失败。","expected":"生成文件","steps":"点击导出","input_clarifications_needed":["尚未说明具体哪个产品"]}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, ErrClarify.Error(), result.Error)
	var count int64
	require.NoError(t, service.db.Model(&Issue{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
func TestFocusedPreviewAliasesProposalAndKeepsNativeConfirmation(t *testing.T) {
	service := testService(t)
	calls := []bool{}
	service.Management = func(_ context.Context, _ Principal, in ManagementInput, preview bool) (map[string]any, error) {
		calls = append(calls, preview)
		return map[string]any{"action": in.Action, "name": in.Name}, nil
	}
	principal := testPrincipal()
	preview, err := focusedToolByName(t, service, ToolKnowledgePreview).Execute(principalContext(principal), json.RawMessage(`{"action":"create_kb","name":"New product"}`))
	require.NoError(t, err)
	require.True(t, preview.Success, preview.Error)
	var row map[string]any
	require.NoError(t, json.Unmarshal([]byte(preview.Output), &row))
	require.Equal(t, row["id"], row["proposal_id"])
	id := row["proposal_id"].(string)
	require.True(t, strings.HasPrefix(id, "OP-"))
	require.Equal(t, []bool{true}, calls)
	confirm := focusedToolByName(t, service, ToolKnowledgeConfirm)
	payload, _ := json.Marshal(map[string]string{"proposal_id": id, "decision": "confirm"})
	denied, err := confirm.Execute(principalContext(principal), payload)
	require.NoError(t, err)
	require.False(t, denied.Success)
	require.Equal(t, ErrDenied.Error(), denied.Error)
	principal.MessageID = "confirmation-message"
	principal.MessageText = "确认 " + id
	completed, err := confirm.Execute(principalContext(principal), payload)
	require.NoError(t, err)
	require.True(t, completed.Success, completed.Error)
	var done map[string]any
	require.NoError(t, json.Unmarshal([]byte(completed.Output), &done))
	require.Equal(t, id, done["proposal_id"])
	require.Equal(t, "completed", done["status"])
	require.Equal(t, []bool{true, true, false}, calls)
	again, err := confirm.Execute(principalContext(principal), payload)
	require.NoError(t, err)
	require.True(t, again.Success)
	require.Len(t, calls, 3, "duplicate confirmation must not execute the mutation again")
}
func TestLegacyIssueContentAliasIsNotExposedByFocusedSchema(t *testing.T) {
	service := testService(t)
	result, err := NewTool(service).Execute(principalContext(testPrincipal()), json.RawMessage(`{"operation":"create_issue","kind":"suggestion","title":"改进提示","content":"用户希望按钮文案更清楚。"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var issue Issue
	require.NoError(t, json.Unmarshal([]byte(result.Output), &issue))
	require.Equal(t, "用户希望按钮文案更清楚。", issue.Description)
}

func TestFocusedToolsUsePortableFlatJSONSchemas(t *testing.T) {
	for _, tool := range NewTools(nil) {
		t.Run(tool.Name(), func(t *testing.T) {
			var schema map[string]any
			require.NoError(t, json.Unmarshal(tool.Parameters(), &schema))
			require.Equal(t, "object", schema["type"])
			require.Equal(t, false, schema["additionalProperties"])
			required, ok := schema["required"].([]any)
			require.True(t, ok, "required must be an array, never null")
			properties, ok := schema["properties"].(map[string]any)
			require.True(t, ok)
			for _, field := range required {
				require.Contains(t, properties, field)
			}
			for name, value := range properties {
				property, ok := value.(map[string]any)
				require.True(t, ok, "property %s must be a schema, not null", name)
				require.Contains(t, []any{"string", "integer", "array"}, property["type"])
				if property["type"] == "array" {
					items, ok := property["items"].(map[string]any)
					require.True(t, ok)
					require.Equal(t, "string", items["type"])
				}
				if raw, present := property["enum"]; present {
					values, ok := raw.([]any)
					require.True(t, ok)
					require.NotEmpty(t, values)
				}
			}
			for _, keyword := range []string{"if", "then", "else", "allOf", "oneOf", "anyOf", "$ref"} {
				require.NotContains(t, schema, keyword)
			}
		})
	}
}
