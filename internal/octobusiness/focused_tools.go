package octobusiness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	ToolConfiguration    = "octo_configuration"
	ToolContacts         = "octo_contacts"
	ToolIssues           = "octo_issues"
	ToolKnowledgePreview = "octo_knowledge_preview"
	ToolKnowledgeConfirm = "octo_knowledge_confirm"
	ToolReport           = "octo_report"
)

// NewTools exposes focused model-facing signatures while reusing the same
// principal validation, authorization, idempotency and confirmation executor.
// octo_knowledge_operations remains the settings group and legacy entry point.
func NewTools(service *Service) []types.Tool {
	legacy := NewTool(service)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	// The legacy schema is a compile-time constant and is also covered by tests.
	if err := json.Unmarshal(legacy.Parameters(), &schema); err != nil {
		panic(err)
	}
	makeTool := func(name, description string, fields, required []string, overrides map[string]any) types.Tool {
		// JSON Schema requires an array (or an omitted keyword), never null.
		if required == nil {
			required = []string{}
		}
		props := map[string]json.RawMessage{}
		for _, field := range fields {
			definition, ok := schema.Properties[field]
			if !ok || len(definition) == 0 {
				panic("unknown focused tool parameter: " + field)
			}
			props[field] = definition
		}
		for field, definition := range overrides {
			props[field], _ = json.Marshal(definition)
		}
		params, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required})
		return &focusedTool{name: name, description: description, parameters: params, fields: props, required: required, legacy: legacy}
	}
	return []types.Tool{
		makeTool(ToolConfiguration, "Read the current verified Octo scope, readable/manageable knowledge bases and creation capability. Takes no arguments. Use the returned IDs to resolve the user's target; never ask users for backend IDs. An empty manageable list alone does not decide creation permission.", nil, []string{}, nil),
		makeTool(ToolContacts, "Find responsible contacts for the currently authorized knowledge bases. Optional knowledge_base_id narrows the result. Display returned native contacts; do not notify or message them.", []string{"knowledge_base_id"}, []string{}, nil),
		makeTool(ToolIssues, "Collect and manage registered knowledge gaps, bugs and suggestions in this conversation. Select action=create/list/get/update. For create, describe verified user facts in description (not content). input_clarifications_needed lists only ambiguities in the user's request/report, never the answer missing from the knowledge base. Clarify those ambiguities first. A clear unanswered question needs an empty list; record a knowledge gap only after successful retrieval returned no answer. Lookup issue IDs with list, do not ask users for internal IDs. Creating a record does not update or publish knowledge.", []string{"knowledge_base_id", "issue_id", "kind", "title", "description", "expected", "steps", "retrieval_status", "status", "note", "keyword", "from", "to", "page", "page_size"}, []string{"action"}, map[string]any{
			"action":                      map[string]any{"type": "string", "enum": []string{"create", "list", "get", "update"}, "description": "Required. create: kind+title+description and an authorized knowledge_base_id (defaults only with one readable KB); bug also needs steps+expected. get/update: issue_id; update also needs status. list supports optional filters."},
			"input_clarifications_needed": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only facts unclear in the user's own input, e.g. which product or reproduction steps. NOT the unknown answer. A clear question such as asking the price has no input ambiguity even when no price exists in the knowledge base; use [] and record the missing answer as kind=missing after verified retrieval."},
		}),
		makeTool(ToolKnowledgePreview, "Preview a knowledge change; action is required. To create a library use action=create_kb and name. An optional creation template needs read permission, not management permission; the server's preview decides current creation authorization. Do not infer denial from an empty manageable list. rename/save_draft may default the KB only when exactly one is manageable. Resolve multiple targets with octo_configuration. Return the exact preview and proposal_id; no change is executed until the same user explicitly confirms. Never publish automatically.", []string{"action", "knowledge_base_id", "knowledge_id", "name", "content", "scope_id"}, []string{"action"}, map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"create_kb", "rename", "save_draft", "publish", "delete_document", "bind", "unbind", "delete_kb"}, "description": "Required. create_kb: name (template KB optional/readable); rename: name; save_draft: name+content; publish: knowledge_id+name+content; delete_document: knowledge_id. All except create_kb need an authorized target KB; rename/save_draft can default only the sole manageable KB. bind/unbind/delete_kb need an explicit resolved knowledge_base_id."},
		}),
		makeTool(ToolKnowledgeConfirm, "Confirm or cancel a previously returned knowledge preview. Use its proposal_id and decision=confirm/cancel only after the same user's actual confirmation/cancellation in this conversation. Never confirm on behalf of the user; the existing server authorization and replay guards remain authoritative.", []string{"proposal_id"}, []string{"proposal_id", "decision"}, map[string]any{
			"decision": map[string]any{"type": "string", "enum": []string{"confirm", "cancel"}, "description": "The same user's explicit decision for this proposal."},
		}),
		makeTool(ToolReport, "Summarize registered issues in the currently authorized scope and requested period. Optional format is text, markdown, html or csv; choose a file format only when requested. Preserve scope, period, truncation and counting limitations. A report counts recorded issues, not every question or overall answer rate. Only claim file delivery when the tool confirms it.", []string{"knowledge_base_id", "kind", "status", "keyword", "from", "to", "page", "page_size"}, []string{}, map[string]any{
			"format": map[string]any{"type": "string", "enum": []string{"text", "markdown", "html", "csv"}, "description": "Omit for text. Use html, csv or markdown for an explicitly requested report file."},
		}),
	}
}

type focusedTool struct {
	name, description string
	parameters        json.RawMessage
	fields            map[string]json.RawMessage
	required          []string
	legacy            *Tool
}

func (t *focusedTool) Name() string        { return t.name }
func (t *focusedTool) Description() string { return t.description }
func (t *focusedTool) Parameters() json.RawMessage {
	return append(json.RawMessage(nil), t.parameters...)
}
func (t *focusedTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	if _, err := currentPrincipal(ctx); err != nil {
		return toolResult(nil, err)
	}
	var input map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(args))
	if decoder.Decode(&input) != nil || input == nil {
		return toolResult(nil, argumentError("Supply one JSON object matching this focused tool's parameters."))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return toolResult(nil, argumentError("Supply exactly one JSON object."))
	}
	for field := range input {
		if _, ok := t.fields[field]; !ok {
			detail := "Use only this focused tool's declared fields; do not supply operation, identity or fields belonging to another tool."
			if t.name == ToolIssues && field == "content" {
				detail = "octo_issues uses description for verified issue facts, not content. Use octo_knowledge_preview for document content."
			}
			if t.name == ToolIssues && field == "missing_fields" {
				detail = "Use input_clarifications_needed only for ambiguity in the user's input. A missing knowledge answer is not an input clarification."
			}
			return toolResult(nil, argumentError(detail))
		}
	}
	for _, field := range t.required {
		var value string
		if json.Unmarshal(input[field], &value) != nil || strings.TrimSpace(value) == "" {
			return toolResult(nil, argumentError(t.name+" requires "+field+". Use the values described in this tool's parameters; do not report this as a permission denial."))
		}
	}
	operation := ""
	switch t.name {
	case ToolConfiguration:
		operation = "configuration"
	case ToolContacts:
		operation = "contacts"
	case ToolIssues:
		var action string
		_ = json.Unmarshal(input["action"], &action)
		operation = map[string]string{"create": "create_issue", "list": "list_issues", "get": "get_issue", "update": "update_issue"}[action]
		if operation == "" {
			return toolResult(nil, argumentError("octo_issues action must be create, list, get or update."))
		}
		delete(input, "action")
		if value, ok := input["input_clarifications_needed"]; ok {
			input["missing_fields"] = value
			delete(input, "input_clarifications_needed")
		}
	case ToolKnowledgePreview:
		operation = "propose"
	case ToolKnowledgeConfirm:
		var decision string
		_ = json.Unmarshal(input["decision"], &decision)
		if decision != "confirm" && decision != "cancel" {
			return toolResult(nil, argumentError("decision must be confirm or cancel, matching the same user's actual decision."))
		}
		operation = decision
		delete(input, "decision")
	case ToolReport:
		operation = "report"
		if value, ok := input["format"]; ok {
			input["report_format"] = value
			delete(input, "format")
		}
	}
	input["operation"], _ = json.Marshal(operation)
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	result, err := t.legacy.Execute(ctx, payload)
	if err == nil && result != nil && result.Success && (t.name == ToolKnowledgePreview || t.name == ToolKnowledgeConfirm) {
		var row map[string]any
		if json.Unmarshal([]byte(result.Output), &row) == nil {
			if id, ok := row["id"].(string); ok && strings.HasPrefix(id, "OP-") {
				row["proposal_id"] = id
				encoded, e := json.Marshal(row)
				if e != nil {
					return nil, e
				}
				result.Output = string(encoded)
				if result.Data == nil {
					result.Data = map[string]any{}
				}
				result.Data["result"] = row
			}
		}
	}
	return result, err
}
