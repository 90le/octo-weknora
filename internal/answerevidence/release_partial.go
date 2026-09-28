package answerevidence

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ReleaseFact is a private, turn-local projection of a trusted positive
// GitHubReleaseCitation. It must never be reconstructed from model text, a
// stored tool step, or a release history/tag listing.
type ReleaseFact struct {
	Repository string
	TagName    string
	URL        string
	CheckedAt  time.Time
}

func validReleaseRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

func normalizedReleaseFact(fact ReleaseFact) (ReleaseFact, bool) {
	if !validReleaseRepository(fact.Repository) || fact.TagName == "" || len(fact.TagName) > 256 ||
		!utf8.ValidString(fact.TagName) || fact.CheckedAt.IsZero() {
		return ReleaseFact{}, false
	}
	for _, r := range fact.TagName {
		if unicode.IsControl(r) {
			return ReleaseFact{}, false
		}
	}
	// The trusted connector builds this URL from its authorized repository and
	// the tag returned by GitHub. Check the same canonical form before any
	// private marker can become a visible Markdown link.
	wantURL := "https://github.com/" + fact.Repository + "/releases/tag/" + url.PathEscape(fact.TagName)
	if fact.URL != wantURL {
		return ReleaseFact{}, false
	}
	fact.Repository = repositoryIdentity(fact.Repository)
	fact.URL = "https://github.com/" + fact.Repository + "/releases/tag/" + url.PathEscape(fact.TagName)
	fact.CheckedAt = fact.CheckedAt.UTC()
	return fact, true
}

// RecordReleaseFact admits only the private citation obtained from a
// completed, authorized latest-release tool call. Conflicting positive facts
// for one repository remain ambiguous for the rest of this turn.
func RecordReleaseFact(ctx context.Context, fact ReleaseFact) bool {
	fact, ok := normalizedReleaseFact(fact)
	if !ok {
		return false
	}
	state := stateFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.releaseEvidence = true
	if state.releaseIdentities == nil {
		state.releaseIdentities = make(map[string]bool)
	}
	state.releaseIdentities[fact.Repository] = true
	if state.releaseFacts == nil {
		state.releaseFacts = make(map[string]ReleaseFact)
	}
	if state.releaseFactConflicts[fact.Repository] {
		return false
	}
	if previous, exists := state.releaseFacts[fact.Repository]; exists {
		if previous.TagName != fact.TagName || previous.URL != fact.URL {
			if state.releaseFactConflicts == nil {
				state.releaseFactConflicts = make(map[string]bool)
			}
			state.releaseFactConflicts[fact.Repository] = true
			return false
		}
		if previous.CheckedAt.After(fact.CheckedAt) {
			return true
		}
	}
	state.releaseFacts[fact.Repository] = fact
	return true
}

// RecordReleaseTargetResolution is called only after a scoped release list
// uniquely resolved a user-named repository. Ownerless names require a
// complete catalog page; an explicit owner may be resolved by an exact row.
func RecordReleaseTargetResolution(ctx context.Context, required, actual string) {
	state := stateFrom(ctx)
	actual = repositoryIdentity(actual)
	required = repositoryIdentity(required)
	if state == nil || !validReleaseRepository(actual) || !requiredRepositoryMatches(required, actual) {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	found := false
	for _, target := range state.requiredReleases {
		if target == required {
			found = true
			break
		}
	}
	if !found || state.releaseTargetConflicts[required] {
		return
	}
	if state.releaseTargets == nil {
		state.releaseTargets = make(map[string]string)
	}
	if previous := state.releaseTargets[required]; previous != "" && previous != actual {
		if state.releaseTargetConflicts == nil {
			state.releaseTargetConflicts = make(map[string]bool)
		}
		state.releaseTargetConflicts[required] = true
		return
	}
	state.releaseTargets[required] = actual
}

func releaseFactForTargetLocked(state *State, required string) (ReleaseFact, bool) {
	identity := required
	if !strings.Contains(required, "/") {
		if state.releaseTargetConflicts[required] {
			return ReleaseFact{}, false
		}
		identity = state.releaseTargets[required]
	}
	if identity == "" || state.releaseFactConflicts[identity] || !state.releaseIdentities[identity] {
		return ReleaseFact{}, false
	}
	fact, ok := state.releaseFacts[identity]
	return fact, ok
}

func escapeReleaseTag(tag string) string {
	var out strings.Builder
	for _, r := range tag {
		switch r {
		case '\\', 0x60, '*', '_', '[', ']', '(', ')', '!':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '<':
			out.WriteString("&lt;")
		case '>':
			out.WriteString("&gt;")
		case '&':
			out.WriteString("&amp;")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// partialReleaseReply is deliberately narrower than a normal answer: it can
// show a verified latest stable tag, its release page and the check time, but
// never invent release changes, a publication date or source implementation.
func partialReleaseReply(ctx context.Context) string {
	state := stateFrom(ctx)
	if state == nil {
		return ""
	}
	state.mu.RLock()
	if state.needs != needRelease || len(state.requiredReleases) < 2 || len(state.releaseFacts) == 0 {
		state.mu.RUnlock()
		return ""
	}
	chinese := state.chinese
	type verified struct {
		fact ReleaseFact
	}
	known := make([]verified, 0, len(state.requiredReleases))
	missing := make([]string, 0, len(state.requiredReleases))
	for _, target := range state.requiredReleases {
		if fact, ok := releaseFactForTargetLocked(state, target); ok {
			known = append(known, verified{fact: fact})
		} else {
			missing = append(missing, target)
		}
	}
	state.mu.RUnlock()
	if len(known) == 0 || len(missing) == 0 {
		return ""
	}
	var lines []string
	if chinese {
		lines = append(lines, "以下仅列出本轮可核验的 GitHub 最新稳定 Release：")
		for _, item := range known {
			lines = append(lines, fmt.Sprintf("- %s：截至 %s，标签为「%s」；[官方 Release](<%s>)。",
				item.fact.Repository, item.fact.CheckedAt.Format(time.RFC3339), escapeReleaseTag(item.fact.TagName), item.fact.URL))
		}
		lines = append(lines, "以下目标未核验：")
		for _, target := range missing {
			lines = append(lines, "- "+target+"：本轮未取得可核验的最新稳定 Release 记录。")
		}
		lines = append(lines, "这不代表未核验的仓库没有发布；本答复也未核验更新内容、发布日期或源码实现。")
	} else {
		lines = append(lines, "Verified GitHub latest stable Releases in this turn:")
		for _, item := range known {
			lines = append(lines, fmt.Sprintf("- %s: as of %s, tag %s; [official Release](<%s>).",
				item.fact.Repository, item.fact.CheckedAt.Format(time.RFC3339), escapeReleaseTag(item.fact.TagName), item.fact.URL))
		}
		lines = append(lines, "Unverified targets:")
		for _, target := range missing {
			lines = append(lines, "- "+target+": no verifiable latest stable Release record was obtained in this turn.")
		}
		lines = append(lines, "This does not mean those repositories have no releases. Changes, publication dates and source implementation were not verified.")
	}
	return strings.Join(lines, "\n")
}
