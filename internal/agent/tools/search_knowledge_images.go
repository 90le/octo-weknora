package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/searchutil"
	"github.com/Tencent/WeKnora/internal/types"
)

// Asset type filters accepted by search_knowledge.
const (
	SearchAssetTypeAll   = "all"
	SearchAssetTypeImage = "image"

	// Images smaller than 15 KiB with little/no OCR text are typically logos,
	// dividers, or bullet icons rather than substantive charts or screenshots.
	minPreferImageAssetBytes = 15 * 1024
	minSubstantialOCRRunes   = 30
	maxImageGalleryKeywords  = 3
)

// ImageResourceMeta describes lightweight storage metadata for a KB image resource.
type ImageResourceMeta struct {
	Handle       string
	Size         int64
	MimeType     string
	OriginalName string
}

// ImageResourceMetaLookup fetches storage size/MIME metadata for resource://<handle>
// references in batch so asset_type="image" search can deprioritize tiny icons/logos
// without reading object bytes from storage.
type ImageResourceMetaLookup interface {
	LookupImageResourceMeta(ctx context.Context, handles []string) (map[string]ImageResourceMeta, error)
}

// WithImageResourceMetaLookup attaches optional resource metadata enrichment
// for search_knowledge(asset_type="image").
func (t *SearchKnowledgeTool) WithImageResourceMetaLookup(lookup ImageResourceMetaLookup) *SearchKnowledgeTool {
	t.imageMetaLookup = lookup
	return t
}

func normalizeSearchAssetType(assetType string) (string, error) {
	assetType = strings.ToLower(strings.TrimSpace(assetType))
	switch assetType {
	case "", SearchAssetTypeAll:
		return SearchAssetTypeAll, nil
	case SearchAssetTypeImage:
		return SearchAssetTypeImage, nil
	default:
		return "", fmt.Errorf("asset_type must be one of all, image (got %q)", assetType)
	}
}

type imageAssetCandidate struct {
	URL             string
	Handle          string
	Caption         string
	OCRText         string
	ChunkID         string
	ChunkIndex      int
	ChunkType       string
	KnowledgeID     string
	KnowledgeBaseID string
	KnowledgeTitle  string
	KnowledgeMeta   string
	Score           float64
	MatchType       types.MatchType
	SourceQuery     string
	QueryType       string
	SizeBytes       int64
	MimeType        string
	OriginalName    string
	SmallIcon       bool
	SearchResult    *types.SearchResult
}

func (t *SearchKnowledgeTool) executeImageAssetSearch(
	ctx context.Context,
	query, mode string,
	limit int,
	searchTargets types.SearchTargets,
	kbModes map[string]kbSearchMode,
	kbList []*types.KnowledgeBase,
	kbTypeMap map[string]string,
) (*types.ToolResult, error) {
	kbIDs := searchTargets.GetAllKnowledgeBaseIDs()
	retrievalLimit := min(searchKnowledgeMaxLimit, max(limit*2, 20))
	topK, vectorThreshold, keywordThreshold := t.retrievalParams(retrievalLimit)

	logKnowledgeSearchQueries(ctx, []string{query})
	logger.Infof(ctx,
		"[Tool][SearchKnowledge] asset_type=image mode=%s limit=%d top_k=%d targets=%d kbs=%d",
		mode, limit, topK, len(searchTargets), len(kbIDs))

	// Path 1: Existing vector / keyword / hybrid search across authorized targets.
	allResults, searchFailures, searchCalls, embedFallbacks := t.concurrentSearchByTargets(
		ctx, query, mode, kbModes, searchTargets, kbList, topK, vectorThreshold, keywordThreshold, kbTypeMap,
	)
	for kbID, reason := range embedFallbacks {
		kbModes[kbID] = kbSearchMode{mode: SearchModeKeyword, fallback: true, reason: reason}
	}

	// Enrich image info on retrieved chunks so parent text hits expose their child images.
	if t.chunkService != nil && len(allResults) > 0 {
		byTenant := make(map[uint64][]*types.SearchResult)
		for _, r := range allResults {
			if r == nil || r.SearchResult == nil {
				continue
			}
			tid := searchTargets.GetTenantIDForKB(r.KnowledgeBaseID)
			if tid == 0 {
				continue
			}
			byTenant[tid] = append(byTenant[tid], r.SearchResult)
		}
		for tid, batch := range byTenant {
			searchutil.EnrichSearchResultsImageInfo(ctx, t.chunkService.GetRepository(), tid, batch)
		}
	}

	candidates := make(map[string]*imageAssetCandidate)
	mergeCandidate := func(c *imageAssetCandidate) {
		if c == nil || strings.TrimSpace(c.URL) == "" {
			return
		}
		key := strings.TrimSpace(c.URL)
		existing, ok := candidates[key]
		if !ok {
			candidates[key] = c
			return
		}
		if c.Score > existing.Score {
			existing.Score = c.Score
			if c.ChunkID != "" {
				existing.ChunkID = c.ChunkID
			}
			if c.ChunkType != "" {
				existing.ChunkType = c.ChunkType
			}
			if c.SearchResult != nil {
				existing.SearchResult = c.SearchResult
			}
		}
		if len(strings.TrimSpace(c.Caption)) > len(strings.TrimSpace(existing.Caption)) {
			existing.Caption = strings.TrimSpace(c.Caption)
		}
		if len(strings.TrimSpace(c.OCRText)) > len(strings.TrimSpace(existing.OCRText)) {
			existing.OCRText = strings.TrimSpace(c.OCRText)
		}
		if existing.KnowledgeTitle == "" && c.KnowledgeTitle != "" {
			existing.KnowledgeTitle = c.KnowledgeTitle
		}
		if existing.KnowledgeID == "" && c.KnowledgeID != "" {
			existing.KnowledgeID = c.KnowledgeID
		}
		if existing.KnowledgeBaseID == "" && c.KnowledgeBaseID != "" {
			existing.KnowledgeBaseID = c.KnowledgeBaseID
		}
	}

	for _, r := range allResults {
		if r == nil || r.SearchResult == nil || r.ImageInfo == "" {
			continue
		}
		var imgs []types.ImageInfo
		if err := json.Unmarshal([]byte(r.ImageInfo), &imgs); err != nil || len(imgs) == 0 {
			continue
		}
		baseScore := r.Score
		if r.ChunkType == string(types.ChunkTypeImageCaption) ||
			r.ChunkType == string(types.ChunkTypeImageOCR) ||
			r.ChunkType == string(types.ChunkTypeImageVector) {
			baseScore += 0.08
		}
		for _, img := range imgs {
			u := strings.TrimSpace(img.URL)
			if u == "" {
				continue
			}
			handle, _ := types.ParseResourcePath(u)
			mergeCandidate(&imageAssetCandidate{
				URL:             u,
				Handle:          handle,
				Caption:         strings.TrimSpace(img.Caption),
				OCRText:         strings.TrimSpace(img.OCRText),
				ChunkID:         r.ID,
				ChunkIndex:      r.ChunkIndex,
				ChunkType:       r.ChunkType,
				KnowledgeID:     r.KnowledgeID,
				KnowledgeBaseID: r.KnowledgeBaseID,
				KnowledgeTitle:  r.KnowledgeTitle,
				KnowledgeMeta:   r.KnowledgeCustomMetadata,
				Score:           baseScore,
				MatchType:       r.MatchType,
				SourceQuery:     query,
				QueryType:       r.QueryType,
				SearchResult:    r.SearchResult,
			})
		}
	}

	// Path 2: Direct authorized chunk_images gallery search (caption + ocr_text) so
	// exact product names, chart labels, or OCR metrics surface even when text chunks
	// outrank image chunks in global top-K.
	t.collectGalleryImageCandidates(ctx, query, searchTargets, mergeCandidate)

	if len(candidates) == 0 && searchCalls > 0 && len(searchFailures) >= searchCalls {
		msg := "Knowledge image asset search failed: " + strings.Join(searchFailures, "; ") +
			". The knowledge bases were not searched, so this is not evidence that they lack visual assets."
		return &types.ToolResult{Success: false, Output: msg, Error: msg}, nil
	}

	// Enrich file size & MIME metadata from stored resources in one batch query.
	handles := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c.Handle != "" {
			handles = append(handles, c.Handle)
		}
	}
	var metaByHandle map[string]ImageResourceMeta
	if t.imageMetaLookup != nil && len(handles) > 0 {
		if loaded, err := t.imageMetaLookup.LookupImageResourceMeta(ctx, dedupNonEmptyStrings(handles)); err == nil {
			metaByHandle = loaded
		} else {
			logger.Warnf(ctx, "[Tool][SearchKnowledge] Image resource meta lookup failed: %v", err)
		}
	}

	nonIcons := make([]*imageAssetCandidate, 0, len(candidates))
	smallIcons := make([]*imageAssetCandidate, 0)
	for _, c := range candidates {
		if meta, ok := metaByHandle[c.Handle]; ok {
			c.SizeBytes = meta.Size
			c.MimeType = meta.MimeType
			c.OriginalName = meta.OriginalName
		}
		ocrRunes := utf8.RuneCountInString(c.OCRText)
		captionRunes := utf8.RuneCountInString(c.Caption)
		c.SmallIcon = c.SizeBytes > 0 && c.SizeBytes < minPreferImageAssetBytes && ocrRunes < minSubstantialOCRRunes

		// Boost substantive visual assets (rich OCR / high-res screenshots / diagrams)
		// and deprioritize tiny decorative logos/icons.
		if ocrRunes >= minSubstantialOCRRunes {
			c.Score += 0.12
		}
		if captionRunes >= 20 {
			c.Score += 0.05
		}
		if c.SizeBytes >= 30*1024 {
			c.Score += 0.08
		}
		if c.SmallIcon {
			c.Score -= 0.35
			smallIcons = append(smallIcons, c)
		} else {
			nonIcons = append(nonIcons, c)
		}
	}

	sortCandidates := func(list []*imageAssetCandidate) {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Score != list[j].Score {
				return list[i].Score > list[j].Score
			}
			if list[i].SizeBytes != list[j].SizeBytes {
				return list[i].SizeBytes > list[j].SizeBytes
			}
			return list[i].URL < list[j].URL
		})
	}
	sortCandidates(nonIcons)
	sortCandidates(smallIcons)

	filteredSmallIcons := 0
	selected := nonIcons
	if len(selected) == 0 {
		// Fall back to small icons only if nothing else matched.
		selected = smallIcons
	} else {
		filteredSmallIcons = len(smallIcons)
	}
	if len(selected) > limit {
		selected = selected[:limit]
	}

	result := t.formatImageAssetOutput(ctx, selected, kbIDs, query, mode, filteredSmallIcons)
	if len(searchFailures) > 0 {
		result.Data["partial_failures"] = searchFailures
	}
	annotateModeFallback(result.Data, mode, kbModes)
	if len(selected) == 0 {
		result.Output = fmt.Sprintf(
			"No matching visual/OCR image assets found for %q (asset_type=image, mode=%s) in %d knowledge base(s). "+
				"Try shorter subject keywords or search text chunks with asset_type=\"all\".",
			query, mode, len(kbIDs),
		)
	}
	return result, nil
}

func (t *SearchKnowledgeTool) collectGalleryImageCandidates(
	ctx context.Context,
	query string,
	searchTargets types.SearchTargets,
	merge func(*imageAssetCandidate),
) {
	if t.chunkService == nil || t.knowledgeService == nil {
		return
	}
	chunkRepo := t.chunkService.GetRepository()
	if chunkRepo == nil {
		return
	}

	keywords := extractImageSearchKeywords(query)
	if len(keywords) == 0 {
		return
	}
	enabled := true
	searchFields := []types.ImageAssetField{
		{Builtin: "caption"},
		{Builtin: "ocr_text"},
	}

	// Cache per-knowledge authorization & titles so each document is verified at most once.
	type docAuth struct {
		allowed bool
		title   string
		kbID    string
	}
	authCache := make(map[string]docAuth)
	authorizeDoc := func(tenantID uint64, knowledgeID string) docAuth {
		cacheKey := fmt.Sprintf("%d:%s", tenantID, knowledgeID)
		if cached, ok := authCache[cacheKey]; ok {
			return cached
		}
		ownerCtx := context.WithValue(ctx, types.TenantIDContextKey, tenantID)
		k, err := t.knowledgeService.GetKnowledgeByIDOnly(ownerCtx, knowledgeID)
		if err != nil || k == nil || k.TenantID != tenantID || strings.EqualFold(k.EnableStatus, "disabled") {
			authCache[cacheKey] = docAuth{allowed: false}
			return authCache[cacheKey]
		}
		if _, authErr := AuthorizeLoadedKnowledge(ownerCtx, searchTargets, k, t.knowledgeService); authErr != nil {
			authCache[cacheKey] = docAuth{allowed: false}
			return authCache[cacheKey]
		}
		res := docAuth{
			allowed: true,
			title:   strings.TrimSpace(k.Title),
			kbID:    k.KnowledgeBaseID,
		}
		authCache[cacheKey] = res
		return res
	}

	for _, target := range searchTargets {
		if target == nil || target.KnowledgeBaseID == "" || target.TenantID == 0 {
			continue
		}
		for kwIdx, kw := range keywords {
			rows, _, err := chunkRepo.ListImageAssets(ctx, target.TenantID, target.KnowledgeBaseID, &types.ImageAssetQuery{
				Keyword:      kw,
				SearchFields: searchFields,
				IsEnabled:    &enabled,
				KnowledgeIDs: target.KnowledgeIDs,
				SortField:    types.ImageAssetField{Builtin: "updated_at"},
				SortDesc:     true,
				Limit:        15,
			})
			if err != nil {
				logger.Warnf(ctx, "[Tool][SearchKnowledge] Gallery image lookup failed for KB %s: %v",
					target.KnowledgeBaseID, err)
				continue
			}
			// Higher base score for full-query match than sub-token match.
			baseScore := 0.72 - float64(kwIdx)*0.08
			for _, row := range rows {
				u := strings.TrimSpace(row.URL)
				if u == "" {
					continue
				}
				doc := authorizeDoc(target.TenantID, row.KnowledgeID)
				if !doc.allowed {
					continue
				}
				handle, _ := types.ParseResourcePath(u)
				score := baseScore
				lowerKW := strings.ToLower(kw)
				if strings.Contains(strings.ToLower(row.OCRText), lowerKW) {
					score += 0.06
				}
				if strings.Contains(strings.ToLower(row.Caption), lowerKW) {
					score += 0.04
				}
				merge(&imageAssetCandidate{
					URL:             u,
					Handle:          handle,
					Caption:         strings.TrimSpace(row.Caption),
					OCRText:         strings.TrimSpace(row.OCRText),
					ChunkID:         row.ChunkID,
					ChunkIndex:      row.ImageIndex,
					ChunkType:       row.ChunkType,
					KnowledgeID:     row.KnowledgeID,
					KnowledgeBaseID: target.KnowledgeBaseID,
					KnowledgeTitle:  doc.title,
					Score:           score,
					SourceQuery:     query,
					QueryType:       "image_gallery",
				})
			}
		}
	}
}

// extractImageSearchKeywords returns the trimmed query (when concise) plus up to
// maxImageGalleryKeywords salient terms for ILIKE matching against chunk_images.
func extractImageSearchKeywords(query string) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	add := func(s string) {
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) < 2 {
			return
		}
		key := strings.ToLower(s)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	if utf8.RuneCountInString(q) <= 24 {
		add(q)
	}
	tokens := strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("，。！？、；：（）()[]【】\"'“”‘’/\\|,+&", r)
	})
	// Sort tokens longest-first so domain terms beat filler words.
	sort.SliceStable(tokens, func(i, j int) bool {
		return utf8.RuneCountInString(tokens[i]) > utf8.RuneCountInString(tokens[j])
	})
	for _, tok := range tokens {
		if len(out) >= maxImageGalleryKeywords {
			break
		}
		add(tok)
	}
	if len(out) == 0 {
		add(q)
	}
	return out
}

func (t *SearchKnowledgeTool) formatImageAssetOutput(
	ctx context.Context,
	candidates []*imageAssetCandidate,
	kbsToSearch []string,
	query, mode string,
	filteredSmallIcons int,
) *types.ToolResult {
	data := map[string]interface{}{
		"knowledge_base_ids":   kbsToSearch,
		"query":                query,
		"queries":              []string{query},
		"mode":                 mode,
		"asset_type":           SearchAssetTypeImage,
		"filtered_small_icons": filteredSmallIcons,
		"results":              []interface{}{},
		"count":                len(candidates),
		"display_type":         "search_results",
	}
	if len(candidates) == 0 {
		return &types.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("No matching visual/OCR image assets found for %q.", query),
			Data:    data,
		}
	}

	kbCounts := make(map[string]int)
	var ob strings.Builder
	fmt.Fprintf(&ob, "<search_results count=\"%d\" mode=\"%s\" asset_type=\"image\">\n", len(candidates), xmlEscape(mode))
	fmt.Fprintf(&ob, "<query>%s</query>\n", xmlEscape(query))
	if filteredSmallIcons > 0 {
		fmt.Fprintf(&ob, "<note>Filtered %d small icon/logo image(s) (&lt;15KB without substantial OCR text).</note>\n",
			filteredSmallIcons)
	}

	formattedResults := make([]map[string]interface{}, 0, len(candidates))
	for i, c := range candidates {
		kbCounts[c.KnowledgeBaseID]++
		sizeHuman := ""
		if c.SizeBytes > 0 {
			sizeHuman = formatFileSize(c.SizeBytes)
		}
		var summaryParts []string
		summaryParts = append(summaryParts, "[Image Asset] "+c.URL)
		if sizeHuman != "" {
			summaryParts = append(summaryParts, "size="+sizeHuman)
		}
		if c.MimeType != "" {
			summaryParts = append(summaryParts, "mime="+c.MimeType)
		}
		if c.SmallIcon {
			summaryParts = append(summaryParts, "note=small_icon")
		}
		if c.OCRText != "" {
			summaryParts = append(summaryParts, "has_ocr=true")
		}
		summary := strings.Join(summaryParts, " | ")
		if c.Caption != "" {
			summary += "\nCaption: " + c.Caption
		}

		imgEntry := map[string]string{
			"url": c.URL,
		}
		if c.Caption != "" {
			imgEntry["caption"] = c.Caption
		}
		if c.OCRText != "" {
			imgEntry["ocr_text"] = c.OCRText
		}
		if c.SizeBytes > 0 {
			imgEntry["size_bytes"] = strconv.FormatInt(c.SizeBytes, 10)
			imgEntry["size_human"] = sizeHuman
		}
		if c.MimeType != "" {
			imgEntry["mime_type"] = c.MimeType
		}
		if c.OriginalName != "" {
			imgEntry["file_name"] = c.OriginalName
		}

		chunkID := c.ChunkID
		if chunkID == "" {
			chunkID = fmt.Sprintf("img_%d", i+1)
		}
		fmt.Fprintf(&ob, "<chunk rank=\"%d\" chunk_id=\"%s\" chunk_index=\"%d\" knowledge_id=\"%s\" knowledge_base_id=\"%s\" knowledge_title=\"%s\" score=\"%.3f\">\n",
			i+1, xmlEscape(chunkID), c.ChunkIndex, xmlEscape(c.KnowledgeID), xmlEscape(c.KnowledgeBaseID), xmlEscape(c.KnowledgeTitle), c.Score)
		fmt.Fprintf(&ob, "<content>%s</content>\n", xmlEscape(summary))
		if c.OCRText != "" {
			ocr := c.OCRText
			if runes := []rune(ocr); len(runes) > 1200 {
				ocr = string(runes[:1200]) + "..."
			}
			fmt.Fprintf(&ob, "<image_ocr>%s</image_ocr>\n", xmlEscape(ocr))
		}
		ob.WriteString("</chunk>\n")

		row := map[string]interface{}{
			"result_index":       i + 1,
			"chunk_id":           chunkID,
			"chunk_index":        c.ChunkIndex,
			"chunk_type":         "image_asset",
			"content":            summary,
			"knowledge_id":       c.KnowledgeID,
			"knowledge_base_id":  c.KnowledgeBaseID,
			"knowledge_title":    c.KnowledgeTitle,
			"knowledge_metadata": c.KnowledgeMeta,
			"score":              c.Score,
			"source_query":       query,
			"query_type":         c.QueryType,
			"images":             []map[string]string{imgEntry},
		}
		formattedResults = append(formattedResults, row)
	}
	ob.WriteString("</search_results>")

	data["results"] = formattedResults
	data["kb_counts"] = kbCounts
	result := &types.ToolResult{
		Success: true,
		Output:  ob.String(),
		Data:    data,
	}
	logKnowledgeSearchOutput(ctx, result)
	return result
}
