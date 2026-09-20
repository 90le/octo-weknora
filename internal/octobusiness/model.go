// Package octobusiness contains the bounded knowledge-assistant operations shared
// by the native Octo channel and the authenticated WeKnora administration UI.
package octobusiness

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

var (
	ErrDenied   = errors.New("operation is not authorized in this conversation")
	ErrInvalid  = errors.New("invalid knowledge operation")
	ErrConflict = errors.New("operation changed or is already executing; refresh before retrying")
	ErrClarify  = errors.New("clarify the missing facts before registering this issue")
)

type Attachment struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Type string `json:"type"`
}

// Principal is constructed by trusted IM admission code, never from tool or
// HTTP input. Validate refreshes native roles/bindings at every invocation.
type Principal struct {
	CanCreateKnowledgeBase                                       bool
	TenantID                                                     uint64
	AccountID, ChannelID, ScopeID, ScopeName, GroupID, SubareaID string
	UserID, UserName, MessageID, MessageText                     string
	Attachments                                                  []Attachment
	KnowledgeBaseIDs, ManageKnowledgeBaseIDs                     []string
	ReadIssueScopeIDs                                            []string
	ReadIssueKnowledgeBaseIDs                                    []string
	CanManageScope, IsDirect, Console                            bool
	Validate                                                     func(context.Context) (Principal, error)
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	principal.KnowledgeBaseIDs = append([]string(nil), principal.KnowledgeBaseIDs...)
	principal.ManageKnowledgeBaseIDs = append([]string(nil), principal.ManageKnowledgeBaseIDs...)
	principal.ReadIssueScopeIDs = append([]string(nil), principal.ReadIssueScopeIDs...)
	principal.ReadIssueKnowledgeBaseIDs = append([]string(nil), principal.ReadIssueKnowledgeBaseIDs...)
	principal.Attachments = append([]Attachment(nil), principal.Attachments...)
	return context.WithValue(ctx, principalKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// ValidatedPrincipal refreshes the native authorization before an outbound
// integration uses scoped metadata such as citation links.
func ValidatedPrincipal(ctx context.Context) (Principal, error) { return currentPrincipal(ctx) }

func currentPrincipal(ctx context.Context) (Principal, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.TenantID == 0 || p.UserID == "" || p.ChannelID == "" {
		return Principal{}, ErrDenied
	}
	if p.Validate != nil {
		fresh, err := p.Validate(ctx)
		if err != nil || fresh.TenantID != p.TenantID || fresh.AccountID != p.AccountID || fresh.ChannelID != p.ChannelID || fresh.UserID != p.UserID || fresh.ScopeID != p.ScopeID || fresh.IsDirect != p.IsDirect || fresh.Console != p.Console {
			return Principal{}, ErrDenied
		}
		// Message identity/content are immutable transport facts, not refreshed model input.
		fresh.MessageID, fresh.MessageText, fresh.Attachments = p.MessageID, p.MessageText, p.Attachments
		return fresh, nil
	}
	if !p.Console {
		return Principal{}, ErrDenied
	}
	return p, nil
}

type Issue struct {
	ID              string     `json:"id" gorm:"primaryKey"`
	TenantID        uint64     `json:"-"`
	AccountID       string     `json:"account_id"`
	ChannelID       string     `json:"channel_id"`
	ScopeID         string     `json:"scope_id"`
	ScopeName       string     `json:"scope_name"`
	GroupID         string     `json:"group_id"`
	SubareaID       string     `json:"subarea_id"`
	IsDirect        bool       `json:"is_direct"`
	KnowledgeBaseID string     `json:"knowledge_base_id"`
	Kind            string     `json:"kind"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Expected        string     `json:"expected"`
	Steps           string     `json:"steps"`
	ReporterUID     string     `json:"reporter_uid"`
	ReporterName    string     `json:"reporter_name"`
	MessageID       string     `json:"message_id"`
	OriginalMessage string     `json:"original_message"`
	Attachments     types.JSON `json:"attachments" gorm:"type:json"`
	OwnerUID        string     `json:"owner_uid"`
	OwnerName       string     `json:"owner_name"`
	Status          string     `json:"status"`
	IdempotencyKey  string     `json:"-" gorm:"uniqueIndex"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (Issue) TableName() string { return "octo_knowledge_issues" }

type IssueEvent struct {
	ID             string    `json:"id" gorm:"primaryKey"`
	TenantID       uint64    `json:"-"`
	IssueID        string    `json:"issue_id"`
	ActorUID       string    `json:"actor_uid"`
	ActorName      string    `json:"actor_name"`
	Status         string    `json:"status"`
	Note           string    `json:"note"`
	IdempotencyKey string    `json:"-" gorm:"uniqueIndex"`
	CreatedAt      time.Time `json:"created_at"`
}

func (IssueEvent) TableName() string { return "octo_knowledge_issue_events" }

type Contact struct {
	ID              string    `json:"id" gorm:"primaryKey"`
	TenantID        uint64    `json:"-"`
	KnowledgeBaseID string    `json:"knowledge_base_id"`
	Topic           string    `json:"topic"`
	Name            string    `json:"name"`
	UID             string    `json:"uid"`
	Details         string    `json:"details"`
	IsDefault       bool      `json:"is_default"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (Contact) TableName() string { return "octo_knowledge_contacts" }

type Proposal struct {
	ID              string     `json:"id" gorm:"primaryKey"`
	TenantID        uint64     `json:"-"`
	AccountID       string     `json:"-"`
	ChannelID       string     `json:"-"`
	ScopeID         string     `json:"-"`
	UserID          string     `json:"-"`
	Action          string     `json:"action"`
	KnowledgeBaseID string     `json:"knowledge_base_id"`
	Payload         types.JSON `json:"payload" gorm:"type:json"`
	Status          string     `json:"status"`
	Result          types.JSON `json:"result,omitempty" gorm:"type:json"`
	SourceMessageID string     `json:"-"`
	IdempotencyKey  string     `json:"-" gorm:"uniqueIndex"`
	ExpiresAt       time.Time  `json:"expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (Proposal) TableName() string { return "octo_knowledge_proposals" }
