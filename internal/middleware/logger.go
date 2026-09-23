package middleware

import (
	"context"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestID middleware adds a unique request ID to the context.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		c.Header("X-Request-ID", requestID)
		c.Set(types.RequestIDContextKey.String(), requestID)

		requestLogger := logger.GetLogger(c).WithField("request_id_sha256", secutils.HashRequestIDForLog(requestID))
		c.Set(types.LoggerContextKey.String(), requestLogger)
		c.Request = c.Request.WithContext(
			context.WithValue(
				context.WithValue(c.Request.Context(), types.RequestIDContextKey, requestID),
				types.LoggerContextKey, requestLogger,
			),
		)
		c.Next()
	}
}

// Logger records bounded transport metadata only. The generic access log must
// never read, replay, or retain request/response bodies: ordinary QA payloads
// contain user questions, quoted messages, attachments, and generated answers.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestPath := c.Request.URL.Path
		isWikiStats := strings.HasPrefix(requestPath, "/api/v1/knowledgebase/") && strings.HasSuffix(requestPath, "/wiki/stats")
		if strings.HasPrefix(requestPath, "/assets/") || isWikiStats {
			c.Next()
			return
		}

		c.Next()

		// Gin's registered route template omits user-controlled path segments.
		// An unmatched request may contain arbitrary input in its URL path.
		path := c.FullPath()
		if path == "" {
			path = "[unmatched route]"
		}
		statusCode := c.Writer.Status()
		logMsg := logger.GetLogger(c).WithFields(map[string]interface{}{
			"request_id_sha256": secutils.HashRequestIDForLog(c.GetString(types.RequestIDContextKey.String())),
			"method":            secutils.SanitizeForLog(c.Request.Method),
			"path":              path,
			"status_code":       statusCode,
			"request_bytes":     c.Request.ContentLength,
			"response_bytes":    c.Writer.Size(),
			"latency":           time.Since(start).String(),
			"client_ip":         secutils.SanitizeForLog(c.ClientIP()),
			"has_query":         c.Request.URL.RawQuery != "",
			"error_count":       len(c.Errors),
		})
		// Do not append c.Errors.Last().Err: parser/provider errors can contain
		// original request or response fragments.
		switch {
		case statusCode >= 500:
			logMsg.Error()
		case statusCode >= 400:
			logMsg.Warn()
		default:
			logMsg.Info()
		}
	}
}
