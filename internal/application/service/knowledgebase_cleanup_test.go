package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type kbFinalizerStub struct {
	interfaces.KnowledgeBaseRepository
	plan      *interfaces.KBDeletionPlan
	finalized int
}

func (r *kbFinalizerStub) PrepareKnowledgeBaseCleanup(context.Context, uint64, string) (*interfaces.KBDeletionPlan, error) {
	return r.plan, nil
}
func (r *kbFinalizerStub) FinalizeKnowledgeBaseCleanup(context.Context, uint64, string) error {
	r.finalized++
	return nil
}

type kbFilesStub struct {
	interfaces.FileService
	err   error
	calls []string
}

func (f *kbFilesStub) DeleteFile(_ context.Context, path string) error {
	f.calls = append(f.calls, path)
	return f.err
}
func TestKBPhysicalFailureReturnsRetryAndDoesNotRetireMetadata(t *testing.T) {
	files := &kbFilesStub{err: errors.New("storage offline")}
	repo := &kbFinalizerStub{plan: &interfaces.KBDeletionPlan{Knowledge: []*types.Knowledge{{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FilePath: "local/doc", StorageSize: 12}}}}
	svc := &knowledgeBaseService{repo: repo, fileSvc: files, chunkRepo: kbCleanupChunkRepo{}}
	body, err := json.Marshal(types.KBDeletePayload{TenantID: 7, KnowledgeBaseID: "kb"})
	require.NoError(t, err)
	task := asynq.NewTask(types.TypeKBDelete, body)
	require.ErrorContains(t, svc.ProcessKBDelete(context.Background(), task), "storage offline")
	require.Zero(t, repo.finalized)
	files.err = os.ErrNotExist // An earlier attempt may have successfully unlinked the bytes.
	require.NoError(t, svc.ProcessKBDelete(context.Background(), task))
	require.Equal(t, 1, repo.finalized)
}
func TestKBFileCleanupKeepsSharedResourceBytes(t *testing.T) {
	files := &kbFilesStub{}
	private := types.StoredResource{ID: "private", Handle: "abcdefghijklmnopqrstuv", PhysicalPath: "local/private", State: types.ResourceStateActive}
	shared := types.StoredResource{ID: "shared", Handle: "zyxwvutsrqponmlkjihgfe", PhysicalPath: "local/shared", State: types.ResourceStateActive}
	plan := &interfaces.KBDeletionPlan{Resources: []interfaces.KBDeletionResource{{Resource: private}, {Resource: shared, Shared: true}}}
	svc := &knowledgeBaseService{fileSvc: files}
	require.NoError(t, svc.deleteKnowledgeBaseFiles(context.Background(), []*types.Knowledge{{FilePath: types.BuildResourcePath(shared.Handle)}}, []string{"https://example.invalid/image.png", shared.PhysicalPath}, plan))
	require.Equal(t, []string{types.BuildResourcePath(private.Handle)}, files.calls)
}
