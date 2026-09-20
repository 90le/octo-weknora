package octobusiness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
)

// This is an administrator catalog, not a retrieval tool. Draft bodies must
// never be made discoverable by loosening public knowledge_search visibility.
const managedDocumentContentLimit = 200000

type ManagedDocumentQuery struct {
	Action          string `json:"action"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID     string `json:"knowledge_id"`
	Keyword         string `json:"keyword"`
	Page            int    `json:"page"`
	PageSize        int    `json:"page_size"`
}

type ManagedDocument struct {
	KnowledgeBaseID   string `json:"knowledge_base_id"`
	KnowledgeID       string `json:"knowledge_id"`
	Title             string `json:"title"`
	Type              string `json:"type"`
	PublicationStatus string `json:"publication_status"`
	ParseStatus       string `json:"parse_status"`
	EnableStatus      string `json:"enable_status"`
	Revision          string `json:"revision"`
	Content           string `json:"content,omitempty"`
	ContentAvailable  bool   `json:"content_available"`
	Notice            string `json:"notice,omitempty"`
}

type ManagedDocumentPage struct {
	KnowledgeBaseID string            `json:"knowledge_base_id"`
	Items           []ManagedDocument `json:"items"`
	Total           int64             `json:"total"`
	Page            int               `json:"page"`
	PageSize        int               `json:"page_size"`
	Notice          string            `json:"notice"`
}

func managedDocumentView(k *types.Knowledge, includeContent bool) (ManagedDocument, error) {
	view := ManagedDocument{KnowledgeBaseID: k.KnowledgeBaseID, KnowledgeID: k.ID, Title: k.Title, Type: k.Type, PublicationStatus: "not_manual", ParseStatus: k.ParseStatus, EnableStatus: k.EnableStatus, Revision: k.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if !k.IsManual() {
		if includeContent {
			view.Notice = "Only status is returned for imported files. Use the native document page for file editing; do not replace an imported file with guessed manual content."
		}
		return view, nil
	}
	metadata, err := k.ManualMetadata()
	if err != nil {
		return ManagedDocument{}, ErrInvalid
	}
	view.PublicationStatus = "unknown"
	if metadata != nil {
		if metadata.Status == types.ManualKnowledgeStatusDraft || metadata.Status == types.ManualKnowledgeStatusPublish {
			view.PublicationStatus = metadata.Status
		}
		if includeContent {
			if len(metadata.Content) > managedDocumentContentLimit {
				view.Notice = "The exact body exceeds the chat maintenance limit. Use the native document editor; do not publish a truncated or reconstructed body."
			} else {
				view.Content = metadata.Content
				view.ContentAvailable = true
			}
		}
	}
	return view, nil
}

func (s *Service) Documents(ctx context.Context, in ManagedDocumentQuery) (any, error) {
	intake, ok := PrincipalFromContext(ctx)
	if !ok || !intake.CanManageScope {
		return nil, ErrDenied
	}
	p, err := currentPrincipal(ctx)
	if err != nil || !p.CanManageScope {
		return nil, ErrDenied
	}
	if in.Action != "list" && in.Action != "get" {
		return nil, argumentError("action must be list or get; this tool never publishes or modifies a document.")
	}
	if in.Action == "list" && (len(in.Keyword) > 300 || in.Page < 0 || in.Page > 100000 || in.PageSize < 0 || in.PageSize > 50) {
		return nil, argumentError("Use page >= 1, page_size from 1 to 50, and a short document-title keyword.")
	}
	if in.Action == "get" && !validText(in.KnowledgeID, 128) {
		return nil, argumentError("get requires knowledge_id from the authorized document list.")
	}
	if len(p.ManageKnowledgeBaseIDs) == 0 {
		return nil, ErrDenied
	}
	if in.KnowledgeBaseID == "" {
		if len(p.ManageKnowledgeBaseIDs) == 1 {
			in.KnowledgeBaseID = p.ManageKnowledgeBaseIDs[0]
		} else {
			return nil, argumentError("Resolve the intended manageable knowledge_base_id with octo_configuration. Multiple manageable libraries are not interchangeable.")
		}
	}
	// Console/read grants and inherited query bindings do not expand this tool.
	// Deferred reply authorization captures the intake scope. Do not read a
	// newly granted draft outside that snapshot in this turn; it is usable on
	// the next admitted message, where delivery can also enforce its revocation.
	if !contains(intake.ManageKnowledgeBaseIDs, in.KnowledgeBaseID) || !contains(p.ManageKnowledgeBaseIDs, in.KnowledgeBaseID) {
		return nil, ErrDenied
	}
	kb, err := s.checkKB(ctx, p, in.KnowledgeBaseID, true)
	if err != nil {
		return nil, err
	}
	caller := types.CallerFromContext(ctx)
	if caller.TenantID != p.TenantID {
		return nil, ErrDenied
	}
	grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleEditor, nil, nil)
	if err != nil {
		return nil, err
	}
	ctx = grant.Context(ctx)
	if s.knowledge == nil {
		return nil, ErrInvalid
	}
	switch in.Action {
	case "list":
		page := &types.Pagination{Page: in.Page, PageSize: in.PageSize}
		result, e := s.knowledge.ListPagedKnowledgeByKnowledgeBaseID(ctx, kb.ID, page, types.KnowledgeListFilter{Keyword: strings.TrimSpace(in.Keyword)})
		if e != nil {
			return nil, e
		}
		if result == nil {
			return nil, ErrInvalid
		}
		rows, ok := result.Data.([]*types.Knowledge)
		if !ok {
			return nil, ErrInvalid
		}
		out := &ManagedDocumentPage{KnowledgeBaseID: kb.ID, Items: []ManagedDocument{}, Total: result.Total, Page: result.Page, PageSize: result.PageSize, Notice: "Management-only catalog, including drafts. Use get for the exact manual body before a publish preview. This is not public RAG evidence and an empty result is not a knowledge gap."}
		for _, row := range rows {
			if row == nil || row.TenantID != p.TenantID || row.KnowledgeBaseID != kb.ID || row.DeletedAt.Valid {
				return nil, ErrDenied
			}
			view, e := managedDocumentView(row, false)
			if e != nil {
				return nil, e
			}
			out.Items = append(out.Items, view)
		}
		return out, nil
	case "get":
		row, e := s.knowledge.GetKnowledgeByID(ctx, in.KnowledgeID)
		if e != nil {
			return nil, e
		}
		if row == nil || row.TenantID != p.TenantID || row.KnowledgeBaseID != kb.ID || row.DeletedAt.Valid {
			return nil, ErrDenied
		}
		return managedDocumentView(row, true)
	default:
		return nil, argumentError("action must be list or get; this tool never publishes or modifies a document.")
	}
}

type knowledgeDocumentsTool struct{ service *Service }

func newKnowledgeDocumentsTool(service *Service) types.Tool {
	return &knowledgeDocumentsTool{service: service}
}
func (*knowledgeDocumentsTool) Name() string { return ToolKnowledgeDocuments }
func (*knowledgeDocumentsTool) Description() string {
	return "List or get documents, draft publication state and the exact manual body in currently manageable knowledge bases. Requires fresh group management permission and an explicit asset-management grant; query-only users cannot read drafts. action=list supports title keyword and pagination; action=get needs knowledge_id from that list. knowledge_base_id defaults only to the sole manageable KB, including an authorized asset whose query binding was removed. Use this tool, NOT public RAG, to locate a saved draft, check status or prepare publication. A missing management record is not a knowledge gap. Before publish, get the exact title/body and pass them unchanged to octo_knowledge_preview; do not publish when content_available=false. This tool does not mutate or publish."
}
func (*knowledgeDocumentsTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["list","get"]},"knowledge_base_id":{"type":"string","description":"A currently manageable KB from octo_configuration; defaults only with exactly one manageable library."},"knowledge_id":{"type":"string","description":"Required for get; use the identifier returned by list, never guess it."},"keyword":{"type":"string","description":"Optional title/file-name filter for list."},"page":{"type":"integer","minimum":1},"page_size":{"type":"integer","minimum":1,"maximum":50}},"required":["action"]}`)
}
func (t *knowledgeDocumentsTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.CanManageScope {
		return toolResult(nil, ErrDenied)
	}
	var in ManagedDocumentQuery
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		return toolResult(nil, argumentError("Use the declared document query fields; identity and permissions cannot be supplied as arguments."))
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return toolResult(nil, argumentError("Supply exactly one JSON object."))
	}
	if in.Action != "list" && in.Action != "get" {
		return toolResult(nil, argumentError("action must be list or get."))
	}
	result, err := t.service.Documents(ctx, in)
	return toolResult(result, err)
}
