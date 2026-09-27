package im

import (
	"context"
	"encoding/json"
	"html"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
)

var octoSourceTagRE = regexp.MustCompile(`<(kb|web)\b([^>]*)/?>`)
var octoSourceAttrRE = regexp.MustCompile(`([A-Za-z_]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
var octoSourceHeadingRE = regexp.MustCompile(`(?im)^\s*(?:\*\*)?(?:实际来源|来源|参考资料|参考|资料依据|sources?|references?)\s*(?:\*\*)?\s*[:：]\s*(?:\*\*)?`)
var octoQuotedSourceTitleRE = regexp.MustCompile(`《([^《》\r\n]{1,400})》`)
var octoGitCommitRE = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var octoGitHubLineFragmentRE = regexp.MustCompile(`^L[0-9]+(?:-L[0-9]+)?$`)
var octoMarkdownGitHubLinkRE = regexp.MustCompile(`(?i)\[([^\]\r\n]+)\]\((<?https://github\.com/[^\s)]+>?)\)`)
var octoBareGitHubURLRE = regexp.MustCompile(`(?i)https://github\.com/[^#\s<>"'\])，。；：！？,;:!?]+#(?:L|%4C)[0-9]+(?:-(?:L|%4C)[0-9]+)?`)
var octoBareCredentialRE = regexp.MustCompile(`(?i)(?:^|[\s:=])(?:sk-|bf_|app_|uk_)[a-z0-9_-]{8,}`)
var octoURLCredentialRE = regexp.MustCompile(`(?i)(?:^|[/#=:])(?:sk-|bf_|app_|uk_)[a-z0-9]{16,}`)
var octoWindowsSlashPathRE = regexp.MustCompile(`(?i)^[a-z]:/`)
var octoEmbeddedAbsolutePathRE = regexp.MustCompile(`(?i)(?:^|[\s（(:：])(?:[a-z]:/|~/|/[^/\s]+/)`)

const maxOctoObservedLineSubset = 12

type octoCitedSource struct{ title, url string }

// A model may write a plausible GitHub line link without reading that range.
// Keep such links clickable only when this turn's authorized source_browse read
// returned the exact pinned URL or a pinned range containing a short subset.
// This checks provenance, not whether the adjacent claim is semantically
// supported by the excerpt.
func sanitizeOctoGitHubLineLinks(ctx context.Context, answer string) string {
	if _, ok := octobusiness.PrincipalFromContext(ctx); !ok {
		return answer
	}
	p, err := octobusiness.ValidatedPrincipal(ctx)
	if err != nil {
		return answer
	}
	allowed := make(map[string]bool)
	for _, source := range octobusiness.SourceCitations(ctx) {
		if octoAllowedKB(p, source.KnowledgeBaseID) && source.Path != "" && source.Revision != "" {
			if valid := safeOctoSourceURL(source.URL); valid != "" {
				if isPinnedOctoGitHubLineURL(valid) {
					allowed[valid] = true
				}
			}
		}
	}
	answer = octoMarkdownGitHubLinkRE.ReplaceAllStringFunc(answer, func(link string) string {
		m := octoMarkdownGitHubLinkRE.FindStringSubmatch(link)
		if len(m) != 3 {
			return link
		}
		candidate := strings.Trim(m[2], "<>")
		if !isOctoGitHubLineURL(candidate) || octoObservedGitHubLineURL(candidate, allowed) {
			return link
		}
		return m[1] + "（源码行号未核验）"
	})
	return octoBareGitHubURLRE.ReplaceAllStringFunc(answer, func(raw string) string {
		candidate := strings.TrimRight(raw, ".,;:!?。，；：！？\"'`")
		suffix := raw[len(candidate):]
		if !isOctoGitHubLineURL(candidate) || octoObservedGitHubLineURL(candidate, allowed) {
			return raw
		}
		return "（未经核验的源码行号链接已省略）" + suffix
	})
}

func octoGitHubLineRange(fragment string) (start, end int, ok bool) {
	if !octoGitHubLineFragmentRE.MatchString(fragment) {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(fragment, "L"), "-L", 2)
	start, err := strconv.Atoi(parts[0])
	if err != nil || start < 1 {
		return 0, 0, false
	}
	end = start
	if len(parts) == 2 {
		end, err = strconv.Atoi(parts[1])
		if err != nil || end < start {
			return 0, 0, false
		}
	}
	return start, end, true
}

func octoObservedGitHubLineURL(candidate string, observed map[string]bool) bool {
	candidate = safeOctoSourceURL(candidate)
	if candidate == "" || !isPinnedOctoGitHubLineURL(candidate) {
		return false
	}
	if observed[candidate] {
		return true
	}
	parsed, _ := url.Parse(candidate)
	start, end, ok := octoGitHubLineRange(parsed.Fragment)
	if !ok || end-start+1 > maxOctoObservedLineSubset {
		return false
	}
	for read := range observed {
		readURL, err := url.Parse(read)
		if err != nil || !strings.EqualFold(readURL.Host, parsed.Host) || readURL.EscapedPath() != parsed.EscapedPath() {
			continue
		}
		readStart, readEnd, ok := octoGitHubLineRange(readURL.Fragment)
		if ok && start >= readStart && end <= readEnd {
			return true
		}
	}
	return false
}

func isOctoGitHubLineURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") || !octoGitHubLineFragmentRE.MatchString(parsed.Fragment) {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return len(parts) >= 5 && parts[2] == "blob"
}

func isPinnedOctoGitHubLineURL(raw string) bool {
	if !isOctoGitHubLineURL(raw) {
		return false
	}
	parsed, _ := url.Parse(raw)
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return octoGitCommitRE.MatchString(parts[3])
}

func octoCitationAttrs(raw string) map[string]string {
	attrs := map[string]string{}
	for _, m := range octoSourceAttrRE.FindAllStringSubmatch(raw, -1) {
		value := m[2]
		if value == "" {
			value = m[3]
		}
		attrs[strings.ToLower(m[1])] = html.UnescapeString(value)
	}
	return attrs
}
func octoAllowedKB(p octobusiness.Principal, id string) bool {
	for _, kb := range p.KnowledgeBaseIDs {
		if kb == id {
			return true
		}
	}
	return false
}
func uniqueCitedKnowledge(refs []*types.SearchResult, chunk, kb, title string) *types.SearchResult {
	byID := map[string]*types.SearchResult{}
	for _, ref := range refs {
		if ref == nil || ref.KnowledgeID == "" || ref.KnowledgeBaseID == "" || kb != "" && kb != ref.KnowledgeBaseID {
			continue
		}
		match := false
		if chunk != "" {
			match = ref.ID == chunk || ref.KnowledgeID == chunk
		} else if title != "" {
			match = strings.EqualFold(strings.TrimSpace(ref.KnowledgeTitle), strings.TrimSpace(title)) || strings.EqualFold(strings.TrimSpace(ref.KnowledgeFilename), strings.TrimSpace(title))
		}
		if match {
			byID[ref.KnowledgeID] = ref
		}
	}
	if len(byID) != 1 {
		return nil
	}
	for _, ref := range byID {
		return ref
	}
	return nil
}

// safeOctoSourceURL never renders relative file paths, embedded credentials,
// private-network endpoints, or signed/token query strings as public citations.
func safeOctoSourceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Port() != "" {
		return ""
	}
	// A public-looking URL can still carry a secret in its path or fragment.
	// net/url has decoded percent escapes in Path and Fragment by this point.
	location := strings.ToLower(parsed.Path + "#" + parsed.Fragment)
	if octoURLCredentialRE.MatchString(location) || strings.Contains(location, "token=") ||
		strings.Contains(location, "api_key=") || strings.Contains(location, "apikey=") ||
		strings.Contains(location, "secret=") || strings.Contains(location, "bearer ") {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return ""
	}
	if strings.ContainsAny(raw, "\r\n\t<>") {
		return ""
	}
	return parsed.String()
}
func persistedOctoSource(k *types.Knowledge) string {
	// Native manual metadata also contains numeric version fields. Read only
	// the provenance fields instead of requiring every metadata value to be text.
	var metadata struct {
		GitHubURL    string `json:"github_url"`
		GitHubCommit string `json:"github_commit"`
	}
	if len(k.Metadata) > 0 && json.Unmarshal(k.Metadata, &metadata) != nil {
		return ""
	}
	candidate := metadata.GitHubURL
	if candidate == "" {
		candidate = k.Source
	}
	candidate = safeOctoSourceURL(candidate)
	if candidate == "" {
		return ""
	}
	if isOctoGitHubLineURL(candidate) && !isPinnedOctoGitHubLineURL(candidate) {
		return ""
	}
	if commit := metadata.GitHubCommit; commit != "" {
		parsed, _ := url.Parse(candidate)
		if !octoGitCommitRE.MatchString(commit) || !strings.EqualFold(parsed.Hostname(), "github.com") || !strings.Contains(parsed.Path, "/blob/"+commit+"/") {
			return ""
		}
	}
	return candidate
}
func markdownSourceTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;").Replace(title)
}

func safeOctoSourceTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	lower := strings.ToLower(title)
	// A persisted title can be operator-supplied. Never surface a local path or
	// a credential-shaped title as an IM citation label.
	if title == "" || strings.HasPrefix(title, "/") || strings.HasPrefix(title, "~/") ||
		strings.HasPrefix(title, "./") || strings.HasPrefix(title, "../") ||
		octoWindowsSlashPathRE.MatchString(title) || octoEmbeddedAbsolutePathRE.MatchString(title) ||
		strings.Contains(title, "\\") ||
		strings.Contains(title, "://") || strings.Contains(lower, "token=") ||
		strings.Contains(lower, "api_key=") || strings.Contains(lower, "secret=") ||
		strings.Contains(lower, "bearer ") || octoBareCredentialRE.MatchString(title) {
		return "知识库资料"
	}
	if runes := []rune(title); len(runes) > 64 {
		return string(runes[:64]) + "…"
	}
	return title
}

func inlineOctoSource(source octoCitedSource) string {
	title := markdownSourceTitle(safeOctoSourceTitle(source.title))
	if source.url == "" {
		return "（来源：" + title + "）"
	}
	return " [来源：" + title + "](<" + source.url + ">)"
}

// appendOctoSources is invoked before the ordinary IM citation-tag stripping.
// It links only explicit citations, never all search hits. Titles alone are
// resolved only in an explicit source section or Chinese book-title marks,
// against actual retrieved references and only when unambiguous.
func (s *Service) appendOctoSources(ctx context.Context, answer string, refs []*types.SearchResult) string {
	if _, ok := octobusiness.PrincipalFromContext(ctx); !ok {
		return answer
	}
	p, err := octobusiness.ValidatedPrincipal(ctx)
	if err != nil {
		return answer
	}
	answer = sanitizeOctoGitHubLineLinks(ctx, answer)
	if !octobusiness.CitationRenderingEnabled(ctx) {
		return stripIMCitationTags(answer)
	}
	cited := map[string]*types.SearchResult{}
	ordered := []string{}
	addRef := func(ref *types.SearchResult) {
		if ref != nil && octoAllowedKB(p, ref.KnowledgeBaseID) && cited[ref.KnowledgeID] == nil {
			cited[ref.KnowledgeID] = ref
			ordered = append(ordered, ref.KnowledgeID)
		}
	}
	rawSources := map[string]octobusiness.SourceCitation{}
	officialReleaseSources := []octoCitedSource{}
	for _, source := range octobusiness.SourceCitations(ctx) {
		if octoAllowedKB(p, source.KnowledgeBaseID) {
			if valid := safeOctoSourceURL(source.URL); valid != "" {
				if isOctoGitHubLineURL(valid) && !isPinnedOctoGitHubLineURL(valid) {
					continue
				}
				rawSources[valid] = source
				// A github_release_lookup citation is not a model-generated web
				// tag: it comes from the system-owned latest-release preflight for
				// this exact turn. Render it deterministically so a good factual
				// answer cannot lose its official source merely because the model
				// omitted a <ref/> handle. Ordinary source_browse reads keep the
				// existing explicit-tag rule below.
				if source.OfficialRelease && octobusiness.CitationRenderingEnabled(ctx) && strings.TrimSpace(source.Title) != "" {
					officialReleaseSources = append(officialReleaseSources, octoCitedSource{title: source.Title, url: valid})
				}
			}
		}
	}
	sort.Slice(officialReleaseSources, func(i, j int) bool {
		if officialReleaseSources[i].title != officialReleaseSources[j].title {
			return officialReleaseSources[i].title < officialReleaseSources[j].title
		}
		return officialReleaseSources[i].url < officialReleaseSources[j].url
	})
	sources := append([]octoCitedSource(nil), officialReleaseSources...)
	for _, tag := range octoSourceTagRE.FindAllStringSubmatch(answer, -1) {
		attrs := octoCitationAttrs(tag[2])
		if tag[1] == "web" {
			valid := safeOctoSourceURL(attrs["url"])
			if source, ok := rawSources[valid]; ok {
				title := source.Title
				if title == "" {
					title = source.Path
				}
				sources = append(sources, octoCitedSource{title: title, url: valid})
			}
			continue
		}
		chunk := attrs["chunk_id"]
		kb := attrs["kb_id"]
		addRef(uniqueCitedKnowledge(refs, chunk, kb, attrs["doc"]))
	}
	// A plain document title is evidence of use only under an explicit source
	// heading. Generic title mentions elsewhere in the prose do not count.
	if heading := octoSourceHeadingRE.FindStringIndex(answer); heading != nil {
		section := answer[heading[0]:]
		for _, ref := range refs {
			if ref == nil {
				continue
			}
			title := strings.TrimSpace(ref.KnowledgeTitle)
			if len([]rune(title)) >= 4 && strings.Contains(section, title) {
				addRef(uniqueCitedKnowledge(refs, "", "", title))
			}
		}
	}
	// An exact, explicitly named work may be cited inline rather than under a
	// heading. Never look up guessed titles beyond this turn's retrieved refs.
	for _, quoted := range octoQuotedSourceTitleRE.FindAllStringSubmatch(answer, -1) {
		addRef(uniqueCitedKnowledge(refs, "", "", quoted[1]))
	}
	resolvedKnowledge := map[string]octoCitedSource{}
	for _, id := range ordered {
		ref := cited[id]
		if s.knowledgeService == nil {
			continue
		}
		knowledge, e := s.knowledgeService.GetKnowledgeByID(ctx, id)
		if e != nil || knowledge == nil || knowledge.ID != id || knowledge.TenantID != p.TenantID || knowledge.KnowledgeBaseID != ref.KnowledgeBaseID || !octoAllowedKB(p, knowledge.KnowledgeBaseID) || !types.IsPublishedKnowledgeForAnswer(knowledge) {
			continue
		}
		title := knowledge.Title
		if title == "" {
			title = knowledge.FileName
		}
		if title == "" {
			continue
		}
		source := octoCitedSource{title: title, url: persistedOctoSource(knowledge)}
		sources = append(sources, source)
		resolvedKnowledge[id] = source
	}
	// Render only the same explicitly cited, current-turn sources that passed
	// the tail-list checks above. Invalid tags disappear, including references
	// to draft, other-tenant, other-KB, or otherwise unobserved material.
	answer = octoSourceTagRE.ReplaceAllStringFunc(answer, func(tag string) string {
		match := octoSourceTagRE.FindStringSubmatch(tag)
		if len(match) != 3 {
			return ""
		}
		attrs := octoCitationAttrs(match[2])
		if match[1] == "web" {
			valid := safeOctoSourceURL(attrs["url"])
			source, ok := rawSources[valid]
			if !ok {
				return ""
			}
			title := source.Title
			if title == "" {
				title = source.Path
			}
			return inlineOctoSource(octoCitedSource{title: title, url: valid})
		}
		ref := uniqueCitedKnowledge(refs, attrs["chunk_id"], attrs["kb_id"], attrs["doc"])
		if ref == nil {
			return ""
		}
		if source, ok := resolvedKnowledge[ref.KnowledgeID]; ok {
			return inlineOctoSource(source)
		}
		return ""
	})
	seen := map[string]bool{}
	lines := []string{}
	visibleAnswer := answer
	for _, source := range sources {
		key := source.url
		if key == "" {
			key = "title:" + source.title
		}
		if seen[key] || source.url != "" && strings.Contains(visibleAnswer, source.url) {
			continue
		}
		seen[key] = true
		title := markdownSourceTitle(safeOctoSourceTitle(source.title))
		if source.url != "" {
			lines = append(lines, "- ["+title+"](<"+source.url+">)")
		} else {
			// A title mentioned in prose is not a source entry. Keep an
			// explicit, human-readable location even without a public URL.
			lines = append(lines, "- "+title+"（知识库资料，未配置公开来源链接）")
		}
		if len(lines) >= 12 {
			break
		}
	}
	if len(lines) == 0 {
		return answer
	}
	return strings.TrimSpace(answer) + "\n\n来源：\n" + strings.Join(lines, "\n")
}
