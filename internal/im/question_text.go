package im

import "strings"

// OctoQuestionTextKey is populated by the authenticated Octo adapter after
// validating native mention spans. Content remains available for commands and
// the original platform message remains available for reply quotations.
const OctoQuestionTextKey = "octo_question_text"

func imQuestionText(msg *IncomingMessage) string {
	if msg == nil {
		return ""
	}
	text, ok := msg.Extra[OctoQuestionTextKey]
	if msg.Platform != "octo" || msg.Extra["octo_addressed"] != "true" || !ok {
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
