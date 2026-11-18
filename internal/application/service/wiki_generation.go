package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const wikiGenerationPageSize = 50

// WikiGenerationRetryLimit is the maximum number of documents per request.
const WikiGenerationRetryLimit = 20

type wikiRetrySummaryKey struct{}

// collectWikiRetrySummary buffers the stream and accepts only a complete
// answer. A dropped connection must not publish an incomplete summary.
func collectWikiRetrySummary(
	ctx context.Context, model chat.Chat, messages []chat.Message, opts *chat.ChatOptions,
) (*types.ChatResponse, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := model.ChatStream(streamCtx, messages, opts)
	if err != nil {
		return nil, err
	}
	var content strings.Builder
	for event := range stream {
		switch event.ResponseType {
		case types.ResponseTypeError:
			return nil, fmt.Errorf("wiki summary stream: %s", event.Content)
		case types.ResponseTypeAnswer:
			content.WriteString(event.Content)
			if event.Done && event.FinishReason != "" && event.FinishReason != types.FinishReasonIncomplete &&
				!isLengthStopFinishReason(event.FinishReason) &&
				strings.TrimSpace(content.String()) != "" {
				return &types.ChatResponse{Content: content.String(), FinishReason: event.FinishReason}, nil
			}
		}
	}
	if err := streamCtx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New(types.StreamEndedEarlyError)
}

var (
	// ErrWikiGenerationUnavailable indicates that the target Wiki cannot be used.
	ErrWikiGenerationUnavailable = errors.New("wiki is disabled or unavailable")
	// ErrWikiGenerationModel indicates that the configured synthesis model could not be loaded.
	ErrWikiGenerationModel = errors.New("wiki generation model is unavailable")
)

// WikiGenerationTask shows the current status of a document with a recorded failure.
type WikiGenerationTask struct {
	KnowledgeID string     `json:"knowledge_id"`
	Title       string     `json:"title"`
	Status      string     `json:"status"`
	ErrorCode   string     `json:"error_code,omitempty"`
	FailedAt    *time.Time `json:"failed_at,omitempty"`
	Attempts    int        `json:"attempts"`
}

// WikiGenerationPage is one page of document-level generation tasks.
type WikiGenerationPage struct {
	Tasks      []WikiGenerationTask `json:"tasks"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// WikiGenerationRetryResult reports accepted and skipped documents.
type WikiGenerationRetryResult struct {
	Queued          []string `json:"queued"`
	Skipped         int      `json:"skipped"`
	DispatchPending bool     `json:"dispatch_pending,omitempty"`
}

// WikiGenerationService restores only Wiki ingest operations. The original
// processing attempt, document chunks, and its other indexing jobs are untouched.
type WikiGenerationService struct {
	db    *gorm.DB
	queue interfaces.TaskEnqueuer
	model interfaces.ModelService
	kb    interfaces.KnowledgeBaseService
}

// NewWikiGenerationService wires the Wiki maintenance service to the durable queue.
func NewWikiGenerationService(
	db *gorm.DB, queue interfaces.TaskEnqueuer, model interfaces.ModelService, kb interfaces.KnowledgeBaseService,
) *WikiGenerationService {
	return &WikiGenerationService{db: db, queue: queue, model: model, kb: kb}
}

// List reads generation failures and the current status within one knowledge base.
func (s *WikiGenerationService) List(
	ctx context.Context, tenantID uint64, kbID, cursor string,
) (WikiGenerationPage, error) {
	rows, err := repository.ListWikiGenerationRows(
		s.db.WithContext(ctx), tenantID, kbID, wikiTaskType, cursor, nil, wikiGenerationPageSize+1,
	)
	if err != nil {
		return WikiGenerationPage{}, err
	}
	result := WikiGenerationPage{Tasks: make([]WikiGenerationTask, 0, len(rows))}
	if len(rows) > wikiGenerationPageSize {
		rows = rows[:wikiGenerationPageSize]
		result.NextCursor = rows[len(rows)-1].KnowledgeID
	}
	now := time.Now()
	for _, row := range rows {
		result.Tasks = append(result.Tasks, wikiGenerationTask(row, now))
	}
	return result, nil
}

func wikiGenerationTask(row repository.WikiGenerationRow, now time.Time) WikiGenerationTask {
	task := WikiGenerationTask{
		KnowledgeID: row.KnowledgeID,
		Title:       row.Title,
		Status:      "failed",
		Attempts:    row.FailCount,
	}
	if !row.FailedAt.IsZero() {
		task.FailedAt = &row.FailedAt
	}
	switch {
	case row.PendingID != 0:
		task.Status = "queued"
		task.Attempts = row.RetryCount
		freshClaim := row.ClaimedAt != nil && now.Sub(*row.ClaimedAt) < wikiClaimStaleAfter
		activeSpan := row.SpanStatus == types.SpanStatusRunning && row.SpanStarted != nil &&
			row.EnqueuedAt != nil && !row.SpanStarted.Before(*row.EnqueuedAt) &&
			now.Sub(*row.SpanStarted) < wikiClaimStaleAfter
		if freshClaim || activeSpan {
			task.Status = "running"
		}
	case row.Published:
		task.Status = "published"
	case row.EnableStatus != "enabled" || row.ParseStatus != types.ParseStatusCompleted:
		task.Status = "unavailable"
	case row.SpanStatus == types.SpanStatusSkipped &&
		(row.SpanError == "no_chunks" || row.SpanError == "insufficient_text_content"):
		task.Status = "unavailable"
		task.ErrorCode = "no_text"
	default:
		task.ErrorCode = wikiGenerationIssue(row.SpanError, row.SpanCode)
	}
	return task
}

func wikiGenerationIssue(message, code string) string {
	message = strings.ToLower(message)
	switch {
	case strings.Contains(message, "504"),
		strings.Contains(message, "deadline exceeded"),
		strings.Contains(message, "timeout"):
		return "gateway_timeout"
	case strings.Contains(message, "503"):
		return "gateway_unavailable"
	case isLikelyRateLimitError(errors.New(message)):
		return "rate_limited"
	case code == "EXTRACT_FAILED":
		return "extract_failed"
	case code == "SUMMARY_FAILED":
		return "summary_failed"
	default:
		return "generation_failed"
	}
}

func wikiSynthesisModelID(kb *types.KnowledgeBase) string {
	if kb.WikiConfig != nil && kb.WikiConfig.SynthesisModelID != "" {
		return kb.WikiConfig.SynthesisModelID
	}
	return kb.SummaryModelID
}

// Retry restores eligible document failures to the Wiki ingest queue.
func (s *WikiGenerationService) Retry(
	ctx context.Context, tenantID uint64, kbID string, ids []string,
) (WikiGenerationRetryResult, error) {
	result := WikiGenerationRetryResult{Queued: make([]string, 0, len(ids))}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	kb, err := s.kb.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		return result, err
	}
	if kb == nil || kb.TenantID != tenantID || !kb.IsWikiEnabled() {
		return result, ErrWikiGenerationUnavailable
	}
	modelID := wikiSynthesisModelID(kb)
	if modelID == "" {
		return result, ErrWikiGenerationModel
	}
	if _, err := s.model.GetChatModel(ctx, modelID); err != nil {
		logger.Warnf(ctx, "wiki retry: model unavailable for KB %s: %v", kbID, err)
		return result, ErrWikiGenerationModel
	}

	alreadyPending := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Select("id").Where("id = ? AND tenant_id = ?", kbID, tenantID)
		docQuery := tx.Model(&types.Knowledge{}).Select("id").
			Where("id IN ? AND tenant_id = ? AND knowledge_base_id = ?", ids, tenantID, kbID).
			Order("id")
		if tx.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "SHARE"})
			docQuery = docQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var lockedKB types.KnowledgeBase
		if err := query.Take(&lockedKB).Error; err != nil {
			return err
		}
		var lockedDocs []types.Knowledge
		if err := docQuery.Find(&lockedDocs).Error; err != nil {
			return err
		}
		rows, err := repository.ListWikiGenerationRows(tx, tenantID, kbID, wikiTaskType, "", ids, len(ids))
		if err != nil {
			return err
		}
		now := time.Now()
		for _, row := range rows {
			if row.PendingID != 0 {
				alreadyPending = true
			}
			if wikiGenerationTask(row, now).Status != "failed" {
				continue
			}
			var archived WikiPendingOp
			if err := json.Unmarshal(row.Failure, &archived); err != nil {
				return err
			}
			language := archived.Language
			if language == "" {
				language = types.LanguageFromContextOrDefault(ctx)
			}
			payload, err := json.Marshal(WikiPendingOp{
				Op: WikiOpIngest, KnowledgeID: row.KnowledgeID, Language: language, ManualRetry: true,
			})
			if err != nil {
				return err
			}
			op := types.TaskPendingOp{
				TenantID: tenantID, TaskType: wikiTaskType, Scope: wikiTaskScope, ScopeID: kbID,
				Op: WikiOpIngest, DedupKey: row.KnowledgeID, Payload: payload, EnqueuedAt: now,
			}
			if err := tx.Create(&op).Error; err != nil {
				return err
			}
			result.Queued = append(result.Queued, row.KnowledgeID)
		}
		return nil
	})
	if err != nil {
		return WikiGenerationRetryResult{}, err
	}
	result.Skipped = len(ids) - len(result.Queued)
	if len(result.Queued) > 0 || alreadyPending {
		if err := enqueueWikiIngestTrigger(ctx, s.queue, tenantID, kbID); err != nil {
			logger.Warnf(ctx, "wiki retry: persisted operations for KB %s but dispatch failed: %v", kbID, err)
			result.DispatchPending = true
		}
	}
	return result, nil
}
