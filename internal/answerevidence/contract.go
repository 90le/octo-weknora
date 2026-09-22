// Package answerevidence owns the turn-local contract that decides which
// evidence an answer needs before it may make a conclusion. It deliberately
// contains no transport, model, or persistence dependency: callers classify a
// user turn once, record only trusted tool outcomes, and consult the same
// state before streaming or finalising an answer.
package answerevidence

import (
	"context"
	"strings"
	"sync"
	"unicode"
)

// Intent identifies the evidence boundary for a user request.
type Intent string

const (
	IntentNone        Intent = ""
	IntentSource      Intent = "source"
	IntentRelease     Intent = "release"
	IntentIntegration Intent = "integration"
)

// State is request-local mutable evidence state. It is placed in Context only
// for the duration of one incoming turn and is never persisted or exposed to a
// model. Tool execution may be parallel, hence the mutex.
type State struct {
	mu               sync.RWMutex
	intent           Intent
	chinese          bool
	sourceRead       bool
	documentEvidence bool
	releaseEvidence  bool
	nudgeCount       int
}

type stateKey struct{}

// Classify uses deliberately narrow cues. A source classification has
// precedence because a version question that explicitly asks about code still
// needs a source read. Latest-version, release, and changelog questions are
// distinct from support/integration questions: the former need release
// provenance, while the latter need actual documentation or source evidence.
// Broad product words alone do not turn a request into a source-code claim.
func Classify(query string) Intent {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return IntentNone
	}
	if containsAny(q,
		"源码", "源代码", "代码", "函数", "接口实现", "具体实现", "实现细节", "调用链", "字段", "变量",
		"类定义", "类名", "方法名", "方法实现", "调用方法",
		"错误码", "堆栈", "栈追踪", "哪个文件", "文件里", "文件中", "行号", "仓库里面", "仓库中", "仓库内",
		"source code", "function", "method", "class ", "implementation", "call stack", "stack trace",
		"error code", "repository", ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".java", ".php", ".rs", ".swift", ".kt", ".yaml", ".yml",
	) {
		return IntentSource
	}
	if containsAny(q,
		"最新版本", "当前版本", "版本", "更新日志", "更新了什么", "更新内容", "发布", "发行", "release", "changelog", "release note",
	) {
		return IntentRelease
	}
	if containsAny(q,
		"是否支持", "支持", "接入", "集成", "兼容", "对接", "原生接入", "channel-octo", "octo-channel", "github.com/",
		"integration", "integrate", "compatible", "support",
	) {
		return IntentIntegration
	}
	return IntentNone
}

func containsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// WithContract classifies one incoming turn and starts its evidence ledger.
// Returning the original context for an unrelated question avoids imposing
// product-specific policy on general agent work.
func WithContract(ctx context.Context, query string) context.Context {
	intent := Classify(query)
	if intent == IntentNone {
		return ctx
	}
	return context.WithValue(ctx, stateKey{}, &State{intent: intent, chinese: containsHan(query)})
}

func stateFrom(ctx context.Context) *State {
	state, _ := ctx.Value(stateKey{}).(*State)
	return state
}

func IntentFromContext(ctx context.Context) Intent {
	if state := stateFrom(ctx); state != nil {
		state.mu.RLock()
		defer state.mu.RUnlock()
		return state.intent
	}
	return IntentNone
}

func SourceReadObserved(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.sourceRead
}

func VerifiedEvidenceObserved(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.sourceRead || state.documentEvidence || state.releaseEvidence
}

// ReleaseEvidenceObserved reports whether a trusted release-lookup result
// supplied private release provenance during this turn. It must not be used to
// establish support or integration: a release tag alone says nothing about an
// integration's existence or behaviour.
func ReleaseEvidenceObserved(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.releaseEvidence
}

// DocumentOrSourceEvidenceObserved is the integration contract's evidence
// gate. It intentionally excludes release provenance: both positive and
// negative support claims need actual documentation or source content.
func DocumentOrSourceEvidenceObserved(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.sourceRead || state.documentEvidence
}

// RecordSourceRead must be called only after trusted server code has verified
// a successful source_browse read and its private provenance marker.
func RecordSourceRead(ctx context.Context) {
	if state := stateFrom(ctx); state != nil {
		state.mu.Lock()
		state.sourceRead = true
		state.mu.Unlock()
	}
}

// RecordDocumentEvidence marks a successful document/Wiki/web reader result
// with actual content. A plain successful RAG request with zero hits must not
// call this method.
func RecordDocumentEvidence(ctx context.Context) {
	if state := stateFrom(ctx); state != nil {
		state.mu.Lock()
		state.documentEvidence = true
		state.mu.Unlock()
	}
}

// RecordReleaseEvidence records trusted, private provenance from an official
// release lookup. The future github_release_lookup tool is expected to call
// this only after server code has validated its provenance marker. It is a
// separate record so RAG/source results cannot accidentally establish a
// "latest release" conclusion.
func RecordReleaseEvidence(ctx context.Context) {
	if state := stateFrom(ctx); state != nil {
		state.mu.Lock()
		state.releaseEvidence = true
		state.mu.Unlock()
	}
}

// ShouldHoldStreamingAnswer prevents an unverified natural answer from being
// optimistically exposed in a stream. A safe explicit unknown is later
// released as normal; only version or integration conclusions are retried or
// replaced when their intent-specific evidence is missing.
func ShouldHoldStreamingAnswer(ctx context.Context) bool {
	switch IntentFromContext(ctx) {
	case IntentSource:
		return !SourceReadObserved(ctx)
	case IntentRelease:
		return !ReleaseEvidenceObserved(ctx)
	case IntentIntegration:
		return !DocumentOrSourceEvidenceObserved(ctx)
	default:
		return false
	}
}

// CanRetryEvidence grants exactly one explicit evidence retry. It prevents an
// LLM that ignores the initial system-owned instruction from burning every
// agent iteration while preserving one chance to search and read.
func CanRetryEvidence(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.nudgeCount >= 1 {
		return false
	}
	state.nudgeCount++
	return true
}

func isChinese(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.chinese
}

// NeedsEvidenceRetry reports whether a natural final answer is prohibited by
// the active contract. Source facts always require a source read. Release and
// integration turns allow only an explicit uncertainty reply without their
// respective evidence; any other final content could be an affirmative or
// negative conclusion hidden behind short wording such as "yes" or a tag.
func NeedsEvidenceRetry(ctx context.Context, answer string) bool {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return false
	}
	switch IntentFromContext(ctx) {
	case IntentSource:
		return !SourceReadObserved(ctx)
	case IntentRelease:
		return !ReleaseEvidenceObserved(ctx) && !isExplicitReleaseUnknown(answer)
	case IntentIntegration:
		return !DocumentOrSourceEvidenceObserved(ctx) && !isExplicitIntegrationUnknown(answer)
	default:
		return false
	}
}

// NeedsSynthesisFallback prevents max-iteration synthesis from inventing a
// conclusion after the run has already failed to obtain the required evidence.
func NeedsSynthesisFallback(ctx context.Context) bool {
	switch IntentFromContext(ctx) {
	case IntentSource:
		return !SourceReadObserved(ctx)
	case IntentRelease:
		return !ReleaseEvidenceObserved(ctx)
	case IntentIntegration:
		return !DocumentOrSourceEvidenceObserved(ctx)
	default:
		return false
	}
}

// AllowsMissingIssue is the persistence-side gate for an automatic “missing
// knowledge” record. The normal retrieval trace proves that a lookup actually
// ran; this method adds the stronger contract-specific proof so a source
// search without an actual read cannot be mislabeled as a knowledge gap.
func AllowsMissingIssue(ctx context.Context) bool {
	switch IntentFromContext(ctx) {
	case IntentSource:
		return SourceReadObserved(ctx)
	case IntentRelease:
		return ReleaseEvidenceObserved(ctx)
	case IntentIntegration:
		return DocumentOrSourceEvidenceObserved(ctx)
	default:
		return true
	}
}

// ClaimsUnsupported detects only categorical negative assertions. It is not a
// generic fact checker: wording such as “I cannot confirm” remains an allowed
// unknown answer. The conservative list keeps false positives low.
func ClaimsUnsupported(answer string) bool {
	lower := strings.ToLower(answer)
	return containsAny(lower,
		"不支持", "未支持", "没有支持", "无法接入", "不能接入", "不存在该接入",
		"not supported", "does not support", "unsupported", "cannot integrate", "can't integrate", "no integration",
	)
}

// isExplicitReleaseUnknown identifies the narrow safe reply allowed when a
// latest-version lookup found no trusted release provenance. Any other final
// reply is retried, because even a bare tag or “no release” is a release fact.
func isExplicitReleaseUnknown(answer string) bool {
	lower := strings.ToLower(strings.TrimSpace(answer))
	return lower != "" && containsAny(lower,
		"无法确认最新", "无法核验最新", "不能确认最新", "无法确认当前版本", "当前材料无法确认", "当前资料无法确认", "当前授权资料无法确认",
		"没有取得可核验的发布", "没有可核验的发布", "没有发布证据",
		"cannot confirm the latest", "cannot verify the latest", "unable to verify the latest", "cannot confirm the current version", "cannot verify the current version", "current material cannot confirm", "no verifiable release evidence",
	)
}

// isExplicitIntegrationUnknown identifies the narrow safe reply allowed when
// no documentation/source body was read. Any other answer could assert either
// support or non-support, including a terse “yes” or “no”.
func isExplicitIntegrationUnknown(answer string) bool {
	lower := strings.ToLower(strings.TrimSpace(answer))
	return lower != "" && containsAny(lower,
		"当前材料无法确认", "当前资料无法确认", "当前授权资料无法确认", "无法确认是否支持", "无法核验是否支持", "不能确认是否支持",
		"没有读取到可核验", "未读到可核验",
		"cannot confirm support", "cannot verify support", "unable to confirm whether", "unable to verify whether",
	)
}

// Prompt is system-owned guidance added only for classified Octo turns. The
// source hard gate below is still authoritative; this prompt helps the model
// choose the right tool before the gate needs to intervene.
func Prompt(ctx context.Context) string {
	switch IntentFromContext(ctx) {
	case IntentSource:
		if isChinese(ctx) {
			return `<answer_evidence_contract>
本回合属于源码事实问题。对函数、实现、配置、错误、文件、版本行为等结论，必须先用 source_browse 搜索，再 read 实际文件和行段。search/list 的摘要、RAG 文档、历史对话和文件名都不能替代 read 证据。若无法读到源码，明确说明“当前没有可核验的源码证据”，不要断言实现细节。
</answer_evidence_contract>`
		}
		return `<answer_evidence_contract>
This is a source-code fact question. Before asserting functions, implementation, configuration, errors, files, or version behaviour, use source_browse to search and then read the actual file and lines. Search/list summaries, RAG documents, prior conversation, and filenames never replace a read. If a source read is unavailable, say that source evidence was not obtained; do not assert implementation details.
</answer_evidence_contract>`
	case IntentRelease:
		if isChinese(ctx) {
			return `<answer_evidence_contract>
本回合询问最新版本、发布或更新日志。只有经过可信发布查询并附带发布来源记录的结果，才能断言“最新”、当前版本、发布日期或完整更新内容。README、RAG 文档、源码快照和历史对话可补充背景，但不能证明它们代表最新发布；当前没有可核验发布记录时，只能说明无法确认。
</answer_evidence_contract>`
		}
		return `<answer_evidence_contract>
This turn asks about a latest version, release, or changelog. Only a trusted release lookup with recorded release provenance may establish "latest", the current version, release date, or complete changes. README/RAG/source snapshots and prior conversation may provide background but cannot prove they represent the latest release; without a verifiable release record, say it cannot be confirmed.
</answer_evidence_contract>`
	case IntentIntegration:
		if isChinese(ctx) {
			return `<answer_evidence_contract>
本回合询问支持、接入、集成、兼容性或某个 channel 项目的职责。无论结论是“支持”还是“不支持”，或对项目用途的说明，都必须先读取当前授权范围内的 README、接口文档或实际源码；发布标签、RAG 未命中、文档缺失和文件名都不能单独证明结论。若未读到证据，只能说明当前材料无法确认。
</answer_evidence_contract>`
		}
		return `<answer_evidence_contract>
This turn asks about support, integration, compatibility, or a channel project's responsibility. Both affirmative and negative conclusions, and a description of a project's role, require an actual read of authorized README/API documentation or source. A release tag, RAG miss, missing document, or filename alone cannot establish the conclusion. If no content was read, say the current material cannot confirm it.
</answer_evidence_contract>`
	default:
		return ""
	}
}

func RetryNudge(ctx context.Context) string {
	switch IntentFromContext(ctx) {
	case IntentSource:
		if isChinese(ctx) {
			return "在给出源码结论前，先使用 source_browse 查找并 read 实际文件和行段。不要重复未核验的结论；若无法获得 read 证据，请明确说明证据不足。"
		}
		return "Before giving a source-code conclusion, use source_browse to find and read the actual file and lines. Do not repeat an unverified conclusion; if a read is unavailable, state that evidence is insufficient."
	case IntentRelease:
		if isChinese(ctx) {
			return "不要把 README、RAG、源码快照或资料未命中当成“最新发布”的证据。请查找可核验的发布记录；若仍无发布证据，只能说明当前材料无法确认。"
		}
		return "Do not treat README/RAG/source snapshots or missing material as proof of the latest release. Look for a verifiable release record; if none exists, state that the current material cannot confirm it."
	case IntentIntegration:
		if isChinese(ctx) {
			return "不要把发布标签、RAG 未命中或资料缺失当成“支持”或“不支持”。请先读取可核验的 README、接口文档或源码；若仍无证据，只能说明当前材料无法确认。"
		}
		return "Do not treat a release tag, RAG miss, or missing material as proof of support or non-support. First read verifiable README/API documentation or source; if none exists, state that the current material cannot confirm it."
	default:
		return ""
	}
}

func FallbackReply(ctx context.Context) string {
	switch IntentFromContext(ctx) {
	case IntentSource:
		if isChinese(ctx) {
			return "我没有读取到可核验的源码内容，因此不能确认实现、函数、配置或版本行为。请提供具体仓库、分支、路径、函数名或错误文本，或确认允许访问对应源码。"
		}
		return "I did not obtain a verifiable source read, so I cannot confirm implementation, functions, configuration, or version behaviour. Please provide a repository, branch, path, function name, or error text, or confirm access to the relevant source."
	case IntentRelease:
		if isChinese(ctx) {
			return "当前没有取得可核验的发布记录，不能确认最新版本、发布日期或完整更新内容。请提供发布页、版本标签或允许查询对应发布来源后再确认。"
		}
		return "No verifiable release record was obtained, so I cannot confirm the latest version, release date, or complete changelog. Please provide a release page, tag, or access to the relevant release source."
	case IntentIntegration:
		if isChinese(ctx) {
			return "当前没有读取到可核验的 README、接口文档或源码，因此不能确认该能力支持或不支持接入。请提供仓库、路径、文档范围或允许访问对应资料后再确认。"
		}
		return "I did not read verifiable README/API documentation or source, so I cannot confirm whether the capability supports integration. Please provide a repository, path, document scope, or access to the relevant material."
	default:
		return ""
	}
}
