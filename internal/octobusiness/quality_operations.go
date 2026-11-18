package octobusiness

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type qualityBatchInput struct {
	ScopeID   string   `json:"scope_id"`
	LibraryID string   `json:"library_id"`
	Messages  []string `json:"message_ids"`
	Action    string   `json:"action"`
	Value     string   `json:"value"`
}

func (s *Service) qualityOperationRow(ctx context.Context, scope *qualityScope, id string) (qualityRow, error) {
	var entry qualityInbox
	if !validText(id, 255) {
		return qualityRow{}, ErrInvalid
	}
	if err := s.qualityInbox(ctx, scope).Where("message_id = ?", id).Take(&entry).Error; err != nil {
		return qualityRow{}, err
	}
	row, ok := entry.row(scope)
	if !ok {
		return qualityRow{}, ErrDenied
	}
	row.GroupKey = qualityGroupKey(scope, row)
	return row, nil
}

func (s *Service) qualityBatch(
	ctx context.Context, in qualityBatchInput,
) ([]operationResult, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	if !contains([]string{"review", "topic", "disposition", "link", "merge", "split"}, in.Action) ||
		len(in.Messages) == 0 || len(in.Messages) > 100 || len(in.Value) > 160 {
		return nil, ErrInvalid
	}
	if in.Action == "disposition" && !contains([]string{"", "seen", "watch", "dismiss"}, in.Value) {
		return nil, ErrInvalid
	}
	scope, err := s.qualityScope(ctx, in.ScopeID)
	if err != nil {
		return nil, err
	}
	if in.Action == "review" && !scope.permits(in.LibraryID) {
		return nil, ErrDenied
	}
	groupID := ""
	var canonical qualityRow
	switch in.Action {
	case "split":
		groupID = uuid.NewString()
	case "merge":
		canonical, err = s.qualityOperationRow(ctx, scope, in.Value)
		if err != nil {
			return nil, err
		}
		groupID = canonical.GroupKey
		var saved qualityReview
		query := s.qualityReviewQuery(ctx, scope).Where("message_id = ?", canonical.ID)
		if err := query.Take(&saved).Error; err == nil && saved.GroupID != "" {
			groupID = saved.GroupID
		}
	}
	var linked Issue
	if in.Action == "link" {
		err = s.issueQuery(ctx, p, IssueFilter{Visibility: "all"}).
			Where("id = ? AND channel_id = ? AND scope_id = ?", in.Value, scope.ChannelID, scope.ID).Take(&linked).Error
		if err != nil || !scope.permits(linked.KnowledgeBaseID) {
			return nil, ErrDenied
		}
	}
	seen := map[string]bool{}
	for _, id := range in.Messages {
		if seen[id] || !validText(id, 255) {
			return nil, ErrInvalid
		}
		seen[id] = true
	}
	results := make([]operationResult, 0, len(in.Messages))
	for _, id := range in.Messages {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			scoped := *s
			scoped.db = tx
			row, err := scoped.qualityOperationRow(ctx, scope, id)
			if err != nil {
				return err
			}
			review := qualityReview{
				TenantID: p.TenantID, ChannelID: scope.ChannelID, ScopeID: scope.ID,
				MessageID: id, UpdatedAt: time.Now().UTC(),
			}
			columns := []string{"updated_at"}
			switch in.Action {
			case "review":
				if !contains(row.libraries, in.LibraryID) {
					return ErrDenied
				}
				return scoped.enqueueQualityReview(ctx, scope, review, in.LibraryID)
			case "topic":
				review.Topic = strings.TrimSpace(in.Value)
				columns = append(columns, "topic")
			case "disposition":
				review.Disposition = in.Value
				columns = append(columns, "disposition")
			case "link":
				if !contains(row.libraries, linked.KnowledgeBaseID) {
					return ErrDenied
				}
				review.IssueID = linked.ID
				columns = append(columns, "issue_id")
			case "merge", "split":
				if in.Action == "merge" && !sameQualityLibraries(row.libraries, canonical.libraries) {
					return ErrDenied
				}
				review.GroupID = groupID
				columns = append(columns, "group_id")
			}
			if err := scoped.writeQualityReview(ctx, review, columns); err != nil {
				return err
			}
			return audit(tx, p, types.AuditAction("octo.quality."+in.Action), id,
				map[string]string{"scope_id": in.ScopeID})
		})
		results = append(results, operationResult{ID: id, State: operationState(err)})
	}
	return results, nil
}

func sameQualityLibraries(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func (s *Service) enqueueQualityReview(
	ctx context.Context, scope *qualityScope, review qualityReview, library string,
) error {
	// Upsert the row before requesting work so the conditional update also protects first-time analysis.
	if err := s.writeQualityReview(ctx, review, []string{"updated_at"}); err != nil {
		return err
	}
	query := s.qualityReviewQuery(ctx, scope).Where("message_id = ? AND state <> 'working'", review.MessageID)
	change := query.Updates(map[string]any{
		"state": "queued", "library_id": library, "available_at": nil, "attempt_count": 0,
	})
	if change.Error != nil {
		return change.Error
	}
	if change.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// BatchQuality applies maintenance actions to checked retained turns.
func (h *Handler) BatchQuality(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var in qualityBatchInput
	if c.ShouldBindJSON(&in) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	out, err := h.service.qualityBatch(ctx, in)
	respond(c, out, err)
}

// QualitySettings returns the workspace's background review configuration.
func (h *Handler) QualitySettings(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, err := currentPrincipal(ctx)
	if err != nil {
		respond(c, nil, err)
		return
	}
	out, err := h.service.readQualitySettings(ctx, p.TenantID)
	respond(c, out, err)
}

// PutQualitySettings updates the workspace's analysis configuration.
func (h *Handler) PutQualitySettings(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var in qualitySettings
	if c.ShouldBindJSON(&in) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	out, err := h.service.writeQualitySettings(ctx, h.models, in)
	respond(c, out, err)
}
