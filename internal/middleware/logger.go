package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
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
)

// loggerResponseBodyWriter 自定义ResponseWriter用于捕获响应内容（用于logger中间件）
type loggerResponseBodyWriter struct {
	gin.ResponseWriter
	body      *bytes.Buffer
	truncated bool
}

// Write 重写Write方法，同时写入buffer和原始writer
// 限制buffer大小，避免SSE等流式响应导致内存无限增长
func (r *loggerResponseBodyWriter) Write(b []byte) (int, error) {
	if r.body.Len() < maxBodySize {
		remaining := maxBodySize - r.body.Len()
		if len(b) <= remaining {
			r.body.Write(b)
		} else {
			r.body.Write(b[:remaining])
			r.truncated = true
		}
	} else if len(b) > 0 {
		r.truncated = true
	}
	return r.ResponseWriter.Write(b)
}

// isSensitiveLogFieldName normalizes snake_case, camelCase, and kebab-case
// names. Any field ending in "token" is redacted, covering preview_token,
// confirmation_token, invite_token, publishToken, and future signed
// confirmation tokens without requiring endpoint-specific additions.
func isSensitiveLogFieldName(field string) bool {
	var normalized strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(field)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			normalized.WriteRune(r)
		}
	}
	name := normalized.String()
	if strings.HasSuffix(name, "token") || strings.HasSuffix(name, "signature") || strings.HasSuffix(name, "nonce") ||
		strings.HasSuffix(name, "key") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "credential") || strings.Contains(name, "accesskey") {
		return true
	}
	switch name {
	case "ticket", "authorization", "apikey", "apisecret", "secretkey", "privatekey", "pairinglink", "authorizationurl", "authorizationattempt", "state", "code", "headers", "authheaders", "customheaders", "connectionconfig", "authconfig", "securityconfig":
		return true
	default:
		return false
	}
}

func redactJSONValueForLog(value interface{}, field string) interface{} {
	if field != "" && isSensitiveLogFieldName(field) {
		return "***"
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		redacted := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			redacted[key] = redactJSONValueForLog(child, key)
		}
		return redacted
	case []interface{}:
		redacted := make([]interface{}, len(typed))
		for index, child := range typed {
			redacted[index] = redactJSONValueForLog(child, "")
		}
		return redacted
	default:
		return value
	}
}

func sanitizeJSONBodyForLog(body []byte) string {
	var decoded interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "[invalid JSON body omitted]"
	}
	redacted, err := json.Marshal(redactJSONValueForLog(decoded, ""))
	if err != nil {
		return "[JSON body omitted]"
	}
	return string(redacted)
}

func sanitizeFormBodyForLog(body []byte) string {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return "[invalid form body omitted]"
	}
	for field := range values {
		if isSensitiveLogFieldName(field) {
			values[field] = []string{"***"}
		}
	}
	return values.Encode()
}

// sanitizeHTTPBodyForLog is the only request/response payload path used by the
// generic HTTP logger. It fails closed for malformed JSON and text bodies: a
// payload that cannot be structurally redacted is never written to logs.
func sanitizeHTTPBodyForLog(contentType string, body []byte) string {
	contentType = strings.ToLower(contentType)
	switch {
	case strings.Contains(contentType, "application/json"), strings.Contains(contentType, "+json"):
		return sanitizeJSONBodyForLog(body)
	case strings.Contains(contentType, "application/x-www-form-urlencoded"):
		return sanitizeFormBodyForLog(body)
	default:
		return "[body omitted]"
	}
}

// sanitizeBody remains a small test-facing wrapper for legacy direct callers.
// Production request/response logging must use sanitizeHTTPBodyForLog with the
// actual content type so malformed and text payloads fail closed.
func sanitizeBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return sanitizeJSONBodyForLog([]byte(body))
	}
	return sanitizeFormBodyForLog([]byte(body))
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
		if _, sensitive := sensitiveQueryFields[strings.ToLower(key)]; sensitive || isSensitiveLogFieldName(key) {
			values[key] = []string{"***"}
		}
	}
	return values.Encode()
}

// readRequestBody 读取请求体（限制大小用于日志，但完整读取用于重置）
func readRequestBody(c *gin.Context) string {
	if c.Request.Body == nil {
		return ""
	}

	// Read and restore only textual request bodies. Non-text uploads remain
	// untouched for handlers and are never buffered for logging.
	contentType := c.GetHeader("Content-Type")
	isJSONOrForm := strings.Contains(strings.ToLower(contentType), "application/json") ||
		strings.Contains(strings.ToLower(contentType), "+json") ||
		strings.Contains(strings.ToLower(contentType), "application/x-www-form-urlencoded")
	isText := strings.Contains(strings.ToLower(contentType), "text/")
	if !isJSONOrForm && !isText {
		return "[非文本类型，已跳过]"
	}

	// 完整读取body内容（不限制大小），因为需要完整重置给后续handler使用
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "[读取请求体失败]"
	}

	// 重置request body，使用完整内容，确保后续handler能读取到完整数据
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Redact before truncating. A sensitive value that starts immediately
	// before the logging limit used to be sliced into an invalid JSON fragment,
	// which prevented field-level redaction and leaked its prefix.
	sanitizedBody := sanitizeHTTPBodyForLog(contentType, bodyBytes)
	logBodyBytes := []byte(sanitizedBody)
	if len(logBodyBytes) > maxBodySize {
		logBodyBytes = logBodyBytes[:maxBodySize]
	}

	bodyStr := string(logBodyBytes)
	if len(sanitizedBody) > maxBodySize {
		bodyStr += "... [内容过长，已截断]"
	}

	return bodyStr
}

// RequestID middleware adds a unique request ID to the context
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get request ID from header or generate a new one
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		safeRequestID := secutils.SanitizeForLog(requestID)
		// Set request ID in header
		c.Header("X-Request-ID", requestID)

		// Set request ID in context
		c.Set(types.RequestIDContextKey.String(), requestID)

		// Set logger in context
		requestLogger := logger.GetLogger(c)
		requestLogger = requestLogger.WithField("request_id", safeRequestID)
		c.Set(types.LoggerContextKey.String(), requestLogger)

		// Set request ID in the global context for logging
		c.Request = c.Request.WithContext(
			context.WithValue(
				context.WithValue(c.Request.Context(), types.RequestIDContextKey, requestID),
				types.LoggerContextKey, requestLogger,
			),
		)

		c.Next()
	}
}

// Logger middleware logs request details with request ID, input and output
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		isWikiStats := strings.HasPrefix(path, "/api/v1/knowledgebase/") && strings.HasSuffix(path, "/wiki/stats")
		if strings.HasPrefix(path, "/assets/") || isWikiStats {
			c.Next()
			return
		}

		// Browser traffic contains credentials, page content and screenshots.
		// Keep access metadata, but never read or buffer these request/response bodies.
		browserTraffic := strings.HasPrefix(path, "/api/v1/local-browser/") ||
			path == "/api/v1/me/browser" || strings.HasSuffix(path, "/local-browser")
		// 读取请求体（在Next之前读取，因为Next会消费body）
		var requestBody string
		if !browserTraffic && (c.Request.Method == "POST" || c.Request.Method == "PUT" || c.Request.Method == "PATCH") {
			requestBody = readRequestBody(c)
		}

		// 创建响应体捕获器
		responseBody := &bytes.Buffer{}
		responseWriter := &loggerResponseBodyWriter{
			ResponseWriter: c.Writer,
			body:           responseBody,
		}
		if !browserTraffic {
			c.Writer = responseWriter
		}

		// Process request
		c.Next()

		// Get request ID from context
		requestID, exists := c.Get(types.RequestIDContextKey.String())
		requestIDStr := "unknown"
		if exists {
			if idStr, ok := requestID.(string); ok && idStr != "" {
				requestIDStr = idStr
			}
		}
		safeRequestID := secutils.SanitizeForLog(requestIDStr)

		// Calculate latency
		latency := time.Since(start)

		// Get client IP and status code
		clientIP := c.ClientIP()
		statusCode := c.Writer.Status()
		method := c.Request.Method

		if raw != "" {
			path = path + "?" + sanitizeQuery(raw)
		}

		// 读取响应体
		responseBodyStr := ""
		if responseBody.Len() > 0 {
			contentType := c.Writer.Header().Get("Content-Type")
			if strings.Contains(contentType, "text/event-stream") {
				responseBodyStr = "[SSE流式响应，已跳过]"
			} else if responseWriter.truncated {
				// The capture limit cut an unknown byte boundary. Do not attempt
				// a partial JSON/text sanitizer that could expose a secret prefix.
				responseBodyStr = "[响应内容过长，已省略]"
			} else {
				bodyBytes := responseBody.Bytes()
				responseBodyStr = sanitizeHTTPBodyForLog(contentType, bodyBytes)
			}
		}

		// 构建日志消息
		logMsg := logger.GetLogger(c)
		logMsg = logMsg.WithFields(map[string]interface{}{
			"request_id":  safeRequestID,
			"method":      method,
			"path":        secutils.SanitizeForLog(path),
			"status_code": statusCode,
			"size":        c.Writer.Size(),
			"latency":     latency.String(),
			"client_ip":   secutils.SanitizeForLog(clientIP),
		})

		// 添加请求体（如果有）
		if requestBody != "" {
			logMsg = logMsg.WithField("request_body", secutils.SanitizeForLog(requestBody))
		}

		// 添加响应体（如果有）
		if responseBodyStr != "" {
			logMsg = logMsg.WithField("response_body", secutils.SanitizeForLog(responseBodyStr))
		}
		if last := c.Errors.Last(); last != nil && last.Err != nil {
			logMsg = logMsg.WithField("error", secutils.SanitizeForLog(last.Err.Error()))
		}
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
