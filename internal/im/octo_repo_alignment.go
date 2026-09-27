package im

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
)

var octoRepoHeadingRE = regexp.MustCompile("^(#{1,6})[ \t]+(.+)$")
var octoRepoListPrefixRE = regexp.MustCompile(`^(?:[-*][ \t]+|[0-9]+[.)][ \t]+)`)
var octoReferenceCodeLinkRE = regexp.MustCompile(`(?i)^\s{0,3}\[[^\]\r\n]{1,100}\]:\s*<?https://github\.com/[^>\s]+/blob/[^>\s]+`)

type octoRepoHeading struct {
	level int
	repo  string
}

func octoGitHubCodeRepository(raw string) string {
	if !isPinnedOctoGitHubLineURL(raw) {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 5 {
		return ""
	}
	return strings.ToLower(parts[0] + "/" + parts[1])
}

func octoRepositoryTokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-' || b == '.' || b == '/'
}

func octoContainsRepositoryToken(haystack, needle string) bool {
	for at := 0; at < len(haystack); {
		pos := strings.Index(haystack[at:], needle)
		if pos < 0 {
			return false
		}
		pos += at
		end := pos + len(needle)
		if (pos == 0 || !octoRepositoryTokenByte(haystack[pos-1])) &&
			(end == len(haystack) || !octoRepositoryTokenByte(haystack[end])) {
			return true
		}
		at = pos + 1
	}
	return false
}

func octoNormalizedRepository(raw string) string {
	value := strings.ToLower(strings.Trim(strings.TrimSpace(raw), "/"))
	value = strings.TrimPrefix(value, "https://github.com/")
	value = strings.TrimPrefix(value, "github.com/")
	parts := strings.Split(value, "/")
	if len(parts) == 1 && strings.HasSuffix(value, "-channel-octo") {
		return value // An explicitly named leaf still identifies a requested project.
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	for _, part := range parts {
		if strings.ContainsAny(part, " \t\r\n?#") || part == "." || part == ".." {
			return ""
		}
	}
	return value
}

func octoDistinctiveRepositoryLeaf(leaf string) bool {
	// Ordinary prose such as "main worker" must not change repository scope.
	return strings.ContainsAny(leaf, "-_.")
}

func octoKnownRepositories(trusted map[string]string, extra []string) (map[string]bool, map[string]int, map[string]bool) {
	known, observed := map[string]bool{}, map[string]bool{}
	for raw, claimed := range trusted {
		valid := safeOctoSourceURL(raw)
		repository := octoGitHubCodeRepository(valid)
		if repository == "" || repository != octoNormalizedRepository(claimed) {
			continue
		}
		observed[valid] = true
		known[repository] = true
	}
	// Add explicitly named owner/repo identities before ownerless leaves, so a
	// duplicate mention does not create a fictitious second repository.
	for _, raw := range extra {
		if repository := octoNormalizedRepository(raw); strings.Contains(repository, "/") {
			known[repository] = true
		}
	}
	for _, raw := range extra {
		leaf := octoNormalizedRepository(raw)
		if leaf == "" || strings.Contains(leaf, "/") {
			continue
		}
		matched := false
		for repository := range known {
			if strings.HasSuffix(repository, "/"+leaf) {
				matched = true
				break
			}
		}
		if !matched {
			known[leaf] = true
		}
	}
	leaves := map[string]int{}
	for repository := range known {
		leaf := repository[strings.LastIndexByte(repository, '/')+1:]
		leaves[leaf]++
	}
	return known, leaves, observed
}

// Only current-turn, verified repository names count. Citation links are
// removed first so a wrong URL cannot authorize itself by mentioning its repo.
func octoMentionedRepository(prose string, known map[string]bool, leaves map[string]int) (string, int) {
	prose = strings.ToLower(prose)
	prose = octoSourceTagRE.ReplaceAllString(prose, "")
	prose = octoMarkdownGitHubLinkRE.ReplaceAllStringFunc(prose, func(link string) string {
		match := octoMarkdownGitHubLinkRE.FindStringSubmatch(link)
		if len(match) != 3 {
			return ""
		}
		// A repository-root navigation link may visibly name the section's
		// owner. A code citation cannot declare its own ownership via label.
		parsed, err := url.Parse(strings.Trim(match[2], "<>"))
		if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return ""
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) != 2 {
			return ""
		}
		label := strings.TrimSpace(match[1])
		if strings.EqualFold(label, parts[1]) || strings.EqualFold(label, parts[0]+"/"+parts[1]) {
			return label
		}
		return ""
	})
	prose = octoBareGitHubURLRE.ReplaceAllString(prose, "")
	found, count := "", 0
	for repository := range known {
		leaf := repository[strings.LastIndexByte(repository, '/')+1:]
		if strings.Contains(repository, "/") && octoContainsRepositoryToken(prose, repository) ||
			octoDistinctiveRepositoryLeaf(leaf) && leaves[leaf] == 1 && octoContainsRepositoryToken(prose, leaf) {
			found, count = repository, count+1
		}
	}
	return found, count
}

func octoStandaloneRepositoryLine(line string, known map[string]bool, leaves map[string]int) string {
	line = strings.TrimSpace(octoRepoListPrefixRE.ReplaceAllString(strings.TrimSpace(line), ""))
	if !strings.HasPrefix(line, "**") && !strings.HasSuffix(line, ":") && !strings.HasSuffix(line, "：") {
		return "" // A plain list item is not enough to establish a section.
	}
	line = octoMarkdownGitHubLinkRE.ReplaceAllString(line, "$1")
	line = strings.TrimSpace(strings.Trim(line, "*_"))
	line = strings.TrimSpace(strings.TrimRight(line, ":："))
	line = strings.TrimSpace(strings.Trim(line, "*_"))
	line = strings.ToLower(line)
	for repository := range known {
		leaf := repository[strings.LastIndexByte(repository, '/')+1:]
		if line == repository || octoDistinctiveRepositoryLeaf(leaf) && leaves[leaf] == 1 && line == leaf {
			return repository
		}
	}
	return ""
}

func octoSourceFooterHeading(line string) bool {
	if octoSourceHeadingRE.MatchString(line) {
		return true
	}
	line = strings.TrimSpace(line)
	if heading := octoRepoHeadingRE.FindStringSubmatch(line); len(heading) == 3 {
		line = heading[2]
	}
	line = strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "*_ :：")))
	switch line {
	case "来源", "实际来源", "参考资料", "参考", "资料依据", "source", "sources", "reference", "references":
		return true
	}
	return false
}

// Detect a source from another repository inside a section or paragraph that
// explicitly concerns one repo. Ambiguous comparisons are left unclassified;
// this proves ownership only, not semantic support of the adjacent claim.
func octoCrossRepositoryCitationMismatch(answer string, trusted map[string]string, extra ...string) bool {
	known, leaves, observed := octoKnownRepositories(trusted, extra)
	if len(known) < 2 {
		return false
	}
	headings := []octoRepoHeading{}
	sectionRepo, inFence, mismatch := "", false, false
	var paragraph strings.Builder
	flush := func() {
		if paragraph.Len() == 0 || mismatch {
			paragraph.Reset()
			return
		}
		block := paragraph.String()
		paragraph.Reset()
		expected, count := octoMentionedRepository(block, known, leaves)
		if count > 1 {
			return
		}
		if count == 0 {
			expected = sectionRepo
		}
		if expected == "" {
			return
		}
		for _, raw := range octoBareGitHubURLRE.FindAllString(block, -1) {
			candidate := safeOctoSourceURL(raw)
			if candidate == "" || !octoObservedGitHubLineURL(candidate, observed) {
				continue
			}
			if actual := octoGitHubCodeRepository(candidate); actual != "" && actual != expected {
				mismatch = true
				return
			}
		}
	}
	for _, line := range strings.Split(answer, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "\x60\x60\x60") || strings.HasPrefix(trimmed, "~~~") {
			flush()
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		// A reference-style definition can sit outside the paragraph that uses
		// it. Until both locations are resolved together, fail closed for a
		// multi-repository answer rather than letting the definition bypass the
		// local claim-to-repository check.
		if octoReferenceCodeLinkRE.MatchString(trimmed) {
			return true
		}
		if octoSourceFooterHeading(trimmed) {
			flush()
			headings = nil
			sectionRepo = ""
			continue
		}
		if heading := octoRepoHeadingRE.FindStringSubmatch(trimmed); len(heading) == 3 {
			flush()
			level := len(heading[1])
			for len(headings) > 0 && headings[len(headings)-1].level >= level {
				headings = headings[:len(headings)-1]
			}
			inherited := ""
			if len(headings) > 0 {
				inherited = headings[len(headings)-1].repo
			}
			// Keep a Markdown link's visible heading label but discard its URL;
			// the linked repository cannot authorize itself as section owner.
			headingLabel := octoMarkdownGitHubLinkRE.ReplaceAllString(heading[2], "$1")
			named, count := octoMentionedRepository(headingLabel, known, leaves)
			if count == 1 {
				inherited = named
			} else if count > 1 {
				inherited = ""
			}
			headings = append(headings, octoRepoHeading{level: level, repo: inherited})
			sectionRepo = inherited
			continue
		}
		if standalone := octoStandaloneRepositoryLine(trimmed, known, leaves); standalone != "" {
			flush()
			headings = []octoRepoHeading{{level: 2, repo: standalone}}
			sectionRepo = standalone
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		// Adjacent bullets and table rows are independent claims. Combining
		// them would make two repository names look like one ambiguous paragraph
		// and silently accept a swapped source in one row.
		isTableRow := strings.Count(trimmed, "|") >= 2 || strings.Contains(trimmed, " | ")
		if octoRepoListPrefixRE.MatchString(trimmed) || isTableRow {
			flush()
		}
		paragraph.WriteString(line)
		paragraph.WriteByte('\n')
		if isTableRow {
			flush()
		}
	}
	flush()
	return mismatch
}

const imRepositoryMismatchFallback = "这次跨仓回答的源码引用与所属项目不一致，暂时无法可靠确认具体实现。请按仓库分别核对。"

// Only current-turn, authorized, narrow code reads can participate in the
// final repository ownership check. A model-written URL never becomes a
// trusted repository merely by appearing in answer text.
func octoRepositoryAlignmentMismatch(ctx context.Context, answer string) bool {
	if _, ok := octobusiness.PrincipalFromContext(ctx); !ok {
		return false
	}
	p, err := octobusiness.ValidatedPrincipal(ctx)
	if err != nil {
		return true // Revoked or unverifiable Octo scope cannot publish a reply.
	}
	trusted := map[string]string{}
	known := []string{}
	for _, source := range octobusiness.SourceCitations(ctx) {
		if !octoAllowedKB(p, source.KnowledgeBaseID) {
			continue
		}
		// A broad read may establish which repository is in scope, but cannot
		// authorize a citation. Reuse the full private provenance validation
		// (repository, pinned revision and path) without changing Citable.
		probe := source
		probe.Citable = true
		valid := trustedOctoSourceURL(probe)
		if valid == "" || !isPinnedOctoGitHubLineURL(valid) {
			continue
		}
		known = append(known, source.Repository)
		if source.Citable {
			trusted[valid] = source.Repository
		}
	}
	known = append(known, answerevidence.RequiredRepositories(ctx)...)
	return octoCrossRepositoryCitationMismatch(answer, trusted, known...)
}

// Use one normalization path before persisting either streaming or non-stream
// IM replies. A rejected draft has no published knowledge references and is
// marked fallback so it cannot become trusted follow-up history.
func octoGuardStoredAnswer(ctx context.Context, answer string, assistant *types.Message) string {
	if !octoRepositoryAlignmentMismatch(ctx, answer) {
		return answer
	}
	if assistant != nil {
		assistant.IsFallback = true
		assistant.KnowledgeReferences = nil
	}
	return imRepositoryMismatchFallback
}

// A multi-repository Octo turn may have optimistic model text and retracted
// preambles in the intermediate IM frame. Keep tool status, but withhold both
// answer and thought prose until repository ownership has been checked.
func octoHoldUnverifiedIntermediate(ctx context.Context, useAgent bool, parts *IMStreamParts) {
	if !useAgent || parts == nil || !answerevidence.ShouldHoldStreamingAnswer(ctx) {
		return
	}
	if _, ok := octobusiness.PrincipalFromContext(ctx); !ok {
		return
	}
	parts.LiveAnswer = ""
	parts.Answer = ""
	parts.AgentInner = ""
}
