package octobusiness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolMissingOrInvalidActionIsRecoverableWithoutManagement(t *testing.T) {
	called := false
	s := &Service{Management: func(context.Context, Principal, ManagementInput, bool) (map[string]any, error) {
		called = true
		return nil, ErrDenied
	}}
	tool := NewTool(s)
	for _, payload := range []string{
		`{"operation":"propose","name":"New product","description":"Product knowledge","note":"Please create"}`,
		`{"operation":"propose","action":"create","name":"New product"}`,
	} {
		result, err := tool.Execute(principalContext(testPrincipal()), json.RawMessage(payload))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Equal(t, "invalid_arguments", result.Data["error_code"])
		require.Equal(t, true, result.Data["retryable"])
		require.Contains(t, result.Error, "requires action")
		require.Contains(t, result.Error, "action=create_kb")
		require.NotContains(t, result.Error, ErrDenied.Error())
		require.False(t, called, "missing action must not be delegated as an authorization request")
	}
}
func TestToolRequiredArgumentsFailBeforeServiceDispatch(t *testing.T) {
	tool := NewTool(&Service{}) // Any accidental service dispatch would hit missing dependencies.
	principal := testPrincipal()
	principal.KnowledgeBaseIDs = []string{"kb-a", "kb-b"}
	principal.ManageKnowledgeBaseIDs = []string{"kb-a", "kb-b"}
	for _, tc := range []struct{ payload, field string }{
		{`{}`, "operation"},
		{`{"operation":"create_issue","kind":"suggestion","title":"A","description":"B"}`, "knowledge_base_id"},
		{`{"operation":"create_issue","knowledge_base_id":"kb-a","title":"A","description":"B"}`, "kind"},
		{`{"operation":"create_issue","knowledge_base_id":"kb-a","kind":"suggestion","description":"B"}`, "title"},
		{`{"operation":"create_issue","knowledge_base_id":"kb-a","kind":"bug","title":"A","description":"B","expected":"Works"}`, "steps"},
		{`{"operation":"get_issue"}`, "issue_id"},
		{`{"operation":"update_issue","issue_id":"known-issue"}`, "status"},
		{`{"operation":"update_issue","issue_id":"known-issue","status":"done"}`, "status"},
		{`{"operation":"propose","action":"create_kb"}`, "name"},
		{`{"operation":"propose","action":"rename","name":"New name"}`, "knowledge_base_id"},
		{`{"operation":"propose","action":"save_draft","knowledge_base_id":"kb-a","name":"Draft"}`, "content"},
		{`{"operation":"propose","action":"publish","knowledge_base_id":"kb-a","name":"Doc","content":"Exact document"}`, "knowledge_id"},
		{`{"operation":"confirm"}`, "proposal_id"},
		{`{"operation":"cancel"}`, "proposal_id"},
		{`{"operation":"report","report_format":"pdf"}`, "report_format"},
		{`{"operation":"report","from":"2026-09-20T10:00:00Z","to":"2026-09-19T10:00:00Z"}`, "to must be later"},
		{`{"operation":"report","from":"yesterday"}`, "from must be"},
	} {
		t.Run(tc.field+tc.payload, func(t *testing.T) {
			result, err := tool.Execute(principalContext(principal), json.RawMessage(tc.payload))
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Equal(t, "invalid_arguments", result.Data["error_code"])
			require.Contains(t, result.Error, tc.field)
			require.NotContains(t, result.Error, ErrDenied.Error())
		})
	}
}
func TestToolRejectsExtraJSONAndKeepsTrustedAdmissionFirst(t *testing.T) {
	tool := NewTool(&Service{})
	result, err := tool.Execute(principalContext(testPrincipal()), json.RawMessage(`{"operation":"configuration"} {"operation":"configuration"}`))
	require.NoError(t, err)
	require.Contains(t, result.Error, "exactly one JSON object")
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"operation":"propose","name":"New"}`))
	require.NoError(t, err)
	require.Equal(t, ErrDenied.Error(), result.Error)
	require.Nil(t, result.Data, "unauthorized callers do not receive recoverable-argument treatment")
}
func TestToolCorrectCreationStillDelegatesAuthorization(t *testing.T) {
	s := testService(t)
	p := testPrincipal()
	p.ManageKnowledgeBaseIDs = nil
	p.CanManageScope = true
	called := 0
	allowCreation := true
	s.Management = func(_ context.Context, actor Principal, in ManagementInput, preview bool) (map[string]any, error) {
		called++
		require.Empty(t, actor.ManageKnowledgeBaseIDs)
		require.Equal(t, "create_kb", in.Action)
		require.Empty(t, in.KnowledgeBaseID, "creation may select the current authorized template")
		require.True(t, preview, "propose must never execute a write callback")
		if !actor.CanManageScope || !allowCreation {
			return nil, ErrDenied
		}
		return map[string]any{"action": in.Action, "name": in.Name, "notice": "preview only"}, nil
	}
	payload := json.RawMessage(`{"operation":"propose","action":"create_kb","name":"New product"}`)
	result, err := NewTool(s).Execute(principalContext(p), payload)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, 1, called)
	var proposal Proposal
	require.NoError(t, json.Unmarshal([]byte(result.Output), &proposal))
	require.Equal(t, "pending", proposal.Status)
	require.True(t, strings.HasPrefix(proposal.ID, "OP-"))
	allowCreation = false
	result, err = NewTool(s).Execute(principalContext(p), payload)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, ErrDenied.Error(), result.Error)
	require.Nil(t, result.Data, "real authorization failures must remain real failures")
}
func TestToolSchemaDescribesFlatCreationRecipe(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		}
		Required []string
	}
	tool := NewTool(nil)
	require.NoError(t, json.Unmarshal(tool.Parameters(), &schema))
	require.Equal(t, []string{"operation"}, schema.Required, "do not make action mandatory for read operations")
	require.Contains(t, schema.Properties["action"].Description, "Required whenever operation=propose")
	require.Contains(t, schema.Properties["action"].Description, "create_kb needs name")
	require.Contains(t, tool.Description(), `"operation":"propose","action":"create_kb"`)
}

func TestToolSingleReadableKnowledgeBaseDefaultsForIssue(t *testing.T) {
	s := testService(t)
	principal := testPrincipal()
	principal.ManageKnowledgeBaseIDs = nil
	result, err := NewTool(s).Execute(principalContext(principal), json.RawMessage(`{"operation":"create_issue","kind":"missing","title":"采购付款周期","description":"已成功检索但资料未说明实际付款周期。","retrieval_status":"no_answer"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var issue Issue
	require.NoError(t, json.Unmarshal([]byte(result.Output), &issue))
	require.Equal(t, "kb-a", issue.KnowledgeBaseID)
	require.Equal(t, principal.UserID, issue.ReporterUID)
	require.Empty(t, principal.ManageKnowledgeBaseIDs)
}
func TestToolDefaultsSelectOnlyTheExactEligibleSet(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation string
		action    string
		read      []string
		manage    []string
		explicit  string
		expected  string
	}{
		{name: "issue uses one readable", operation: "create_issue", read: []string{"read"}, manage: []string{"managed"}, expected: "read"},
		{name: "rename uses one manageable", operation: "propose", action: "rename", read: []string{"read"}, manage: []string{"managed"}, expected: "managed"},
		{name: "draft uses one manageable", operation: "propose", action: "save_draft", manage: []string{"managed"}, expected: "managed"},
		{name: "multiple readable stays unresolved", operation: "create_issue", read: []string{"one", "two"}},
		{name: "multiple manageable stays unresolved", operation: "propose", action: "rename", manage: []string{"one", "two"}},
		{name: "read does not grant rename", operation: "propose", action: "rename", read: []string{"read"}},
		{name: "management does not grant issue read", operation: "create_issue", manage: []string{"managed"}},
		{name: "explicit foreign target is not rewritten", operation: "create_issue", read: []string{"read"}, explicit: "foreign", expected: "foreign"},
		{name: "new KB template remains optional", operation: "propose", action: "create_kb", read: []string{"one", "two"}},
		{name: "destructive target never inferred", operation: "propose", action: "delete_kb", manage: []string{"managed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := testPrincipal()
			principal.KnowledgeBaseIDs = tc.read
			principal.ManageKnowledgeBaseIDs = tc.manage
			in := toolInput{Operation: tc.operation, Action: tc.action, IssueInput: IssueInput{KnowledgeBaseID: tc.explicit}}
			defaultKnowledgeTarget(&in, principal)
			require.Equal(t, tc.expected, in.KnowledgeBaseID)
			require.Equal(t, tc.read, principal.KnowledgeBaseIDs)
			require.Equal(t, tc.manage, principal.ManageKnowledgeBaseIDs)
		})
	}
}
