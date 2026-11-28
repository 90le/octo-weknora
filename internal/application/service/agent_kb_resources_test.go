package service

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"io"
	"testing"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/tiff"
)

type fakeKBResLookup struct {
	resources map[string]*types.StoredResource
	owners    map[string][]kbResourceOwner
}

func (f *fakeKBResLookup) GetActiveResourceByHandle(_ context.Context, handle string) (*types.StoredResource, error) {
	return f.resources[handle], nil
}

func (f *fakeKBResLookup) ListKnowledgeOwners(
	_ context.Context, _ uint64, resourceID, _ string,
) ([]kbResourceOwner, error) {
	return f.owners[resourceID], nil
}

type fakeKBResKnowledgeSvc struct {
	interfaces.KnowledgeService
	docs map[string]*types.Knowledge
}

func (f *fakeKBResKnowledgeSvc) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	return f.docs[id], nil
}

type fakeKBResFileSvc struct {
	interfaces.FileService
	files map[string][]byte
}

func (f *fakeKBResFileSvc) GetFile(_ context.Context, filePath string) (io.ReadCloser, error) {
	data, ok := f.files[filePath]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type fakeKBStageStore struct {
	sandbox.SessionFileStore
	written map[string][]byte
}

func (f *fakeKBStageStore) WriteSessionInputFile(_ context.Context, _, filePath string, content []byte) error {
	if f.written == nil {
		f.written = make(map[string][]byte)
	}
	f.written[filePath] = append([]byte(nil), content...)
	return nil
}

func makeTinyTIFF(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 6, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 6; x++ {
			img.Set(x, y, color.RGBA{R: 16, G: 185, B: 129, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, tiff.Encode(&buf, img, nil))
	return buf.Bytes()
}

func TestKBResourceReaderAuthorizationStagingAndQuota(t *testing.T) {
	const (
		handleImg   = "AbCdEfGhIjKlMnOpQrStUv"
		handleTIFF  = "TiffHandle123456789012"
		handleXLSX  = "XlsxHandle123456789012"
		handleBig   = "BigPptxHandle123456789"
		handleOther = "OtherKBHandle123456789"
		handleDraft = "DraftKBHandle123456789"
		handleMsg   = "MsgArtifact12345678901"
	)

	tiffBytes := makeTinyTIFF(t)
	lookup := &fakeKBResLookup{
		resources: map[string]*types.StoredResource{
			handleImg:   {ID: "r-img", Handle: handleImg, TenantID: 10000, Kind: "image", MimeType: "image/png", OriginalName: "ui.png", PhysicalPath: "local://ui.png", Size: 16},
			handleTIFF:  {ID: "r-tif", Handle: handleTIFF, TenantID: 10000, Kind: "image", MimeType: "image/tiff", OriginalName: "legacy.tiff", PhysicalPath: "local://legacy.tiff", Size: int64(len(tiffBytes))},
			handleXLSX:  {ID: "r-xls", Handle: handleXLSX, TenantID: 10000, Kind: "file", MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", OriginalName: "报价表 2026.xlsx", PhysicalPath: "local://quote.xlsx", Size: 12},
			handleBig:   {ID: "r-big", Handle: handleBig, TenantID: 10000, Kind: "file", MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", OriginalName: "huge.pptx", PhysicalPath: "local://huge.pptx", Size: 50 << 20},
			handleOther: {ID: "r-oth", Handle: handleOther, TenantID: 10000, Kind: "image", MimeType: "image/png", OriginalName: "secret.png", PhysicalPath: "local://secret.png", Size: 8},
			handleDraft: {ID: "r-drf", Handle: handleDraft, TenantID: 10000, Kind: "image", MimeType: "image/png", OriginalName: "draft.png", PhysicalPath: "local://draft.png", Size: 8},
			handleMsg:   {ID: "r-msg", Handle: handleMsg, TenantID: 10000, Kind: "file", MimeType: "application/zip", OriginalName: "skill.zip", PhysicalPath: "local://skill.zip", Size: 8},
		},
		owners: map[string][]kbResourceOwner{
			"r-img": {{KnowledgeID: "k-allowed", Relation: types.ResourceRelationExtractedImage}},
			"r-tif": {{KnowledgeID: "k-allowed", Relation: types.ResourceRelationExtractedImage}},
			"r-xls": {{KnowledgeID: "k-allowed", Relation: types.ResourceRelationSourceFile}},
			"r-big": {{KnowledgeID: "k-allowed", Relation: types.ResourceRelationSourceFile}},
			"r-oth": {{KnowledgeID: "k-denied-kb", Relation: types.ResourceRelationExtractedImage}},
			"r-drf": {{KnowledgeID: "k-draft", Relation: types.ResourceRelationExtractedImage}},
			// r-msg intentionally has no knowledge owner (e.g. message artifact or skill zip)
		},
	}

	kSvc := &fakeKBResKnowledgeSvc{
		docs: map[string]*types.Knowledge{
			"k-allowed": {
				ID: "k-allowed", TenantID: 10000, KnowledgeBaseID: "kb-allowed",
				Title: "Media Studio 介绍", EnableStatus: "enabled", ParseStatus: "completed",
			},
			"k-denied-kb": {
				ID: "k-denied-kb", TenantID: 10000, KnowledgeBaseID: "kb-secret",
				Title: "机密库", EnableStatus: "enabled", ParseStatus: "completed",
			},
			"k-draft": {
				ID: "k-draft", TenantID: 10000, KnowledgeBaseID: "kb-allowed",
				Title: "未发布草稿", EnableStatus: "disabled", ParseStatus: "completed",
			},
		},
	}

	fSvc := &fakeKBResFileSvc{
		files: map[string][]byte{
			"local://ui.png":      []byte("\x89PNG\r\n\x1a\n12345678"),
			"local://legacy.tiff": tiffBytes,
			"local://quote.xlsx":  []byte("PK\x03\x04xlsxdata"),
			"local://secret.png":  []byte("topsecret"),
			"local://draft.png":   []byte("draftonly"),
		},
	}

	store := &fakeKBStageStore{}
	reader := &kbResourceReader{
		lookup:           lookup,
		knowledgeService: kSvc,
		fileService:      fSvc,
		searchTargets: types.SearchTargets{
			{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-allowed", TenantID: 10000},
		},
		sessionID:    "sess-1",
		sandboxStore: store,
		inputDir:     sandbox.SessionInputRoot,
	}

	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(10000))

	// 1. Authorized PNG image stages cleanly to /workspace/input/kb/<handle>.png
	res, err := reader.ReadKnowledgeResource(ctx, "resource://"+handleImg, true)
	require.NoError(t, err)
	require.Equal(t, "/workspace/input/kb/"+handleImg+".png", res.StagedPath)
	require.Contains(t, store.written, "/workspace/input/kb/"+handleImg+".png")

	// 2. Authorized TIFF image auto-transcodes to PNG and stages as .png
	tifRes, err := reader.ReadKnowledgeResource(ctx, "resource://"+handleTIFF, true)
	require.NoError(t, err)
	require.Equal(t, "image/png", tifRes.MimeType)
	require.Equal(t, "/workspace/input/kb/"+handleTIFF+".png", tifRes.StagedPath)
	require.True(t, bytes.HasPrefix(tifRes.Data, []byte("\x89PNG")))

	// 3. Authorized lightweight .xlsx stages cleanly with sanitized name
	xlsRes, err := reader.ReadKnowledgeResource(ctx, "resource://"+handleXLSX, true)
	require.NoError(t, err)
	require.Equal(t, "/workspace/input/kb/"+handleXLSX+"_报价表_2026.xlsx", xlsRes.StagedPath)
	require.Contains(t, store.written, xlsRes.StagedPath)

	// 4. Oversized 50 MiB .pptx is NOT staged and returns TooLarge=true
	bigRes, err := reader.ReadKnowledgeResource(ctx, "resource://"+handleBig, true)
	require.NoError(t, err)
	require.True(t, bigRes.TooLarge)
	require.Empty(t, bigRes.StagedPath)

	// 5. Cross-KB resource is strictly denied
	_, err = reader.ReadKnowledgeResource(ctx, "resource://"+handleOther, true)
	require.ErrorContains(t, err, "authorized knowledge scope")
	require.NotContains(t, store.written, "/workspace/input/kb/"+handleOther+".png")

	// 6. Non-knowledge resource (message artifact / skill zip) is strictly denied
	_, err = reader.ReadKnowledgeResource(ctx, "resource://"+handleMsg, true)
	require.ErrorContains(t, err, "authorized knowledge scope")

	// 7. IM scope blocks unpublished/disabled knowledge even in an allowed KB
	imCtx := types.WithIMKnowledgeScope(ctx, []string{"kb-allowed"})
	imCtx = types.WithIMSandboxGrant(imCtx, types.IMSandboxGrant{
		SessionID:  "sess-1",
		TenantID:   10000,
		ConfigID:   "cfg-1",
		AllowShell: true,
		Revalidate: func(context.Context) error { return nil },
	})
	_, err = reader.ReadKnowledgeResource(imCtx, "resource://"+handleDraft, true)
	require.ErrorContains(t, err, "authorized knowledge scope")
}
