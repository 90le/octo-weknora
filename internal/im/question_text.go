package im

import (
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// OctoQuestionTextKey is populated by the authenticated Octo adapter after
// validating native mention spans. Content remains available for commands and
// the original platform message remains available for reply quotations.
const OctoQuestionTextKey = types.OctoQuestionTextKey

func imQuestionText(msg *IncomingMessage) string {
	if msg == nil {
		return ""
	}
	text, ok := types.OctoAddressedQuestion(msg.Extra)
	if msg.Platform != "octo" || !ok {
		return msg.Content
	}
	if strings.TrimSpace(text) == "" && (msg.MessageType == MessageTypeFile || msg.MessageType == MessageTypeImage) {
		return fileOnlyQAContent(msg.FileName)
	}
	n := 0
	for i := range text {
		if n == maxContentLength {
			return text[:i]
		}
		n++
	}
	return text
}
