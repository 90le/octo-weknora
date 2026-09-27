// Package answerevidence owns the turn-local contract that decides which
// evidence an answer needs before it may make a conclusion. It deliberately
// contains no transport, model, or persistence dependency: callers classify a
// user turn once, record only trusted tool outcomes, and consult the same
// state before streaming or finalising an answer.
package answerevidence

import (
	"context"
	"sort"
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

// evidenceNeeds is a set of independent proof obligations. Intent remains the
// legacy primary route, but a question can require more than one kind of
// evidence (for example the latest release *and* its source implementation).
type evidenceNeeds uint8

const (
	needSource evidenceNeeds = 1 << iota
	needRelease
	needIntegration
)

func (needs evidenceNeeds) has(intent Intent) bool {
	switch intent {
	case IntentSource:
		return needs&needSource != 0
	case IntentRelease:
		return needs&needRelease != 0
	case IntentIntegration:
		return needs&needIntegration != 0
	default:
		return false
	}
}

func (needs evidenceNeeds) multiple() bool { return needs != 0 && needs&(needs-1) != 0 }

// State is request-local mutable evidence state. It is placed in Context only
// for the duration of one incoming turn and is never persisted or exposed to a
// model. Tool execution may be parallel, hence the mutex.
type State struct {
	mu                     sync.RWMutex
	intent                 Intent
	needs                  evidenceNeeds
	chinese                bool
	sourceSearch           bool
	sourceComplete         bool
	sourceMatched          bool
	sourceSearchIdentities map[string]bool
	sourceRead             bool
	sourceReadRepos        map[string]bool
	// Full normalized identities prevent a search of one owner's same-named
	// repository from pairing with another owner's read or release evidence.
	sourceReadIdentities map[string]bool
	documentEvidence     bool
	releaseLookup        bool
	releaseEvidence      bool
	releaseIdentities    map[string]bool
	requiredRepos        []string
	// postPreflightSourceBrowse is deliberately separate from the normal
	// source-evidence ledger. It is enabled only after the system has already
	// searched and read every explicitly named channel repository. At that
	// point a model may still need a few focused follow-up reads, but repeated
	// repository-wide searches add latency and drown the answer context without
	// improving the proof requirement.
	postPreflightSourceBrowseActive    bool
	postPreflightSourceBrowseRemaining int
	nudgeCount                         int
}

type stateKey struct{}
type classifiedNoneKey struct{}

// Classify uses deliberately narrow cues. A source classification has
// precedence because a version question that explicitly asks about code still
// needs a source read. Latest-version, release, and changelog questions are
// distinct from support/integration questions: the former need release
// provenance, while the latter need actual documentation or source evidence.
// Broad product words alone do not turn a request into a source-code claim.
func classifyNeeds(query string) evidenceNeeds {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0
	}
	var needs evidenceNeeds
	if containsAny(q,
		"源码", "源代码", "代码", "函数", "接口实现", "具体实现", "实现细节", "调用链", "字段", "变量",
		"类定义", "类名", "方法名", "方法实现", "调用方法",
		"错误码", "堆栈", "栈追踪", "哪个文件", "文件里", "文件中", "行号", "仓库里面", "仓库中", "仓库内",
		"source code", "function", "method", "class ", "implementation", "call stack", "stack trace",
		"error code", "repository",
	) {
		// A file suffix alone is not a source-code request: Node.js, Vue.js
		// and similar product names previously forced ordinary knowledge-base
		// questions into the source-read fallback. Explicit file/code wording
		// above keeps source questions protected without that false positive.
		needs |= needSource
	}
	if containsAny(q,
		"最新版本", "最新版", "当前版本", "版本号", "版本是什么", "版本是多少", "哪个版本", "版本更新", "更新日志", "更新了什么", "更新内容", "发布版本", "版本发布", "发布记录", "发布说明", "发布日志", "发布了什么", "发行版本", "release", "changelog", "release note",
	) {
		needs |= needRelease
	}
	if containsAny(q,
		"是否支持", "支持", "接入", "集成", "兼容", "对接", "原生接入", "channel-octo", "octo-channel",
		"integration", "integrate", "compatible", "support",
	) || (needs == 0 && strings.Contains(q, "github.com/")) {
		// A bare GitHub URL can identify an integration project, but it must
		// not turn a release URL or named source-file question into an extra
		// support claim the user did not request.
		needs |= needIntegration
	}
	return needs
}

// Classify preserves the primary route for callers that display one intent.
// The turn-local contract uses the full set returned by classifyNeeds instead.
func Classify(query string) Intent {
	needs := classifyNeeds(query)
	switch {
	case needs.has(IntentSource):
		return IntentSource
	case needs.has(IntentRelease):
		return IntentRelease
	case needs.has(IntentIntegration):
		return IntentIntegration
	default:
		return IntentNone
	}
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

// namedChannelRepositories extracts only explicit *-channel-octo repository
// names. Generic product words are intentionally ignored: this guard exists to
// prevent a read of one named channel from being generalized to another named
// channel, not to guess repository ownership from ordinary prose.
func namedChannelRepositories(query string) []string {
	tokens := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '/')
	})
	seen := make(map[string]bool)
	for _, token := range tokens {
		if slash := strings.LastIndex(token, "/"); slash >= 0 {
			token = token[slash+1:]
		}
		token = strings.Trim(token, "-_.")
		if !strings.HasSuffix(token, "-channel-octo") || token == "-channel-octo" {
			continue
		}
		seen[token] = true
	}
	out := make([]string, 0, len(seen))
	for repository := range seen {
		out = append(out, repository)
	}
	sort.Strings(out)
	return out
}

func repositoryLeaf(repository string) string {
	repository = strings.ToLower(strings.TrimSpace(repository))
	if slash := strings.LastIndex(repository, "/"); slash >= 0 {
		repository = repository[slash+1:]
	}
	return strings.Trim(repository, "-_.")
}

func repositoryIdentity(repository string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(repository), "/"))
}

// WithContract classifies one incoming turn and starts its evidence ledger.
// It is idempotent: an ingress such as Octo IM may establish the contract
// after normalizing an addressed message, while AgentEngine.Execute establishes
// the same contract for every other transport. Replacing the existing state
// would lose trusted evidence already recorded by the ingress and could make
// the same turn use two different classifications.
// IntentNone is still a classified turn. Keep a separate marker so later
// model-only context (quoted messages, GROUP.md, attachment bodies) cannot
// reclassify the user's ordinary question. It does not create an evidence
// ledger, preserving ordinary agent behavior outside this policy.
func WithContract(ctx context.Context, query string) context.Context {
	if stateFrom(ctx) != nil || ctx.Value(classifiedNoneKey{}) != nil {
		return ctx
	}
	needs := classifyNeeds(query)
	intent := Classify(query)
	if intent == IntentNone {
		return context.WithValue(ctx, classifiedNoneKey{}, true)
	}
	return context.WithValue(ctx, stateKey{}, &State{
		intent:        intent,
		needs:         needs,
		chinese:       containsHan(query),
		requiredRepos: namedChannelRepositories(query),
	})
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

// Requires reads the original user's locked, turn-local proof obligations.
// Later quoted messages, GROUP.md content and retrieved data cannot add one.
func Requires(ctx context.Context, intent Intent) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.needs.has(intent)
}

func multipleNeeds(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.needs.multiple()
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

// SourceSearchObserved records that source_browse itself searched authorized
// snapshots. Listing names, RAG retrieval, and a successful tool invocation
// without the private search audit marker do not count.
func SourceSearchObserved(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.sourceSearch
}

// SourceSearchMatched reports whether any completed source_browse search
// returned a candidate. It is navigation state only, never content evidence.
func SourceSearchMatched(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.sourceMatched
}

// SourceSearchComplete reports whether a trusted source_browse search finished
// across the entire authorized catalog. A complete search of one repository,
// or a capped global page, cannot prove that all sources lack an answer.
func SourceSearchComplete(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.sourceComplete
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

// ReleaseLookupObserved reports that the dedicated official latest-release
// endpoint completed during this turn, even when that endpoint reported no
// stable release. It is not release evidence by itself.
func ReleaseLookupObserved(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.releaseLookup
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

// RequiredRepositories returns the named channel repositories whose roles are
// explicitly requested by the user. These names are display-safe repository
// leaves, not database identities.
func RequiredRepositories(ctx context.Context) []string {
	state := stateFrom(ctx)
	if state == nil {
		return nil
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return append([]string(nil), state.requiredRepos...)
}

// ActivatePostPreflightSourceBrowseBudget enables a small model-call budget
// only after a caller has independently established complete evidence for the
// explicitly named repositories. The caller decides when that condition is
// true; this package only owns the request-local, concurrency-safe counter.
// A zero or negative budget deliberately leaves normal source browsing alone.
func ActivatePostPreflightSourceBrowseBudget(ctx context.Context, budget int) {
	if budget <= 0 {
		return
	}
	if state := stateFrom(ctx); state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.postPreflightSourceBrowseActive {
			return
		}
		state.postPreflightSourceBrowseActive = true
		state.postPreflightSourceBrowseRemaining = budget
	}
}

// ConsumePostPreflightSourceBrowseBudget reserves one post-preflight
// source_browse call. It returns true while no post-preflight control is
// active, so ordinary source-code questions and incomplete preflights retain
// their existing access. Callers must invoke it immediately before executing
// the model-originated tool call; system-owned preflight calls bypass it.
func ConsumePostPreflightSourceBrowseBudget(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.postPreflightSourceBrowseActive {
		return true
	}
	if state.postPreflightSourceBrowseRemaining <= 0 {
		return false
	}
	state.postPreflightSourceBrowseRemaining--
	return true
}

// PostPreflightSourceBrowseBudget reports the active counter for diagnostics
// and tests. It never exposes tool inputs, repository IDs, or evidence text.
func PostPreflightSourceBrowseBudget(ctx context.Context) (active bool, remaining int) {
	state := stateFrom(ctx)
	if state == nil {
		return false, 0
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.postPreflightSourceBrowseActive, state.postPreflightSourceBrowseRemaining
}

// MissingRequiredRepositories reports which explicitly named channel projects
// still lack their own source-file read. It prevents the answerer from reading
// one adapter README and extrapolating implementation details to its peers.
func MissingRequiredRepositories(ctx context.Context) []string {
	state := stateFrom(ctx)
	if state == nil {
		return nil
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	missing := make([]string, 0)
	for _, repository := range state.requiredRepos {
		if !state.sourceReadRepos[repository] {
			missing = append(missing, repository)
		}
	}
	return missing
}

// IntegrationEvidenceObserved preserves ordinary knowledge-base behaviour for
// generic support questions: a real RAG/Wiki/document body remains usable
// evidence. When the user explicitly names channel repositories, each project
// needs its own scoped search and verified source-file read. Search completeness
// is needed for an absence claim, not for using a file that was actually read.
func IntegrationEvidenceObserved(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	if len(state.requiredRepos) == 0 {
		return state.sourceRead || state.documentEvidence
	}
	if !state.sourceSearch || !state.sourceRead {
		return false
	}
	for _, repository := range state.requiredRepos {
		if !namedRepositoryReadAfterSearchLocked(state, repository) {
			return false
		}
	}
	return true
}

// RecordSourceSearch records only a successful source_browse search. Matched
// remains navigation state; no source fact, absence claim, or missing-issue
// workflow is unlocked until a corresponding source file is read. Repository
// is optional for a global search. A bounded scoped search still establishes
// which named repository was searched; only a complete search can establish
// that an absence claim was exhaustively checked.
func RecordSourceSearch(ctx context.Context, complete, matched bool, repositories ...string) {
	if state := stateFrom(ctx); state != nil {
		state.mu.Lock()
		state.sourceSearch = true
		// Only a global search can establish completeness for a general
		// zero-hit/unknown answer. Scoped searches still support verified reads.
		state.sourceComplete = state.sourceComplete || (complete && len(repositories) == 0)
		state.sourceMatched = state.sourceMatched || matched
		if state.sourceSearchIdentities == nil {
			state.sourceSearchIdentities = make(map[string]bool)
		}
		for _, repository := range repositories {
			if identity := repositoryIdentity(repository); identity != "" {
				state.sourceSearchIdentities[identity] = true
			}
		}
		state.mu.Unlock()
	}
}

// RecordSourceRead must be called only after trusted server code has verified
// a successful source_browse read and its private provenance marker. Repository
// is optional for ordinary source questions; an integration question with
// explicit named projects uses it to require a read for every project.
func RecordSourceRead(ctx context.Context, repositories ...string) {
	if state := stateFrom(ctx); state != nil {
		state.mu.Lock()
		state.sourceRead = true
		if state.sourceReadRepos == nil {
			state.sourceReadRepos = make(map[string]bool)
		}
		if state.sourceReadIdentities == nil {
			state.sourceReadIdentities = make(map[string]bool)
		}
		for _, repository := range repositories {
			if leaf := repositoryLeaf(repository); leaf != "" {
				state.sourceReadRepos[leaf] = true
			}
			if identity := repositoryIdentity(repository); identity != "" {
				state.sourceReadIdentities[identity] = true
			}
		}
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
func RecordReleaseEvidence(ctx context.Context, repositories ...string) {
	if state := stateFrom(ctx); state != nil {
		state.mu.Lock()
		state.releaseEvidence = true
		if state.releaseIdentities == nil {
			state.releaseIdentities = make(map[string]bool)
		}
		for _, repository := range repositories {
			if identity := repositoryIdentity(repository); identity != "" {
				state.releaseIdentities[identity] = true
			}
		}
		state.mu.Unlock()
	}
}

// RecordReleaseLookup records a completed call to GitHub's dedicated
// latest-release endpoint. It intentionally does not claim that a stable
// release exists; it merely prevents a model from claiming uncertainty without
// attempting the official source at all.
func RecordReleaseLookup(ctx context.Context) {
	if state := stateFrom(ctx); state != nil {
		state.mu.Lock()
		state.releaseLookup = true
		state.mu.Unlock()
	}
}

// ShouldHoldStreamingAnswer prevents an unverified natural answer from being
// optimistically exposed in a stream. A safe explicit unknown is later
// released as normal; only version or integration conclusions are retried or
// replaced when their intent-specific evidence is missing.
func ShouldHoldStreamingAnswer(ctx context.Context) bool {
	if multipleNeeds(ctx) {
		return !allRequiredEvidenceObserved(ctx)
	}
	switch IntentFromContext(ctx) {
	case IntentSource:
		return !SourceReadObserved(ctx)
	case IntentRelease:
		return !ReleaseEvidenceObserved(ctx)
	case IntentIntegration:
		return !IntegrationEvidenceObserved(ctx)
	default:
		return false
	}
}

// CanRetryEvidence grants a small, intent-specific number of explicit evidence
// retries. A release lookup and a source search/read are two-step operations:
// the model first receives an opaque catalog/search result, then needs one more
// turn to request the concrete release or file body. The bound prevents loops
// while allowing that normal two-step protocol to complete.
func CanRetryEvidence(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.needs.has(IntentSource) && state.needs.has(IntentRelease) &&
		state.sourceRead && state.releaseEvidence {
		// The current release marker carries a tag but no resolved tag commit.
		// Repeating source/release tools cannot prove that a default-branch
		// snapshot implements that release. Stop immediately with the precise
		// repository/alignment fallback instead of burning four model rounds.
		return false
	}
	maxRetries := 1
	switch {
	case state.needs.multiple():
		// A compound request may need two catalog/reader pairs. Keep the
		// budget finite rather than letting the model loop without evidence.
		maxRetries = 4
	case state.intent == IntentRelease || state.intent == IntentIntegration:
		maxRetries = 2
	}
	if state.needs.multiple() && state.needs.has(IntentRelease) &&
		state.releaseLookup && !state.releaseEvidence {
		otherMissing := state.needs.has(IntentSource) && !state.sourceRead
		if state.needs.has(IntentIntegration) {
			otherMissing = otherMissing || !integrationEvidenceObservedLocked(state)
		}
		if !otherMissing {
			// The trusted latest endpoint already ran but did not return
			// verifiable stable-release provenance. No more release retries.
			return false
		}
		maxRetries = 2 // reserve only a search/read pair for other needs
	}
	if state.nudgeCount >= maxRetries {
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
	if multipleNeeds(ctx) {
		// We cannot validate which clause of a free-form answer is merely an
		// uncertainty statement. Until each required proof is present, hold
		// the whole answer and return a bounded, explicit fallback.
		return !allRequiredEvidenceObserved(ctx)
	}
	switch IntentFromContext(ctx) {
	case IntentSource:
		return !SourceReadObserved(ctx)
	case IntentRelease:
		if ReleaseEvidenceObserved(ctx) {
			return false
		}
		// An explicit unknown is safe only after the official latest-release
		// endpoint was actually queried. A README, source snapshot, tag listing,
		// or model assertion of uncertainty is not a lookup.
		return !ReleaseLookupObserved(ctx) || !isExplicitReleaseUnknown(answer)
	case IntentIntegration:
		if IntegrationEvidenceObserved(ctx) {
			return false
		}
		if len(RequiredRepositories(ctx)) > 0 {
			// Explicitly named channel projects are a repository-by-repository
			// question. A source read from one project can never cover another.
			return true
		}
		// For a generic support question, a safe unknown is allowed only after
		// a complete zero-hit source search. A timed-out/capped search is not
		// exhaustive and cannot be used to shortcut the retry/fallback path.
		if !SourceSearchObserved(ctx) || !SourceSearchComplete(ctx) || SourceSearchMatched(ctx) || SourceReadObserved(ctx) {
			return true
		}
		return !isExplicitIntegrationUnknown(answer)
	default:
		return false
	}
}

// NeedsSynthesisFallback prevents max-iteration synthesis from inventing a
// conclusion after the run has already failed to obtain the required evidence.
func NeedsSynthesisFallback(ctx context.Context) bool {
	if multipleNeeds(ctx) {
		return !allRequiredEvidenceObserved(ctx)
	}
	switch IntentFromContext(ctx) {
	case IntentSource:
		return !SourceReadObserved(ctx)
	case IntentRelease:
		return !ReleaseEvidenceObserved(ctx)
	case IntentIntegration:
		return !IntegrationEvidenceObserved(ctx)
	default:
		return false
	}
}

// AllowsMissingIssue is the persistence-side gate for an automatic “missing
// knowledge” record. The normal retrieval trace proves that a lookup actually
// ran; this method adds the stronger contract-specific proof so a source
// search without an actual read cannot be mislabeled as a knowledge gap.
func AllowsMissingIssue(ctx context.Context) bool {
	if multipleNeeds(ctx) {
		return allRequiredEvidenceObserved(ctx)
	}
	switch IntentFromContext(ctx) {
	case IntentSource:
		return SourceReadObserved(ctx)
	case IntentRelease:
		return ReleaseEvidenceObserved(ctx)
	case IntentIntegration:
		return IntegrationEvidenceObserved(ctx)
	default:
		return true
	}
}

func allRequiredEvidenceObserved(ctx context.Context) bool {
	// Each kind is observed from trusted tools in the authorized turn. For a
	// source+release question, even matching full repository identities do not
	// unlock the conclusion: no trusted tag-to-snapshot commit alignment exists
	// yet, so that composite path deliberately remains fail-closed.
	if Requires(ctx, IntentSource) && !SourceReadObserved(ctx) {
		return false
	}
	if Requires(ctx, IntentRelease) && !ReleaseEvidenceObserved(ctx) {
		return false
	}
	if Requires(ctx, IntentIntegration) && !IntegrationEvidenceObserved(ctx) {
		return false
	}
	if Requires(ctx, IntentSource) && Requires(ctx, IntentRelease) {
		// Matching repository identity is necessary but insufficient: a
		// GitHub Release tag has not been resolved to a commit and compared
		// with the read-only snapshot revision. Never unlock a free-form
		// "latest release's source implementation" conclusion on that basis.
		return false
	}
	return true
}

func sameRepositoryEvidenceObserved(ctx context.Context) bool {
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	for repository := range state.sourceReadIdentities {
		if state.releaseIdentities[repository] {
			return true
		}
	}
	return false
}

func integrationEvidenceObservedLocked(state *State) bool {
	if len(state.requiredRepos) == 0 {
		return state.sourceRead || state.documentEvidence
	}
	if !state.sourceSearch || !state.sourceRead {
		return false
	}
	for _, repository := range state.requiredRepos {
		if !namedRepositoryReadAfterSearchLocked(state, repository) {
			return false
		}
	}
	return true
}

func namedRepositoryReadAfterSearchLocked(state *State, requiredLeaf string) bool {
	if !state.sourceReadRepos[requiredLeaf] {
		return false
	}
	for identity := range state.sourceReadIdentities {
		if repositoryLeaf(identity) == requiredLeaf && state.sourceSearchIdentities[identity] {
			return true
		}
	}
	return false
}

func missingEvidenceLabels(ctx context.Context, chinese bool) []string {
	var missing []string
	if Requires(ctx, IntentSource) && !SourceReadObserved(ctx) {
		if chinese {
			missing = append(missing, "源码文件及行段")
		} else {
			missing = append(missing, "source file and lines")
		}
	}
	if Requires(ctx, IntentRelease) && !ReleaseEvidenceObserved(ctx) {
		if chinese {
			missing = append(missing, "可核验的发布记录")
		} else {
			missing = append(missing, "verifiable release record")
		}
	}
	if Requires(ctx, IntentIntegration) && !IntegrationEvidenceObserved(ctx) {
		if chinese {
			missing = append(missing, "接入能力的资料或源码正文")
		} else {
			missing = append(missing, "integration documentation or source body")
		}
	}
	if Requires(ctx, IntentSource) && Requires(ctx, IntentRelease) &&
		SourceReadObserved(ctx) && ReleaseEvidenceObserved(ctx) {
		if !sameRepositoryEvidenceObserved(ctx) {
			if chinese {
				missing = append(missing, "同一仓库的源码与发布来源")
			} else {
				missing = append(missing, "source and release evidence from the same repository")
			}
		} else if chinese {
			missing = append(missing, "发布标签与源码快照的提交对应关系")
		} else {
			missing = append(missing, "release tag to source snapshot commit alignment")
		}
	}
	return missing
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
	return isApprovedUncertaintyAnswer(answer,
		"无法确认最新", "无法确认最新版本", "无法核验最新", "无法核验最新版本", "不能确认最新", "不能确认最新版本", "无法确认当前版本",
		"当前材料无法确认", "当前材料无法确认最新版本", "当前资料无法确认", "当前资料无法确认最新版本", "当前授权资料无法确认", "当前授权资料无法确认最新版本",
		"没有取得可核验的发布", "没有取得可核验的发布记录", "没有可核验的发布", "没有可核验的发布记录", "没有发布证据",
		"cannot confirm the latest", "cannot confirm the latest version", "cannot verify the latest", "cannot verify the latest version", "unable to verify the latest", "unable to verify the latest version",
		"cannot confirm the current version", "cannot verify the current version", "current material cannot confirm", "no verifiable release evidence",
	)
}

// isExplicitIntegrationUnknown identifies the narrow safe reply allowed when
// no documentation/source body was read. Any other answer could assert either
// support or non-support, including a terse “yes” or “no”.
func isExplicitIntegrationUnknown(answer string) bool {
	return isApprovedUncertaintyAnswer(answer,
		"当前材料无法确认", "当前资料无法确认", "当前授权资料无法确认", "无法确认是否支持", "无法核验是否支持", "不能确认是否支持",
		"当前材料无法确认是否支持", "当前资料无法确认是否支持", "当前授权资料无法确认是否支持",
		"没有读取到可核验", "没有读取到可核验证据", "未读到可核验", "未读到可核验证据",
		"cannot confirm support", "cannot verify support", "unable to confirm whether", "unable to verify whether",
	)
}

// isApprovedUncertaintyAnswer permits only a complete, deliberately narrow
// uncertainty reply. The old substring check let a model prepend a safe phrase
// and then append an unverified version or integration claim after a comma or
// contrast word. If a harmless but more elaborate uncertainty response is not
// listed here, the normal retry/fallback path remains safe and supplies the
// localized deterministic reply.
func isApprovedUncertaintyAnswer(answer string, approved ...string) bool {
	normalized := strings.ToLower(strings.TrimSpace(answer))
	normalized = strings.Trim(normalized, " \t\r\n。！？!?…")
	if normalized == "" {
		return false
	}
	for _, candidate := range approved {
		if normalized == strings.ToLower(candidate) {
			return true
		}
	}
	return false
}

// Prompt is system-owned guidance added only for classified Octo turns. The
// source hard gate below is still authoritative; this prompt helps the model
// choose the right tool before the gate needs to intervene.
func Prompt(ctx context.Context) string {
	if multipleNeeds(ctx) {
		if isChinese(ctx) {
			var rules []string
			if Requires(ctx, IntentSource) {
				rules = append(rules, "源码实现需要 source_browse 搜索并 read 实际文件和行段。")
			}
			if Requires(ctx, IntentRelease) {
				rules = append(rules, "最新版本、发布日期和更新内容需要 github_release_lookup 的官方发布来源。")
			}
			if Requires(ctx, IntentIntegration) {
				rules = append(rules, "支持或接入结论需要当前授权范围的资料正文或源码正文。")
			}
			if Requires(ctx, IntentSource) && Requires(ctx, IntentRelease) {
				rules = append(rules, "同仓库仍不等于同版本；未核实发布标签对应提交与源码快照提交一致时，不得说‘最新版源码如此实现’，两类来源只能视为各自独立的记录。")
			}
			return "<answer_evidence_contract>\n本回合有多项独立证据需求，须分别核验后才能给出整体结论。" + strings.Join(rules, "") + "不能用一个来源替代另一个，也不能把一仓结论套用到另一仓。工具成功、目录、标题、RAG 未命中和历史对话都不等于完整证据。任一必要证据缺失时，先补查；仍无法取得时，系统将说明缺项而不输出未经核实的整体结论。\n</answer_evidence_contract>"
		}
		var rules []string
		if Requires(ctx, IntentSource) {
			rules = append(rules, "Source implementation requires source_browse search and a read of actual file lines.")
		}
		if Requires(ctx, IntentRelease) {
			rules = append(rules, "Latest version, date and changes require an official github_release_lookup result.")
		}
		if Requires(ctx, IntentIntegration) {
			rules = append(rules, "Support or integration requires an authorized document or source body.")
		}
		if Requires(ctx, IntentSource) && Requires(ctx, IntentRelease) {
			rules = append(rules, "The same repository is not the same version: unless the release tag's commit has been verified against the source snapshot commit, do not say this is how the latest release is implemented; treat the obtained sources as independent records.")
		}
		return "<answer_evidence_contract>\nThis turn has independent evidence needs. " + strings.Join(rules, " ") + " One source cannot substitute for another, and one repository cannot establish facts about another. Tool success, catalogs, titles, RAG misses and history are not complete evidence. If any required evidence is missing, retrieve it first; if it remains unavailable, the system will name the gap instead of emitting an unverified overall conclusion.\n</answer_evidence_contract>"
	}
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
			if len(RequiredRepositories(ctx)) > 0 {
				return `<answer_evidence_contract>
本回合点名了一个或多个 *-channel-octo 项目。对每个项目的职责、接入方式、存储、沙箱、工作区或运行方式，都必须先用 source_browse 搜索当前授权源码，再 read 该项目自身的 README、接口文档或源码文件；不能把一个项目的实现细节归纳到其他项目。发布标签、RAG 命中或未命中、文档缺失和文件名都不能单独证明结论。若某个项目未读到自身文件，只能明确说明该项目当前材料无法确认。
</answer_evidence_contract>`
			}
			return `<answer_evidence_contract>
本回合询问支持、接入或兼容性。无论结论是“支持”还是“不支持”，都必须读取当前授权范围内的实际 README、接口文档、知识库正文、Wiki 正文或源码；发布标签、RAG 未命中、文档缺失和文件名都不能单独证明结论。若走源码路径，先 source_browse 搜索再 read 实际文件。若未读到证据，只能说明当前材料无法确认。
</answer_evidence_contract>`
		}
		if len(RequiredRepositories(ctx)) > 0 {
			return `<answer_evidence_contract>
This turn names one or more *-channel-octo projects. Before stating each project's role, integration, storage, sandbox, workspace, or execution behaviour, use source_browse to search authorized source snapshots and read that project's own README/API document or source file. Never generalize implementation details from one project to another. A release tag, RAG hit or miss, missing document, or filename alone cannot establish a conclusion. If a named project was not read, say its current material cannot confirm it.
</answer_evidence_contract>`
		}
		return `<answer_evidence_contract>
This turn asks about support, integration, or compatibility. Both affirmative and negative conclusions require an actual authorized README/API document, knowledge-base body, Wiki body, or source read. A release tag, RAG miss, missing document, or filename alone cannot establish the conclusion. When using source snapshots, search with source_browse and then read the actual file. If no content was read, say the current material cannot confirm it.
</answer_evidence_contract>`
	default:
		return ""
	}
}

func RetryNudge(ctx context.Context) string {
	if multipleNeeds(ctx) {
		missing := missingEvidenceLabels(ctx, isChinese(ctx))
		if Requires(ctx, IntentRelease) && ReleaseLookupObserved(ctx) && !ReleaseEvidenceObserved(ctx) {
			if isChinese(ctx) {
				return "官方最新发布来源已查询，但未取得可核验的稳定发布；不要重复相同查询或断言没有发布。仅补查其它仍缺少的证据：" + strings.Join(missing, "、") + "。"
			}
			return "The official latest-release source was queried but no verifiable stable release was obtained. Do not repeat the same lookup or claim no release exists. Retrieve only the other missing evidence: " + strings.Join(missing, ", ") + "."
		}
		if isChinese(ctx) {
			return "本轮仍缺少独立的证据：" + strings.Join(missing, "、") + "。请仅在授权范围内补查相应工具并读取正文；不要把其他来源、检索摘要或工具成功当成证明。"
		}
		return "This turn still lacks independent evidence: " + strings.Join(missing, ", ") + ". Use the corresponding authorized tools and read source bodies; do not treat other sources, search summaries or tool success as proof."
	}
	switch IntentFromContext(ctx) {
	case IntentSource:
		if isChinese(ctx) {
			return "在给出源码结论前，先使用 source_browse 查找并 read 实际文件和行段。不要重复未核验的结论；若无法获得 read 证据，请明确说明证据不足。"
		}
		return "Before giving a source-code conclusion, use source_browse to find and read the actual file and lines. Do not repeat an unverified conclusion; if a read is unavailable, state that evidence is insufficient."
	case IntentRelease:
		if isChinese(ctx) {
			return "不要把 README、RAG、源码快照或资料未命中当成“最新发布”的证据。请使用 github_release_lookup 的 list 后 latest，读取官方 GitHub Release；若 latest 已执行但没有可核验发布证据，只能说明当前材料无法确认。"
		}
		return "Do not treat README/RAG/source snapshots or missing material as proof of the latest release. Use github_release_lookup list then latest to read an official GitHub Release; only after latest ran without verifiable release evidence may you say the current material cannot confirm it."
	case IntentIntegration:
		if isChinese(ctx) {
			missing := MissingRequiredRepositories(ctx)
			if len(missing) > 0 {
				return "请先用 source_browse 搜索并读取每个点名项目的实际文件。尚缺少读取证据的项目：" + strings.Join(missing, "、") + "。不要把一个项目的实现细节归纳到其他项目。"
			}
			return "不要把发布标签、RAG 未命中或资料缺失当成“支持”或“不支持”。请先读取可核验的 README、接口文档、知识库正文、Wiki 正文或源码；若走源码路径，先用 source_browse 搜索并 read 实际文件。只有完整 source_browse 零命中后，才能说明当前材料无法确认。"
		}
		return "Do not treat a release tag, RAG miss, or missing material as proof of support or non-support. First read a verifiable README/API document, knowledge-base body, Wiki body, or source file; when using source snapshots, search with source_browse and then read the file. Only a complete zero-hit source search may support saying the current material cannot confirm it."
	default:
		return ""
	}
}

func FallbackReply(ctx context.Context) string {
	if multipleNeeds(ctx) {
		missing := missingEvidenceLabels(ctx, isChinese(ctx))
		if Requires(ctx, IntentRelease) && ReleaseLookupObserved(ctx) && !ReleaseEvidenceObserved(ctx) {
			if isChinese(ctx) {
				if SourceReadObserved(ctx) {
					return "已查询官方最新发布来源，也已读取源码，但未取得可核验的稳定发布记录，不能把这份源码认定为最新发布版本的实现。"
				}
				return "已查询官方最新发布来源，但未取得可核验的稳定发布记录；同时还缺少" + strings.Join(missing, "、") + "，目前不能完整核实。"
			}
			if SourceReadObserved(ctx) {
				return "I checked the official latest-release source and read source code, but obtained no verifiable stable release record. I cannot identify that source snapshot as the latest released implementation."
			}
			return "The official latest-release source was queried, but no verifiable stable release record was obtained. Other evidence is still missing: " + strings.Join(missing, ", ") + "."
		}
		if Requires(ctx, IntentSource) && Requires(ctx, IntentRelease) &&
			SourceReadObserved(ctx) && ReleaseEvidenceObserved(ctx) {
			if !sameRepositoryEvidenceObserved(ctx) {
				if isChinese(ctx) {
					return "已读取源码并取得发布记录，但二者来自不同仓库或仓库身份无法对应，不能把一个仓库的实现归到另一个仓库的发布版本。"
				}
				return "Source and release evidence were obtained, but their repositories differ or cannot be matched. I cannot attribute one repository's implementation to another repository's release."
			}
			if isChinese(ctx) {
				return "已分别读取同一仓库的源码与官方发布记录，但尚未核实发布标签对应的提交是否等于源码快照提交，因此不能确认这份源码就是该发布版本的实现。"
			}
			return "I read source code and an official release record from the same repository, but have not verified that the release tag resolves to the source snapshot commit. I cannot identify that snapshot as the released implementation."
		}
		if isChinese(ctx) {
			return "当前还缺少" + strings.Join(missing, "、") + "，因此不能完整核实这个问题。请提供更具体的目标仓库、版本或资料范围，或稍后重试。"
		}
		return "I could not fully verify this question because evidence is missing for " + strings.Join(missing, ", ") + ". Please provide a more specific repository, version or source scope, or try again later."
	}
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
			return "当前没有读取到可核验的 README、接口文档、知识库正文、Wiki 正文或源码，因此不能确认该能力支持或不支持接入；若点名项目也不能归纳其职责或实现细节。请提供仓库、路径、文档范围或允许访问对应资料后再确认。"
		}
		return "I did not read a verifiable README/API document, knowledge-base body, Wiki body, or source file, so I cannot confirm integration support or generalize a named project's role or implementation. Please provide a repository, path, document scope, or access to the relevant material."
	default:
		return ""
	}
}
