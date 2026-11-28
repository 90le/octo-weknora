package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type fakeKBResourceReader struct {
	fn func(ctx context.Context, ref string, stage bool) (*KnowledgeResourceReadResult, error)
}

func (f *fakeKBResourceReader) ReadKnowledgeResource(
	ctx context.Context, ref string, stage bool,
) (*KnowledgeResourceReadResult, error) {
	return f.fn(ctx, ref, stage)
}

func makeTinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 37, G: 99, B: 235, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestReadFileKnowledgeResourceImageVisionAndStage(t *testing.T) {
	pngBytes := makeTinyPNG(t)
	var gotStage bool
	tool := NewReadFileTool(nil).WithKnowledgeResources(&fakeKBResourceReader{
		fn: func(_ context.Context, ref string, stage bool) (*KnowledgeResourceReadResult, error) {
			gotStage = stage
			return &KnowledgeResourceReadResult{
				Handle:        "AbCdEfGhIjKlMnOpQrStUv",
				ResourceRef:   ref,
				Kind:          "image",
				Relation:      types.ResourceRelationExtractedImage,
				MimeType:      "image/png",
				OriginalName:  "arch.png",
				DocumentTitle: "Media Studio 全景介绍",
				KnowledgeID:   "k-1",
				Size:          int64(len(pngBytes)),
				Data:          pngBytes,
				StagedPath:    "/workspace/input/kb/AbCdEfGhIjKlMnOpQrStUv.png",
			}, nil
		},
	})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"resource://AbCdEfGhIjKlMnOpQrStUv","detail":"full","focus":"架构图模块"}`))
	require.NoError(t, err)
	require.True(t, res.Success)
	require.False(t, gotStage)
	require.Len(t, res.Images, 1)
	require.Equal(t, "full", res.ImageDetail)
	require.Equal(t, "架构图模块", res.ImageFocus)
	require.Contains(t, res.Output, "/workspace/input/kb/AbCdEfGhIjKlMnOpQrStUv.png")
	require.Contains(t, res.Output, "Media Studio 全景介绍")
	require.Equal(t, "/workspace/input/kb/AbCdEfGhIjKlMnOpQrStUv.png", res.Data["sandbox_path"])
}

func TestReadFileKnowledgeResourceSVGAndOversizedAndText(t *testing.T) {
	t.Run("svg staged without vision failure", func(t *testing.T) {
		svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"></svg>`)
		tool := NewReadFileTool(nil).WithKnowledgeResources(&fakeKBResourceReader{
			fn: func(_ context.Context, ref string, _ bool) (*KnowledgeResourceReadResult, error) {
				return &KnowledgeResourceReadResult{
					Handle:       "SvgHandle1234567890123",
					ResourceRef:  ref,
					Kind:         "image",
					Relation:     types.ResourceRelationExtractedImage,
					MimeType:     "image/svg+xml",
					OriginalName: "flow.svg",
					Size:         int64(len(svg)),
					Data:         svg,
					StagedPath:   "/workspace/input/kb/SvgHandle1234567890123.svg",
				}, nil
			},
		})
		res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"resource://SvgHandle1234567890123"}`))
		require.NoError(t, err)
		require.True(t, res.Success)
		require.Empty(t, res.Images)
		require.Contains(t, res.Output, "Vector SVG image")
		require.Contains(t, res.Output, "/workspace/input/kb/SvgHandle1234567890123.svg")
	})

	t.Run("oversized pptx returns guidance instead of error", func(t *testing.T) {
		tool := NewReadFileTool(nil).WithKnowledgeResources(&fakeKBResourceReader{
			fn: func(_ context.Context, ref string, _ bool) (*KnowledgeResourceReadResult, error) {
				return &KnowledgeResourceReadResult{
					Handle:        "BigDeckHandle123456789",
					ResourceRef:   ref,
					Kind:          "file",
					Relation:      types.ResourceRelationSourceFile,
					OriginalName:  "2025方案.pptx",
					DocumentTitle: "2025方案",
					Size:          80 << 20,
					TooLarge:      true,
					LimitBytes:    15 << 20,
				}, nil
			},
		})
		res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"resource://BigDeckHandle123456789"}`))
		require.NoError(t, err)
		require.True(t, res.Success)
		require.Contains(t, res.Output, "exceeds the")
		require.Contains(t, res.Output, "read_document")
	})

	t.Run("csv source file staged and previewed as text", func(t *testing.T) {
		csv := []byte("product,tier,price\nMedia Studio,Pro,100000\nSocial Studio,Pro,80000\n")
		tool := NewReadFileTool(nil).WithKnowledgeResources(&fakeKBResourceReader{
			fn: func(_ context.Context, ref string, _ bool) (*KnowledgeResourceReadResult, error) {
				return &KnowledgeResourceReadResult{
					Handle:        "CsvHandle1234567890123",
					ResourceRef:   ref,
					Kind:          "file",
					Relation:      types.ResourceRelationSourceFile,
					MimeType:      "text/csv",
					OriginalName:  "pricing.csv",
					DocumentTitle: "报价表",
					Size:          int64(len(csv)),
					Data:          csv,
					StagedPath:    "/workspace/input/kb/CsvHandle1234567890123_pricing.csv",
				}, nil
			},
		})
		res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"resource://CsvHandle1234567890123"}`))
		require.NoError(t, err)
		require.True(t, res.Success)
		require.Contains(t, res.Output, "/workspace/input/kb/CsvHandle1234567890123_pricing.csv")
		require.Contains(t, res.Output, "Media Studio,Pro,100000")
	})

	t.Run("unauthorized error propagates as failed tool result", func(t *testing.T) {
		tool := NewReadFileTool(nil).WithKnowledgeResources(&fakeKBResourceReader{
			fn: func(_ context.Context, _ string, _ bool) (*KnowledgeResourceReadResult, error) {
				return nil, errors.New("resource is not within the current authorized knowledge scope")
			},
		})
		res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"resource://DeniedHandle1234567890"}`))
		require.NoError(t, err)
		require.False(t, res.Success)
		require.Contains(t, res.Error, "authorized knowledge scope")
	})
}
