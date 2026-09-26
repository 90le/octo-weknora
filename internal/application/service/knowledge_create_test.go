package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type createKnowledgeFileRepoStub struct {
	interfaces.KnowledgeRepository

	createCalls       int
	checkCalls        int
	checkDuplicate    *types.Knowledge
	createErr         error
	createdKnowledge  *types.Knowledge
	createdKnowledges []*types.Knowledge
}

func (r *createKnowledgeFileRepoStub) CheckKnowledgeExists(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	params *types.KnowledgeCheckParams,
) (bool, *types.Knowledge, error) {
	r.checkCalls++
	if r.checkDuplicate != nil {
		return true, r.checkDuplicate, nil
	}
	return false, nil, nil
}

func (r *createKnowledgeFileRepoStub) UpdateKnowledgeColumn(context.Context, string, string, interface{}) error {
	return nil
}

func (r *createKnowledgeFileRepoStub) CreateKnowledge(ctx context.Context, knowledge *types.Knowledge) error {
	r.createCalls++
	copied := *knowledge
	r.createdKnowledge = &copied
	r.createdKnowledges = append(r.createdKnowledges, &copied)
	return r.createErr
}

func TestPreparedFileImportKeepsIndependentSourceAndPathIdentity(t *testing.T) {
	repo := &createKnowledgeFileRepoStub{checkDuplicate: &types.Knowledge{ID: "previous-manual-upload"}}
	svc := &knowledgeService{
		repo: repo, kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc: &createKnowledgeFileServiceStub{}, task: &createKnowledgeTaskEnqueuerStub{},
	}
	base := newCreateKnowledgeFileContext()
	create := func(sourceID, candidate, sourceType string) *types.Knowledge {
		t.Helper()
		ds := &types.DataSource{ID: sourceID, TenantID: 1, KnowledgeBaseID: "kb-1", Type: sourceType}
		metadata := map[string]string{"datasource_id": sourceID, "external_id": candidate}
		if sourceType == types.ConnectorTypeGitHub {
			metadata["github_url"] = "https://github.com/test/" + sourceID + "/blob/0123456789012345678901234567890123456789/guide.mdx"
		}
		k, err := svc.CreateKnowledgeFromFile(
			withPreparedFileImport(base, ds, candidate), "kb-1",
			newMultipartFileHeader(t, "guide.mdx", "# Same bytes\nContent"), metadata,
			nil, "guide.mdx", nil, sourceType, nil,
		)
		require.NoError(t, err)
		return k
	}
	first := create("repo-a", "a/guide.mdx:pending:v1", types.ConnectorTypeGitHub)
	second := create("repo-b", "b/guide.mdx:pending:v1", types.ConnectorTypeGitHub)
	third := create("repo-a", "a/other.mdx:pending:v1", types.ConnectorTypeGitHub)
	fourth := create("folder-a", "other/guide.mdx:pending:v1", localfolder.Type)
	require.Len(t, repo.createdKnowledges, 4)
	require.Zero(t, repo.checkCalls, "prepared imports must not use KB-wide content dedupe")
	require.NotEqual(t, first.ID, second.ID)
	require.NotEqual(t, first.ID, third.ID)
	require.NotEqual(t, first.ID, fourth.ID)
	require.Equal(t, "repo-b", second.GetMetadata()["datasource_id"])
	require.Equal(t, "a/other.mdx:pending:v1", third.GetMetadata()["external_id"])
	require.Equal(t, "mdx", fourth.FileType)
	require.Equal(t, "guide.mdx", first.FileName)
	require.Equal(t, "https://github.com/test/repo-a/blob/0123456789012345678901234567890123456789/guide.mdx", first.Source)

	// Metadata and channel are both controlled by an upload client. Neither can
	// grant the internal exception without a matching private context scope.
	spoof := map[string]string{"datasource_id": "repo-a", "external_id": "spoof:pending:v1"}
	_, err := svc.CreateKnowledgeFromFile(base, "kb-1", newMultipartFileHeader(t, "guide.mdx", "# Same bytes\nContent"),
		spoof, nil, "guide.mdx", nil, types.ConnectorTypeGitHub, nil)
	require.Error(t, err)
	var duplicate *types.DuplicateKnowledgeError
	require.ErrorAs(t, err, &duplicate)
	require.Equal(t, 1, repo.checkCalls)
	require.Len(t, repo.createdKnowledges, 4)

	wrongScope := withPreparedFileImport(base, &types.DataSource{ID: "repo-a", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.ConnectorTypeGitHub}, "expected:pending:v1")
	_, err = svc.CreateKnowledgeFromFile(wrongScope, "kb-1", newMultipartFileHeader(t, "guide.mdx", "# Same bytes\nContent"),
		spoof, nil, "guide.mdx", nil, types.ConnectorTypeGitHub, nil)
	require.ErrorAs(t, err, &duplicate)
	require.Equal(t, 2, repo.checkCalls)
	require.Len(t, repo.createdKnowledges, 4)
}

// GetKnowledgeTags is invoked by setAndAttachKnowledgeTags after create even
// when no tags were supplied; a fresh knowledge has none, so return empty.
func (r *createKnowledgeFileRepoStub) GetKnowledgeTags(
	ctx context.Context,
	knowledgeIDs []string,
) (map[string][]*types.KnowledgeTag, error) {
	return map[string][]*types.KnowledgeTag{}, nil
}

type createKnowledgeFileKBServiceStub struct {
	interfaces.KnowledgeBaseService

	kb *types.KnowledgeBase
}

func (s *createKnowledgeFileKBServiceStub) GetKnowledgeBaseByID(
	ctx context.Context,
	id string,
) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type createKnowledgeFileServiceStub struct {
	saveErr              error
	saveCalls            int
	savedWithKnowledgeID string
	deleteCalls          int
	deletedPath          string
}

func (s *createKnowledgeFileServiceStub) CheckConnectivity(ctx context.Context) error {
	return nil
}

func (s *createKnowledgeFileServiceStub) SaveFile(
	ctx context.Context,
	file *multipart.FileHeader,
	tenantID uint64,
	knowledgeID string,
) (string, error) {
	s.saveCalls++
	s.savedWithKnowledgeID = knowledgeID
	if s.saveErr != nil {
		return "", s.saveErr
	}
	return "stored/" + knowledgeID, nil
}

func (s *createKnowledgeFileServiceStub) SaveBytes(
	ctx context.Context,
	data []byte,
	tenantID uint64,
	fileName string,
	temp bool,
) (string, error) {
	return "", errors.New("not implemented")
}

func (s *createKnowledgeFileServiceStub) GetFile(ctx context.Context, filePath string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (s *createKnowledgeFileServiceStub) GetFileURL(ctx context.Context, filePath string) (string, error) {
	return "", errors.New("not implemented")
}

func (s *createKnowledgeFileServiceStub) DeleteFile(ctx context.Context, filePath string) error {
	s.deleteCalls++
	s.deletedPath = filePath
	return nil
}

func (s *createKnowledgeFileServiceStub) CopyFile(ctx context.Context, srcPath string, tenantID uint64, knowledgeID string) (string, error) {
	return "", errors.New("not implemented")
}

type createKnowledgeTaskEnqueuerStub struct {
	calls int
}

func (s *createKnowledgeTaskEnqueuerStub) Enqueue(
	task *asynq.Task,
	opts ...asynq.Option,
) (*asynq.TaskInfo, error) {
	s.calls++
	return &asynq.TaskInfo{ID: "task-1", Queue: "default"}, nil
}

func TestCreateKnowledgeFromFileDoesNotPersistWhenStorageSaveFails(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{}
	fileSvc := &createKnowledgeFileServiceStub{saveErr: errors.New("storage unavailable")}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc:   fileSvc,
	}

	knowledge, err := svc.CreateKnowledgeFromFile(
		newCreateKnowledgeFileContext(),
		"kb-1",
		newMultipartFileHeader(t, "doc.txt", "hello"),
		nil,
		nil,
		"",
		nil,
		"",
		nil,
	)

	require.Error(t, err)
	require.Nil(t, knowledge)
	require.Equal(t, 1, fileSvc.saveCalls)
	require.Zero(t, repo.createCalls)
}

func TestCreateKnowledgeFromFilePersistsStoredFilePathOnCreate(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{}
	fileSvc := &createKnowledgeFileServiceStub{}
	task := &createKnowledgeTaskEnqueuerStub{}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc:   fileSvc,
		task:      task,
	}

	knowledge, err := svc.CreateKnowledgeFromFile(
		newCreateKnowledgeFileContext(),
		"kb-1",
		newMultipartFileHeader(t, "doc.txt", "hello"),
		nil,
		nil,
		"",
		nil,
		"",
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, knowledge)
	require.Equal(t, 1, fileSvc.saveCalls)
	require.NotEmpty(t, fileSvc.savedWithKnowledgeID)
	require.Equal(t, fileSvc.savedWithKnowledgeID, knowledge.ID)
	require.Equal(t, 1, repo.createCalls)
	require.NotNil(t, repo.createdKnowledge)
	require.Equal(t, "stored/"+knowledge.ID, repo.createdKnowledge.FilePath)
	require.Equal(t, 1, task.calls)
}

func TestCreateKnowledgeFromGitHubPinsSourceBeforeParserEnqueue(t *testing.T) {
	repo := &createKnowledgeFileRepoStub{}
	svc := &knowledgeService{repo: repo, kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}}, fileSvc: &createKnowledgeFileServiceStub{}, task: &createKnowledgeTaskEnqueuerStub{}}
	u := "https://github.com/test/docs/blob/0123456789012345678901234567890123456789/doc.txt"
	k, err := svc.CreateKnowledgeFromFile(newCreateKnowledgeFileContext(), "kb-1", newMultipartFileHeader(t, "doc.txt", "hello"), map[string]string{"github_url": u}, nil, "", nil, types.ConnectorTypeGitHub, nil)
	require.NoError(t, err)
	require.Equal(t, u, k.Source)
	require.Equal(t, u, repo.createdKnowledge.Source)
}

func TestCreateKnowledgeFromImageFallsBackWhenLegacyStorageConfigIsIncomplete(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{}
	fileSvc := &createKnowledgeFileServiceStub{}
	task := &createKnowledgeTaskEnqueuerStub{}
	kb := &types.KnowledgeBase{
		ID:        "kb-1",
		VLMConfig: types.VLMConfig{Enabled: true, ModelID: "vlm-1"},
	}
	kb.SetStorageProvider("cos")
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: kb},
		fileSvc:   fileSvc,
		task:      task,
	}
	ctx := context.WithValue(newCreateKnowledgeFileContext(), types.TenantInfoContextKey, &types.Tenant{
		StorageEngineConfig: &types.StorageEngineConfig{
			DefaultProvider: "cos",
			COS:             &types.COSEngineConfig{SecretID: "incomplete"},
		},
	})

	knowledge, err := svc.CreateKnowledgeFromFile(
		ctx,
		"kb-1",
		newMultipartFileHeader(t, "image.png", "image bytes"),
		nil,
		nil,
		"",
		nil,
		"",
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, knowledge)
	require.Equal(t, 1, fileSvc.saveCalls)
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, 1, task.calls)
}

func TestCreateKnowledgeFromFileDeletesStoredFileWhenCreateFails(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{createErr: errors.New("database unavailable")}
	fileSvc := &createKnowledgeFileServiceStub{}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc:   fileSvc,
	}

	knowledge, err := svc.CreateKnowledgeFromFile(
		newCreateKnowledgeFileContext(),
		"kb-1",
		newMultipartFileHeader(t, "doc.txt", "hello"),
		nil,
		nil,
		"",
		nil,
		"",
		nil,
	)

	require.EqualError(t, err, "database unavailable")
	require.Nil(t, knowledge)
	require.Equal(t, 1, fileSvc.saveCalls)
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, 1, fileSvc.deleteCalls)
	require.Equal(t, "stored/"+fileSvc.savedWithKnowledgeID, fileSvc.deletedPath)
}

func TestCreateKnowledgeFromFile_PersistsProcessOverrides(t *testing.T) {
	t.Parallel()

	repo := &createKnowledgeFileRepoStub{}
	fileSvc := &createKnowledgeFileServiceStub{}
	task := &createKnowledgeTaskEnqueuerStub{}
	svc := &knowledgeService{
		repo:      repo,
		kbService: &createKnowledgeFileKBServiceStub{kb: &types.KnowledgeBase{ID: "kb-1"}},
		fileSvc:   fileSvc,
		task:      task,
	}

	chunkSize := 512
	overrides := &types.KnowledgeProcessOverrides{
		ChunkingConfig: &types.ChunkingConfig{ChunkSize: chunkSize},
	}

	knowledge, err := svc.CreateKnowledgeFromFile(
		newCreateKnowledgeFileContext(),
		"kb-1",
		newMultipartFileHeader(t, "doc.txt", "hello"),
		map[string]string{"source": "test"},
		nil,
		"",
		nil,
		"",
		overrides,
	)

	require.NoError(t, err)
	require.NotNil(t, knowledge)
	require.Equal(t, 1, repo.createCalls)
	require.NotNil(t, repo.createdKnowledge)

	parsed, err := repo.createdKnowledge.ProcessOverrides()
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.NotNil(t, parsed.ChunkingConfig)
	require.Equal(t, chunkSize, parsed.ChunkingConfig.ChunkSize)

	metadataMap, err := repo.createdKnowledge.Metadata.Map()
	require.NoError(t, err)
	require.Equal(t, "test", metadataMap["source"])
}

func newCreateKnowledgeFileContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{})
	return ctx
}

func newMultipartFileHeader(t *testing.T, filename string, content string) *multipart.FileHeader {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, req.ParseMultipartForm(1024))
	return req.MultipartForm.File["file"][0]
}
