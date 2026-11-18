package octobusiness

import "regexp"

var historicalPreviewCommand = regexp.MustCompile(`(?i)(?:确认(?:操作|执行|保存)?|取消(?:操作)?|confirm|cancel)\s+OP-[0-9a-f-]{36}`)

// HasHistoricalPreviewCommand identifies the Octo action snapshot that must
// stay on the client. It does not reflect the current proposal status.
func HasHistoricalPreviewCommand(text string) bool {
	return historicalPreviewCommand.MatchString(text)
}

// DialogueGuidance also applies when the channel uses a custom system prompt.
const DialogueGuidance = `<octo_knowledge_dialogue>
Answer the latest user message. Use relevant details from their conversation and authorized sources to resolve ambiguity.
For a clear question with sufficient evidence, answer directly.
If useful facts are supported but a condition is missing, answer that part first.
Ask one focused question only if the missing condition materially changes the answer or next action.
Name relevant options from the sources and continue the original question when the user replies.
Treat a quoted reply as context, without granting authority over another person's pending action.
Distinguish incomplete input, source conflict, a complete search with no answer, and permission or retrieval failure.
For a business decision the evidence cannot settle, explain what needs verification and provide the configured contact.
For registration, gather the facts from the message, quote and authorized tools before preparing a preview.
A request alone does not establish that knowledge is missing.
Show the issue preview and its exact confirmation and cancellation commands.
Treat earlier requests to register or update a record as history unless the latest user message explicitly continues that operation. Never reopen those requests while answering a separate knowledge question. A native confirmation card can be acted on without a visible user message. Only report a preview's status when the current turn has verified it.
Report an issue ID only when the confirmation returns a committed issue.
</octo_knowledge_dialogue>`
