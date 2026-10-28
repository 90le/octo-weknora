package middleware

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	maxBodySize = 1024 * 10 // 最大记录10KB的body内容
	// bodyTruncatedMarker 是日志里跟在被截断的 body 后面的标记。
	bodyTruncatedMarker = "... [内容过长，已截断]"
)

// replayedBody 把日志窥探消费掉的字节还给 handler，让它读到的 body 与没有这层
// 中间件时完全一致；Close 仍然关闭原始 body，连接得以释放。
type replayedBody struct {
	io.Reader
	io.Closer
}

// sensitiveFieldRegex 匹配 JSON 中的敏感字段（不区分大小写，兼容 snake_case / camelCase / PascalCase）。
// $1 捕获原始字段名（包括两侧引号），保持日志中的字段名不变，仅将值替换为 "***"。
var sensitiveFieldRegex = regexp.MustCompile(
	`(?i)("(?:new[_-]?password|old[_-]?password|password|passwd|ticket|token|access[_-]?token|` +
		`refresh[_-]?token|next[_-]?token|device[_-]?token|pending[_-]?token|pairing[_-]?link|` +
		`id[_-]?token|authorization|auth[_-]?token|api[_-]?key|` +
		`api[_-]?secret|secret[_-]?key|client[_-]?secret|private[_-]?key|secret|` +
		`authorization[_-]?url|authorization[_-]?attempt|assertion)")\s*:\s*"[^"]*"`,
)

// sanitizeBody 清理敏感信息
func sanitizeBody(body string) string {
	return sensitiveFieldRegex.ReplaceAllString(body, `$1:"***"`)
}

var sensitiveQueryFields = map[string]struct{}{
	"access_token":          {},
	"authorization_attempt": {},
	"code":                  {},
	"id_token":              {},
	"refresh_token":         {},
	"state":                 {},
	// ticket is the sandbox terminal's WebSocket handshake credential. A
	// browser cannot set Authorization on an upgrade, so it travels in the
	// query string; anyone holding it for its 2-minute TTL can open a shell
	// in the session's sandbox, which is why it must never reach a log line.
	"ticket": {},
	"token":  {},
}

// sanitizeQuery prevents OAuth authorization codes, CSRF/attempt state, and
// handshake credentials from being copied into access logs. Parsing the query
// also covers repeated and percent-encoded parameters without relying on
// fragile string replacement.
func sanitizeQuery(raw string) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "[invalid query omitted]"
	}
	for key := range values {
		if _, sensitive := sensitiveQueryFields[strings.ToLower(key)]; sensitive {
			values[key] = []string{"***"}
		}
	}
	return values.Encode()
}

// readRequestBody 读取请求体（限制大小用于日志，但把剩余字节原样交还后续handler）
func readRequestBody(c *gin.Context) string {
	if c.Request.Body == nil {
		return ""
	}

	// 检查Content-Type，只记录JSON类型
	contentType := c.GetHeader("Content-Type")
	if !strings.Contains(contentType, "application/json") &&
		!strings.Contains(contentType, "application/x-www-form-urlencoded") &&
		!strings.Contains(contentType, "text/") {
		return "[非文本类型，已跳过]"
	}

	// 重置request body：把已窥探的前缀接回未读取的剩余部分，handler 仍能读到完整
	// 数据，但中间件自身只驻留日志需要的那点字节。
	//
	// 这里原本是无上限的 io.ReadAll(c.Request.Body)。Logger 注册在 Auth 之前
	// （router.go NewRouter），因此对所有请求执行，而任何路由的
	// http.MaxBytesReader（limitJSONBody / limitUploadBody / browserskill 的 4 KiB
	// 授权接口）都是在 handler 里才装上的：body 早已被整体读进内存，那些 cap 只剩
	// 报错的意义，没有限住内存的作用。没有 cap 的路由（对话、检索、IM webhook、
	// MCP 端点）则连上限都不存在。
	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodySize+1))
	originalBody := c.Request.Body
	c.Request.Body = &replayedBody{
		Reader: io.MultiReader(bytes.NewReader(bodyBytes), originalBody),
		Closer: originalBody,
	}
	if err != nil {
		return "[读取请求体失败]"
	}

	// 用于日志的body（限制大小）
	truncated := len(bodyBytes) > maxBodySize
	if truncated {
		bodyBytes = bodyBytes[:maxBodySize]
	}

	bodyStr := string(bodyBytes)
	if truncated {
		bodyStr += bodyTruncatedMarker
	}

	return sanitizeBody(bodyStr)
}

// RequestID middleware adds a unique request ID to the context
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
