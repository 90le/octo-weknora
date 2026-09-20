package octobusiness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db        *gorm.DB
	kb        interfaces.KnowledgeBaseService
	knowledge interfaces.KnowledgeService
	// Management validates and executes additional native operations (e.g. KB
	// creation/bindings). It must authorize on both preview and confirmation.
	Management      ManagementExecutor
	ReportPrincipal func(context.Context, ReportSchedule) (context.Context, error)
	ReportDelivery  func(context.Context, ReportSchedule, *Report, *ReportArtifact) error
	reportCron      *cron.Cron
}

func NewService(db *gorm.DB, kb interfaces.KnowledgeBaseService, knowledge interfaces.KnowledgeService) *Service {
	return &Service{db: db, kb: kb, knowledge: knowledge}
}

func contains(ids []string, id string) bool {
	for _, item := range ids {
		if item == id {
			return true
		}
	}
	return false
}
func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
func validText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && !strings.ContainsRune(s, 0)
}

func (s *Service) checkKB(ctx context.Context, p Principal, id string, write bool) (*types.KnowledgeBase, error) {
	if !validText(id, 128) || (!p.Console && !write && !contains(p.KnowledgeBaseIDs, id)) || (write && !p.Console && !contains(p.ManageKnowledgeBaseIDs, id)) {
		return nil, ErrDenied
	}
	var kb types.KnowledgeBase
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", p.TenantID, id).First(&kb).Error; err != nil {
		return nil, err
	}
	if err := types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, id); err != nil {
		return nil, ErrDenied
	}
	return &kb, nil
}

func (s *Service) Contacts(ctx context.Context, kb string) ([]Contact, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	q := s.db.WithContext(ctx).Where("tenant_id = ?", p.TenantID)
	if kb != "" {
		if _, err = s.checkKB(ctx, p, kb, false); err != nil {
			return nil, err
		}
		q = q.Where("knowledge_base_id = ?", kb)
	} else if !p.Console {
		q = q.Where("knowledge_base_id IN ?", p.KnowledgeBaseIDs)
	}
	rows := []Contact{}
	err = q.Order("is_default DESC, topic, name, id").Limit(200).Find(&rows).Error
	return rows, err
}

func (s *Service) PutContact(ctx context.Context, input Contact) (*Contact, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.checkKB(ctx, p, input.KnowledgeBaseID, true); err != nil {
		return nil, err
	}
	if !validText(input.Name, 256) || !validText(input.UID, 128) || len(input.Topic) > 256 || len(input.Details) > 2000 {
		return nil, ErrInvalid
	}
	input.TenantID = p.TenantID
	if input.ID == "" {
		input.ID = uuid.NewString()
		input.CreatedAt = time.Now()
	} else {
		var previous Contact
		if err = s.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND knowledge_base_id = ?", p.TenantID, input.ID, input.KnowledgeBaseID).First(&previous).Error; err != nil {
			return nil, err
		}
		input.CreatedAt = previous.CreatedAt
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if input.IsDefault {
			if e := tx.Model(&Contact{}).Where("tenant_id = ? AND knowledge_base_id = ? AND id <> ?", p.TenantID, input.KnowledgeBaseID, input.ID).Update("is_default", false).Error; e != nil {
				return e
			}
		}
		if e := tx.Save(&input).Error; e != nil {
			return e
		}
		return audit(tx, p, "octo.contact.updated", input.ID, map[string]string{"knowledge_base_id": input.KnowledgeBaseID})
	})
	return &input, err
}

func (s *Service) DeleteContact(ctx context.Context, id string) error {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return err
	}
	var row Contact
	if err = s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", p.TenantID, id).First(&row).Error; err != nil {
		return err
	}
	if _, err = s.checkKB(ctx, p, row.KnowledgeBaseID, true); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Delete(&row).Error; e != nil {
			return e
		}
		return audit(tx, p, "octo.contact.deleted", id, nil)
	})
}

type IssueInput struct {
	Kind            string `json:"kind"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Expected        string `json:"expected"`
	Steps           string `json:"steps"`
	// Semantic assessment is the Agent's responsibility, but unsupported saves
	// are rejected structurally. Retrieval outages are explicitly not gaps.
	MissingFields   []string `json:"missing_fields"`
	RetrievalStatus string   `json:"retrieval_status"`
}

func (s *Service) CreateIssue(ctx context.Context, in IssueInput) (*Issue, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if p.MessageID == "" || p.MessageText == "" || (!p.IsDirect && p.ScopeID == "") {
		return nil, ErrDenied
	}
	if _, err = s.checkKB(ctx, p, in.KnowledgeBaseID, false); err != nil {
		return nil, err
	}
	if in.Kind != "missing" && in.Kind != "bug" && in.Kind != "suggestion" {
		return nil, ErrInvalid
	}
	if len(in.MissingFields) > 0 || !validText(in.Title, 300) || !validText(in.Description, 12000) {
		return nil, ErrClarify
	}
	if in.Kind == "missing" && (in.RetrievalStatus != "no_answer" || !retrievalReady(ctx)) {
		return nil, ErrInvalid
	}
	if in.Kind == "bug" && (!validText(in.Expected, 4000) || !validText(in.Steps, 6000)) {
		return nil, ErrClarify
	}
	attachments, _ := json.Marshal(p.Attachments)
	if p.Attachments == nil {
		attachments = []byte("[]")
	}
	key := digest("issue", fmtTenant(p.TenantID), p.AccountID, p.ChannelID, p.ScopeID, p.UserID, p.MessageID, in.Kind)
	row := Issue{ID: uuid.NewString(), TenantID: p.TenantID, AccountID: p.AccountID, ChannelID: p.ChannelID, ScopeID: p.ScopeID, ScopeName: p.ScopeName, GroupID: p.GroupID, SubareaID: p.SubareaID, IsDirect: p.IsDirect, KnowledgeBaseID: in.KnowledgeBaseID, Kind: in.Kind, Title: strings.TrimSpace(in.Title), Description: strings.TrimSpace(in.Description), Expected: in.Expected, Steps: in.Steps, ReporterUID: p.UserID, ReporterName: p.UserName, MessageID: p.MessageID, OriginalMessage: p.MessageText, Attachments: types.JSON(attachments), Status: "open", IdempotencyKey: key}
	// A single configured default is a routing hint, never an automatic message.
	var contact Contact
	if s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND is_default = ?", p.TenantID, in.KnowledgeBaseID, true).Order("id").First(&contact).Error == nil {
		row.OwnerUID = contact.UID
		row.OwnerName = contact.Name
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "idempotency_key"}}, DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			row = Issue{}
			return tx.Where("idempotency_key = ?", key).First(&row).Error
		}
		return audit(tx, p, "octo.issue.created", row.ID, map[string]string{"kind": row.Kind, "scope_id": row.ScopeID})
	})
	return &row, err
}

type IssueFilter struct {
	ScopeID, KnowledgeBaseID, Kind, Status, Keyword string
	From, To                                        *time.Time
	Page, PageSize                                  int
}
type IssuePage struct {
	Items    []Issue `json:"items"`
	Total    int64   `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
}

func (s *Service) issueQuery(ctx context.Context, p Principal, f IssueFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&Issue{}).Where("tenant_id = ?", p.TenantID)
	if !p.Console {
		if !p.IsDirect && len(p.ReadIssueScopeIDs) > 0 {
			q = q.Where("account_id = ? AND scope_id IN ? AND is_direct = ?", p.AccountID, append(append([]string(nil), p.ReadIssueScopeIDs...), p.ScopeID), false)
		} else {
			q = q.Where("account_id = ? AND channel_id = ? AND scope_id = ? AND is_direct = ?", p.AccountID, p.ChannelID, p.ScopeID, p.IsDirect)
		}
		if p.IsDirect {
			q = q.Where("reporter_uid = ?", p.UserID)
		}
		issueKBs := append([]string(nil), p.KnowledgeBaseIDs...)
		if !p.IsDirect && len(p.ReadIssueScopeIDs) > 0 {
			issueKBs = append(issueKBs, p.ReadIssueKnowledgeBaseIDs...)
		}
		q = q.Where("knowledge_base_id IN ?", issueKBs)
	}
	if f.ScopeID != "" {
		q = q.Where("scope_id = ?", f.ScopeID)
	}
	if f.KnowledgeBaseID != "" {
		q = q.Where("knowledge_base_id = ?", f.KnowledgeBaseID)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Keyword != "" {
		q = q.Where("LOWER(title) LIKE ? OR LOWER(description) LIKE ?", "%"+strings.ToLower(f.Keyword)+"%", "%"+strings.ToLower(f.Keyword)+"%")
	}
	if f.From != nil {
		q = q.Where("created_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("created_at < ?", *f.To)
	}
	return q
}

func (s *Service) ListIssues(ctx context.Context, f IssueFilter) (*IssuePage, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 30
	}
	if f.PageSize > 200 {
		f.PageSize = 200
	}
	if len(f.Keyword) > 500 || f.Page > 100000 || f.From != nil && f.To != nil && !f.To.After(*f.From) {
		return nil, ErrInvalid
	}
	result := &IssuePage{Items: []Issue{}, Page: f.Page, PageSize: f.PageSize}
	q := s.issueQuery(ctx, p, f)
	if err = q.Count(&result.Total).Error; err != nil {
		return nil, err
	}
	err = q.Order("created_at DESC, id").Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&result.Items).Error
	return result, err
}

func (s *Service) GetIssue(ctx context.Context, id string) (*Issue, []IssueEvent, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, nil, err
	}
	var row Issue
	if err = s.issueQuery(ctx, p, IssueFilter{}).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, nil, err
	}
	events := []IssueEvent{}
	err = s.db.WithContext(ctx).Where("tenant_id = ? AND issue_id = ?", p.TenantID, id).Order("created_at, id").Find(&events).Error
	return &row, events, err
}

type IssueUpdate struct {
	Status    string  `json:"status"`
	OwnerUID  *string `json:"owner_uid"`
	OwnerName *string `json:"owner_name"`
	Note      string  `json:"note"`
}

func (s *Service) UpdateIssue(ctx context.Context, id string, in IssueUpdate) (*Issue, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if in.Status != "open" && in.Status != "inprogress" && in.Status != "resolved" && in.Status != "closed" {
		return nil, ErrInvalid
	}
	if len(in.Note) > 4000 || (in.OwnerUID == nil) != (in.OwnerName == nil) {
		return nil, ErrInvalid
	}
	var row Issue
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		copyService := *s
		copyService.db = tx
		// Aggregation is a read capability, never a maintenance grant.
		p.ReadIssueScopeIDs = nil
		p.ReadIssueKnowledgeBaseIDs = nil
		if e := copyService.issueQuery(ctx, p, IssueFilter{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&row).Error; e != nil {
			return e
		}
		manager := p.Console || p.CanManageScope
		if !manager && (row.ReporterUID != p.UserID || in.OwnerUID != nil || in.Status == "resolved" || in.Status == "inprogress") {
			return ErrDenied
		}
		encodedUpdate, _ := json.Marshal(in)
		eventKey := uuid.NewString()
		if p.MessageID != "" {
			eventKey = digest("issue_update", fmtTenant(p.TenantID), p.ChannelID, p.UserID, p.MessageID, id, string(encodedUpdate))
			var existing int64
			if e := tx.Model(&IssueEvent{}).Where("idempotency_key = ?", eventKey).Count(&existing).Error; e != nil {
				return e
			}
			if existing != 0 {
				return nil
			}
		}
		if in.OwnerUID != nil {
			if len(*in.OwnerUID) > 128 || len(*in.OwnerName) > 256 {
				return ErrInvalid
			}
			row.OwnerUID = *in.OwnerUID
			row.OwnerName = *in.OwnerName
		}
		if row.Status == in.Status && in.Note == "" && in.OwnerUID == nil {
			return nil
		}
		row.Status = in.Status
		if e := tx.Save(&row).Error; e != nil {
			return e
		}
		if e := tx.Create(&IssueEvent{ID: uuid.NewString(), TenantID: p.TenantID, IssueID: id, ActorUID: p.UserID, ActorName: p.UserName, Status: in.Status, Note: in.Note, IdempotencyKey: eventKey}).Error; e != nil {
			return e
		}
		return audit(tx, p, "octo.issue.updated", id, map[string]string{"status": in.Status})
	})
	return &row, err
}

func audit(tx *gorm.DB, p Principal, action types.AuditAction, id string, details any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return tx.Create(&types.AuditLog{TenantID: p.TenantID, ActorUserID: p.UserID, ActorRole: "octo", Action: action, ScopeType: "octo_business", ScopeID: id, Details: types.JSON(b), CreatedAt: time.Now()}).Error
}
