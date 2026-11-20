package service

import (
	"context"
	"errors"

	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

var errPreparedFileIngestFailed = errors.New("document processing failed; previous version preserved")

type sourceIngestFailure struct {
	code    string
	message string
	cause   error
}

func (e *sourceIngestFailure) Error() string { return e.message }

func (e *sourceIngestFailure) Unwrap() error { return e.cause }

func (s *DataSourceService) preparedFileFailure(ctx context.Context, knowledgeID string, cause error) error {
	if s.processingSpans == nil || ctx.Err() != nil {
		return cause
	}
	attempt, err := s.processingSpans.LatestAttempt(ctx, knowledgeID)
	if err != nil || attempt == 0 {
		return cause
	}
	spans, err := s.processingSpans.ListByAttempt(ctx, knowledgeID, attempt)
	if err != nil {
		return cause
	}
	for _, span := range spans {
		if span.Status != types.SpanStatusFailed {
			continue
		}
		switch span.ErrorCode {
		case werrors.ErrCodeVectorStoreWriteFailed:
			return &sourceIngestFailure{
				code:    "vector_index_failed",
				message: "Vector index unavailable or write failed. Retry when the vector store is healthy.",
				cause:   cause,
			}
		case werrors.ErrCodeEmbeddingProviderFail, werrors.ErrCodeEmbeddingRateLimit:
			return &sourceIngestFailure{
				code:    "embedding_failed",
				message: "Embedding failed. Check the configured model and retry.",
				cause:   cause,
			}
		case werrors.ErrCodeDocReaderParseFailed, werrors.ErrCodeDocReaderTimeout,
			werrors.ErrCodeDocReaderUnavailable, werrors.ErrCodeChunkingFailed:
			return &sourceIngestFailure{
				code:    "document_parse_failed",
				message: "Document parsing or chunking failed. Check the file and parser, then retry.",
				cause:   cause,
			}
		}
	}
	return cause
}
