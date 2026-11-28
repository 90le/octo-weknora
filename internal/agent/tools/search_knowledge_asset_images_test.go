package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type fakeImageSearchKBSvc struct {
	interfaces.KnowledgeBaseService
	kbs     []*types.KnowledgeBase
	results []*types.SearchResult
}

func (f *fakeImageSearchKBSvc) GetKnowledgeBasesByIDsOnly(_ context.Context, _ []string) ([]*types.KnowledgeBase, error) {
	return f.kbs, nil
}

func (f *fakeImageSearchKBSvc) ResolveEmbeddingModelKeys(_ context.Context, kbs []*types.KnowledgeBase) map[string]string {
	out := make(map[string]string, len(kbs))
	for _, kb := range kbs {
		if kb != nil {
			out[kb.ID] = "emb-default"
		}
	}
	return out
}

func (f *fakeImageSearchKBSvc) GetQueryEmbedding(_ context.Context, _, _ string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (f *fakeImageSearchKBSvc) HybridSearch(_ context.Context, _ string, _ types.SearchParams) ([]*types.SearchResult, error) {
	return f.results, nil
}

type fakeImageSearchChunkRepo struct {
	interfaces.ChunkRepository
	rows []types.ImageAssetRow
}

func (f *fakeImageSearchChunkRepo) ListImageAssets(
	_ context.Context, _ uint64, _ string, _ *types.ImageAssetQuery,
) ([]types.ImageAssetRow, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

func (f *fakeImageSearchChunkRepo) ListChunksByParentIDs(
	_ context.Context, _ uint64, _ []string,
) ([]*types.Chunk, error) {
	return nil, nil
}

type fakeImageSearchChunkSvc struct {
	interfaces.ChunkService
	repo interfaces.ChunkRepository
}

func (f *fakeImageSearchChunkSvc) GetRepository() interfaces.ChunkRepository {
	return f.repo
}

type fakeImageSearchKnowledgeSvc struct {
	interfaces.KnowledgeService
	docs map[string]*types.Knowledge
}

func (f *fakeImageSearchKnowledgeSvc) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	return f.docs[id], nil
}

type fakeImageMetaLookup struct {
	meta map[string]ImageResourceMeta
}

func (f *fakeImageMetaLookup) LookupImageResourceMeta(
	_ context.Context, handles []string,
) (map[string]ImageResourceMeta, error) {
	out := make(map[string]ImageResourceMeta, len(handles))
	for _, h := range handles {
		if m, ok := f.meta[h]; ok {
			out[h] = m
		}
	}
	return out, nil
}

func TestSearchKnowledgeImageAssetsFiltersSmallLogosAndAuthorizesGallery(t *testing.T) {
	kb := kbWithIndexes("kb-1", true, true, "")
	imgJSON := func(u, cap, ocr string) string {
		b, _ := json.Marshal([]types.ImageInfo{{URL: u, Caption: cap, OCRText: ocr}})
		return string(b)
	}

	kbSvc := &fakeImageSearchKBSvc{
		kbs: []*types.KnowledgeBase{kb},
		results: []*types.SearchResult{
			{
				ID:              "c-logo",
				KnowledgeID:     "k-pub",
				KnowledgeBaseID: "kb-1",
				KnowledgeTitle:  "产品手册",
				ChunkType:       string(types.ChunkTypeImageCaption),
				Score:           0.85,
				ImageInfo:       imgJSON("resource://SmallLogoHandle1234567", "公司Logo图标", ""),
			},
			{
				ID:              "c-chart",
				KnowledgeID:     "k-pub",
				KnowledgeBaseID: "kb-1",
				KnowledgeTitle:  "行业报告",
				ChunkType:       string(types.ChunkTypeImageOCR),
				Score:           0.76,
				ImageInfo: imgJSON(
					"resource://ChartImageHandle123456",
					"2024-2026 社交营销市场规模趋势图",
					"2024年 320亿 2025年 410亿 2026E 525亿 CAGR 28.1% 来源：内部研究",
				),
			},
		},
	}

	chunkRepo := &fakeImageSearchChunkRepo{
		rows: []types.ImageAssetRow{
			{
				ChunkID:     "c-ui",
				KnowledgeID: "k-pub",
				ChunkType:   string(types.ChunkTypeImageCaption),
				URL:         "resource://ProductUIHandle1234567",
				Caption:     "Media Studio 媒介洞察工作台实时大盘截图",
				OCRText:     "投放ROI 3.4x 曝光量 1.2亿 预算分配优化建议",
			},
			{
				ChunkID:     "c-draft",
				KnowledgeID: "k-draft",
				ChunkType:   string(types.ChunkTypeImageCaption),
				URL:         "resource://DraftSecretHandle12345",
				Caption:     "未发布草稿价格截图",
				OCRText:     "内部草稿 不可外发",
			},
		},
	}

	kSvc := &fakeImageSearchKnowledgeSvc{
		docs: map[string]*types.Knowledge{
			"k-pub": {
				ID: "k-pub", TenantID: 10000, KnowledgeBaseID: "kb-1",
				Title: "产品手册", EnableStatus: "enabled", ParseStatus: "completed",
			},
			"k-draft": {
				ID: "k-draft", TenantID: 10000, KnowledgeBaseID: "kb-1",
				Title: "未发布草稿", EnableStatus: "disabled", ParseStatus: "completed",
			},
		},
	}

	targets := types.SearchTargets{
		{
			Type:            types.SearchTargetTypeKnowledgeBase,
			KnowledgeBaseID: "kb-1",
			TenantID:        10000,
		},
	}

	meta := &fakeImageMetaLookup{
		meta: map[string]ImageResourceMeta{
			"SmallLogoHandle1234567": {Handle: "SmallLogoHandle1234567", Size: 4200, MimeType: "image/png"},
			"ChartImageHandle123456": {Handle: "ChartImageHandle123456", Size: 184000, MimeType: "image/png"},
			"ProductUIHandle1234567": {Handle: "ProductUIHandle1234567", Size: 256000, MimeType: "image/png"},
			"DraftSecretHandle12345": {Handle: "DraftSecretHandle12345", Size: 210000, MimeType: "image/png"},
		},
	}

	tool := NewSearchKnowledgeTool(kbSvc, kSvc, &fakeImageSearchChunkSvc{repo: chunkRepo}, targets, nil, nil).
		WithImageResourceMetaLookup(meta)

	imCtx := types.WithIMKnowledgeScope(context.Background(), []string{"kb-1"})
	res, err := tool.Execute(imCtx, json.RawMessage(`{"query":"市场规模趋势图 Media Studio","asset_type":"image","limit":5}`))
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, SearchAssetTypeImage, res.Data["asset_type"])
	require.Equal(t, 1, res.Data["filtered_small_icons"])
	// Small logo filtered out; unauthorized draft doc blocked by ScopeTagIDs!
	require.NotContains(t, res.Output, "SmallLogoHandle1234567")
	require.NotContains(t, res.Output, "DraftSecretHandle12345")
	require.Contains(t, res.Output, "ChartImageHandle123456")
	require.Contains(t, res.Output, "ProductUIHandle1234567")
	require.Contains(t, res.Output, "CAGR 28.1%")
}

func TestNormalizeSearchAssetType(t *testing.T) {
	got, err := normalizeSearchAssetType("")
	require.NoError(t, err)
	require.Equal(t, SearchAssetTypeAll, got)

	got, err = normalizeSearchAssetType("IMAGE")
	require.NoError(t, err)
	require.Equal(t, SearchAssetTypeImage, got)

	_, err = normalizeSearchAssetType("video")
	require.Error(t, err)
}
