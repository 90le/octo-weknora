package im

import (
	"context"
	"encoding/json"
	"html"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
)

var octoSourceTagRE = regexp.MustCompile(`<(kb|web)\b([^>]*)/?>`)
var octoSourceAttrRE = regexp.MustCompile(`([A-Za-z_]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
var octoSourceHeadingRE = regexp.MustCompile(`(?im)^\s*(?:\*\*)?(?:实际来源|来源|参考资料|参考|资料依据|sources?|references?)\s*(?:\*\*)?\s*[:：]\s*(?:\*\*)?`)
var octoQuotedSourceTitleRE = regexp.MustCompile(`《([^《》\r\n]{1,400})》`)
var octoGitCommitRE = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type octoCitedSource struct{ title, url string }

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
			if source, ok := rawSources[safeOctoSourceURL(attrs["url"])]; ok {
				sources = append(sources, octoCitedSource{title: source.Path, url: source.URL})
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
		sources = append(sources, octoCitedSource{title: title, url: persistedOctoSource(knowledge)})
	}
	seen := map[string]bool{}
	lines := []string{}
	visibleAnswer := stripIMCitationTags(answer)
	for _, source := range sources {
		key := source.url
		if key == "" {
			key = "title:" + source.title
		}
		if seen[key] || source.url != "" && strings.Contains(visibleAnswer, source.url) {
			continue
		}
		seen[key] = true
		title := markdownSourceTitle(source.title)
		if source.url != "" {
			lines = append(lines, "- ["+title+"](<"+source.url+">)")
		} else if !strings.Contains(visibleAnswer, source.title) {
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
