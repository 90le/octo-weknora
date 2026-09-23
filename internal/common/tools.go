package common

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/logger"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// ToInterfaceSlice converts a slice of strings to a slice of empty interfaces.
func ToInterfaceSlice[T any](slice []T) []interface{} {
	interfaceSlice := make([]interface{}, len(slice))
	for i, v := range slice {
		interfaceSlice[i] = v
	}
	return interfaceSlice
}

// []string -> string, " join, space separated
func StringSliceJoin(slice []string) string {
	result := make([]string, len(slice))
	for i, v := range slice {
		result[i] = `"` + v + `"`
	}
	return strings.Join(result, " ")
}

func GetAttrs[A, B any](extract func(A) B, attrs ...A) []B {
	result := make([]B, len(attrs))
	for i, attr := range attrs {
		result[i] = extract(attr)
	}
	return result
}

// Deduplicate removes duplicates from a slice based on a key function
// T: the type of elements in the slice
// K: the type of key used for deduplication
func Deduplicate[T any, K comparable](keyFunc func(T) K, items ...T) []T {
	seen := make(map[K]T)
	for _, item := range items {
		key := keyFunc(item)
		if _, exists := seen[key]; !exists {
			seen[key] = item
		}
	}
	return slices.Collect(maps.Values(seen))
}

// ScoreComparable is an interface for types that have a Score method returning float64
type ScoreComparable interface {
	GetScore() float64
}

// DeduplicateWithScore removes duplicates from a slice based on a key function,
// keeping the item with the highest score for each key, then sorts by score descending
// T: the type of elements in the slice (must implement ScoreComparable)
// K: the type of key used for deduplication
func DeduplicateWithScore[T ScoreComparable, K comparable](keyFunc func(T) K, items ...T) []T {
	seen := make(map[K]T)
	for _, item := range items {
		key := keyFunc(item)
		if existing, exists := seen[key]; !exists {
			seen[key] = item
		} else if item.GetScore() > existing.GetScore() {
			seen[key] = item
		}
	}
	result := slices.Collect(maps.Values(seen))
	// Sort by score descending
	slices.SortFunc(result, func(a, b T) int {
		scoreA := a.GetScore()
		scoreB := b.GetScore()
		if scoreA > scoreB {
			return -1
		} else if scoreA < scoreB {
			return 1
		}
		return 0
	})
	return result
}

// ParseLLMJsonResponse parses a JSON response from LLM, handling cases where JSON is wrapped in code blocks.
// This is useful when LLMs return responses like:
// ```json
// {"key": "value"}
// ```
// or regular JSON responses directly.
// jsonCodeFenceRE extracts a JSON payload wrapped in a Markdown code fence.
// Compiled once: ParseLLMJsonResponse runs on the graph-extraction path.
var jsonCodeFenceRE = regexp.MustCompile("```(?:json)?\\s*([\\s\\S]*?)```")

func ParseLLMJsonResponse(content string, target interface{}) error {
	// First, try to parse directly as JSON
	err := json.Unmarshal([]byte(content), target)
	if err == nil {
		return nil
	}

	// If direct parsing fails, try to extract JSON from code blocks
	matches := jsonCodeFenceRE.FindStringSubmatch(content)
	if len(matches) >= 2 {
		// Extract the JSON content within the code block
		jsonContent := strings.TrimSpace(matches[1])
		if fenceErr := json.Unmarshal([]byte(jsonContent), target); fenceErr == nil {
			return nil
		}
	}

	// Last resort: models often wrap the payload in prose ("Sure, here is
	// the JSON: {...}"). Scan for a balanced object/array so trailing
	// commentary — including bracket-like text such as "[1]" — cannot
	// truncate the payload.
	if extracted := ExtractBalancedJSON(content); extracted != "" {
		if scanErr := json.Unmarshal([]byte(extracted), target); scanErr == nil {
			return nil
		}
	}

	// Report the direct-parse failure, which is the most descriptive one.
	return err
}

// ExtractBalancedJSON returns the first balanced JSON object or array embedded
// in s, or an empty string when there is none. Whichever bracket type opens
// first wins, and quoted strings are skipped so braces inside string literals
// do not unbalance the scan. The result is not validated as JSON; callers must
// still unmarshal it.
func ExtractBalancedJSON(s string) string {
	objStart := strings.IndexByte(s, '{')
	arrStart := strings.IndexByte(s, '[')
	var open, closeCh byte
	var start int
	switch {
	case objStart < 0 && arrStart < 0:
		return ""
	case objStart < 0:
		open, closeCh, start = '[', ']', arrStart
	case arrStart < 0:
		open, closeCh, start = '{', '}', objStart
	case objStart < arrStart:
		open, closeCh, start = '{', '}', objStart
	default:
		open, closeCh, start = '[', ']', arrStart
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[start : i+1])
			}
		}
	}
	return ""
}

// CleanInvalidUTF8 移除字符串中的非法 UTF-8 字符和 \x00
func CleanInvalidUTF8(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// 非法 UTF-8 字节，跳过
			i++
			continue
		}
		if r == 0 {
			// NULL 字符 \x00，跳过
			i += size
			continue
		}
		b.WriteRune(r)
		i += size
	}

	return b.String()
}

const (
	pipelineLogValueMaxRune = 300
	defaultPipelineStage    = "PIPELINE"
	defaultPipelineAction   = "info"
	pipelineLogPrefix       = "[PIPELINE]"
	pipelineTruncateEll     = "..."
)

// Pipeline fields can contain the user's message, quoted context, retrieved
// passages, tool arguments and provider errors. Routine logs only need stable
// identifiers and measurements; the full values remain in the request/trace
// where their access controls apply. Keep this an allowlist so a newly added
// string field cannot accidentally make private content part of server logs.
var pipelineLogIdentifier = regexp.MustCompile(`^[A-Za-z0-9_./:@-]{1,128}$`)

func safePipelineStringField(key string) bool {
	switch key {
	case "session_id", "message_id", "user_message_id",
		"kb_id", "knowledge_base_id", "knowledge_id", "chunk_id",
		"chat_model", "model_id", "rerank_model", "rerank_model_id",
		"vlm_model_id", "query_understand_model_id", "provider_id",
		"finish_reason", "status", "error_code", "target_type":
		return true
	default:
		return false
	}
}

// PipelineLog builds a structured pipeline log string.
func PipelineLog(stage, action string, fields map[string]interface{}) string {
	if stage == "" {
		stage = defaultPipelineStage
	}
	if action == "" {
		action = defaultPipelineAction
	}

	builder := strings.Builder{}
	builder.Grow(128)
	builder.WriteString(pipelineLogPrefix)
	builder.WriteString(" stage=")
	builder.WriteString(stage)
	builder.WriteString(" action=")
	builder.WriteString(action)

	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			builder.WriteString(" ")
			builder.WriteString(key)
			builder.WriteString("=")
			builder.WriteString(secutils.SanitizeForLog(formatPipelineLogValue(key, fields[key])))
		}
	}
	return builder.String()
}

// PipelineInfo logs pipeline info level entries.
func PipelineInfo(ctx context.Context, stage, action string, fields map[string]interface{}) {
	logger.GetLogger(ctx).Info(PipelineLog(stage, action, fields))
}

// PipelineWarn logs pipeline warning level entries.
func PipelineWarn(ctx context.Context, stage, action string, fields map[string]interface{}) {
	logger.GetLogger(ctx).Warn(PipelineLog(stage, action, fields))
}

// PipelineError logs pipeline error level entries.
func PipelineError(ctx context.Context, stage, action string, fields map[string]interface{}) {
	logger.GetLogger(ctx).Error(PipelineLog(stage, action, fields))
}

func formatPipelineLogValue(key string, value interface{}) string {
	if key == "request_id" {
		if requestID, ok := value.(string); ok {
			return strconv.Quote(secutils.HashRequestIDForLog(requestID))
		}
		return strconv.Quote("[redacted]")
	}
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return "nil"
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "nil"
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.String:
		content := v.String()
		if safePipelineStringField(key) && pipelineLogIdentifier.MatchString(content) {
			return strconv.Quote(content)
		}
		return strconv.Quote(fmt.Sprintf("[redacted len=%d]", utf8.RuneCountInString(content)))
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return fmt.Sprint(v.Interface())
	case reflect.Slice, reflect.Array, reflect.Map:
		return strconv.Quote(fmt.Sprintf("[redacted count=%d]", v.Len()))
	default:
		return strconv.Quote("[redacted]")
	}
}

func truncatePipelineValue(content string) string {
	content = strings.ReplaceAll(content, "\n", "\\n")
	runes := []rune(content)
	if len(runes) <= pipelineLogValueMaxRune {
		return content
	}
	return string(runes[:pipelineLogValueMaxRune]) + pipelineTruncateEll
}

func TruncateForLog(content string) string {
	return truncatePipelineValue(content)
}
