package types

import "strings"

const (
	IMNoAnswerFallback  = "抱歉，我暂时无法回答这个问题。"
	IMErrorFallback     = "抱歉，处理您的问题时出现了异常，请稍后再试。"
	IMCancelledFallback = "抱歉，回答已被取消。"
)

// IsIMFallbackAnswer recognizes historical IM fallback rows written before
// they were explicitly marked IsFallback. Only exact system-owned replies
// qualify; arbitrary model text must not be classified by substring.
func IsIMFallbackAnswer(content string) bool {
	switch strings.TrimSpace(content) {
	case IMNoAnswerFallback, IMErrorFallback, IMCancelledFallback:
		return true
	default:
		return false
	}
}
