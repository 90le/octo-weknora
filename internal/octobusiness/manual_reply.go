package octobusiness

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ManualReplyTarget is resolved from the retained inbox before delivery.
type ManualReplyTarget struct {
	TenantID                                 uint64
	ChannelID, ScopeID, AccountID, LibraryID string
	InboxID, SourceDigest                    string
}

type manualReply struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	TenantID  uint64    `json:"-"`
	IssueID   string    `json:"-"`
	ActorUID  string    `json:"actor_uid"`
	ChannelID string    `json:"channel_id"`
	ScopeName string    `json:"scope_name"`
	Target    string    `json:"target"`
	UserName  string    `json:"user_name"`
	UserUID   string    `json:"user_uid"`
	MessageID string    `json:"message_id"`
	Body      string    `json:"body"`
	Signature string    `json:"-"`
	Status    string    `json:"status"`
	Receipt   string    `json:"receipt"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (manualReply) TableName() string { return "octo_issue_replies" }

var errReplySource = errors.New("no authorized original Octo message is available")

func (s *Service) manualReplyTarget(
	ctx context.Context, issue *Issue, target string,
) (ManualReplyTarget, *evidenceMessage, error) {
	var result ManualReplyTarget
	if issue.IsDirect || issue.ArchiveState != "" || !validText(issue.MessageID, 255) || issue.GroupID == "" {
		return result, nil, errReplySource
	}
	if _, err := s.retestAgent(ctx, issue); err != nil {
		return result, nil, errReplySource
	}
	lookup := func(id string) (evidenceInbox, error) {
		var entry evidenceInbox
		err := s.db.WithContext(ctx).Table("octo_message_inbox").
			Where("tenant_id = ? AND channel_id = ? AND message_id = ?", issue.TenantID, issue.ChannelID, id).Take(&entry).Error
		return entry, err
	}
	inboxID := issue.MessageID
	row, err := lookup(inboxID)
	if err != nil {
		return result, nil, errReplySource
	}
	msg := row.message(issue)
	if msg == nil {
		return result, nil, errReplySource
	}
	if target == "question" && issue.IdempotencyKey != qualityIssueKey(issue) {
		originalID, originalUser := msg.originalQuestionAnchor()
		if originalID == "" || originalUser == "" || msg.Quote == nil || !msg.Quote.IsBotMessage ||
			!strings.HasPrefix(inboxID, "octo:"+msg.Quote.SenderID+":") {
			return result, nil, errReplySource
		}
		inboxID = "octo:" + msg.Quote.SenderID + ":" + originalID
		row, err = lookup(inboxID)
		if err != nil || row.State != "delivered" {
			return result, nil, errReplySource
		}
		sourceIssue := *issue
		sourceIssue.ReporterUID = originalUser
		msg = row.message(&sourceIssue)
		if msg == nil || msg.Extra["octo_message_id"] != originalID {
			return result, nil, errReplySource
		}
	}
	if msg.Extra["octo_card_event_key"] != "" || decimalNativeID([]byte(msg.Extra["octo_message_id"])) == "" {
		return result, nil, errReplySource
	}
	result = ManualReplyTarget{
		TenantID: issue.TenantID, ChannelID: issue.ChannelID, ScopeID: issue.ScopeID, AccountID: issue.AccountID,
		LibraryID: issue.KnowledgeBaseID, InboxID: inboxID, SourceDigest: digest(row.Input, row.Authority),
	}
	return result, msg, nil
}

// ManualSourceDigest binds a prepared reply to the evidence loaded by Octo's adapter.
func ManualSourceDigest(input, authority string) string { return digest(input, authority) }

func (h *Handler) PreviewIssueReply(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var input struct {
		Target string `json:"target"`
		Body   string `json:"body"`
	}
	if c.ShouldBindJSON(&input) != nil || !contains([]string{"question", "feedback"}, input.Target) ||
		!validText(input.Body, 2500) {
		respond(c, nil, ErrInvalid)
		return
	}
	p, _ := currentPrincipal(ctx)
	issue, _, err := h.service.GetIssue(ctx, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	target, msg, err := h.service.manualReplyTarget(ctx, issue, input.Target)
	if err != nil {
		respond(c, nil, err)
		return
	}
	reply := manualReply{
		ID: uuid.NewString(), TenantID: p.TenantID, IssueID: issue.ID, ActorUID: p.UserID,
		ChannelID: issue.ChannelID, ScopeName: issue.ScopeName, Target: input.Target,
		UserName: msg.UserName, UserUID: msg.UserID, MessageID: msg.Extra["octo_message_id"],
		Body: "【人工回复】\n" + strings.TrimSpace(input.Body), Signature: target.SourceDigest, Status: "preview",
	}
	err = h.service.db.WithContext(ctx).Create(&reply).Error
	respond(c, reply, err)
}

func (h *Handler) SendIssueReply(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	reply, err := h.service.sendIssueReply(ctx, c.Param("id"), c.Param("reply"))
	respond(c, reply, err)
}

func (s *Service) sendIssueReply(ctx context.Context, issueID, id string) (*manualReply, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	issue, _, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	query := func(call context.Context) *gorm.DB {
		return s.db.WithContext(call).Model(&manualReply{}).Where("tenant_id = ? AND issue_id = ? AND id = ?", p.TenantID, issue.ID, id)
	}
	var reply manualReply
	if err = query(ctx).Where("actor_uid = ?", p.UserID).Take(&reply).Error; err != nil {
		return nil, err
	}
	if reply.Status != "preview" {
		return &reply, nil
	}
	if time.Since(reply.CreatedAt) > 15*time.Minute || s.ManualReplyDelivery == nil {
		return nil, ErrConflict
	}
	target, _, err := s.manualReplyTarget(ctx, issue, reply.Target)
	if err != nil || target.SourceDigest != reply.Signature {
		return nil, errReplySource
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		claim := tx.Model(&manualReply{}).Where("id = ? AND tenant_id = ? AND status = ?", reply.ID, p.TenantID, "preview").
			Update("status", "sending")
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return ErrConflict
		}
		return audit(tx, p, "octo.issue.reply_claimed", issue.ID, map[string]string{"reply_id": reply.ID})
	})
	if err != nil {
		return nil, err
	}
	receipt, sendErr := s.ManualReplyDelivery(ctx, target, reply.ID, reply.Body)
	reply.Status, reply.Receipt = "sent", receipt
	if sendErr != nil || receipt == "" {
		reply.Status = "uncertain"
		if errors.Is(sendErr, ErrDenied) {
			reply.Status = "blocked"
		}
	}
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = query(write).Updates(map[string]any{"status": reply.Status, "receipt": reply.Receipt}).Error
	return &reply, err
}

func (h *Handler) IssueReplies(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	issue, _, err := h.service.GetIssue(ctx, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	rows := []manualReply{}
	err = h.service.db.WithContext(ctx).Where("tenant_id = ? AND issue_id = ? AND status <> 'preview'", issue.TenantID, issue.ID).
		Order("created_at DESC, id").Limit(50).Find(&rows).Error
	for i := range rows {
		if rows[i].Status == "sending" && time.Since(rows[i].UpdatedAt) > 2*time.Minute {
			rows[i].Status = "uncertain"
		}
	}
	respond(c, rows, err)
}
