package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type boundedIMTestManager struct {
	*artifactFallbackManager
}

func (m *boundedIMTestManager) ReadSessionFileBounded(
	ctx context.Context, sessionID, path string, limit int64,
) ([]byte, error) {
	data, err := m.source.ReadSessionFile(ctx, sessionID, path)
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("bounded read failed")
	}
	return data, nil
}

func TestIMCaptureOnlyPublishesCurrentSessionChanges(t *testing.T) {
	t.Setenv("WEKNORA_SKILL_OUTPUT_DIR", "")
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	db := newPinTestDB(t)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg-docker"})
	require.NoError(t, err)
	oldTime := time.Unix(100, 0)
	old := sandbox.RemoteDirEntry{
		Path: "/workspace/output/old.txt", Name: "old.txt",
		Type: sandbox.RemoteEntryFile, Size: 3, ModTime: oldTime,
	}
	source := &fakeSandboxSource{
		entries: map[string][]sandbox.RemoteDirEntry{"s-1": {old}},
		contents: map[string][]byte{
			"/workspace/output/old.txt": []byte("old"),
			"/workspace/output/new.pdf": {0x25, 0x50, 0x44, 0x46, 0x00, 0xff},
		},
	}
	mgr := &boundedIMTestManager{artifactFallbackManager: &artifactFallbackManager{source: source}}
	store := &fakeFileService{}
	collector := NewArtifactCollector(source, store, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, stubSandboxResolver{mgr: mgr}
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg-docker", 7, 7)
	require.NoError(t, err)
	newFile := sandbox.RemoteDirEntry{
		Path: "/workspace/output/new.pdf", Name: "new.pdf",
		Type: sandbox.RemoteEntryFile, Size: 6, ModTime: oldTime.Add(time.Second),
	}
	outside := sandbox.RemoteDirEntry{
		Path: "/workspace/output/../secret.txt", Name: "secret.txt",
		Type: sandbox.RemoteEntryFile, Size: 6, ModTime: oldTime,
	}
	source.entries["s-1"] = []sandbox.RemoteDirEntry{old, newFile, outside}
	artifacts, err := collector.CollectIM(ctx, snapshot, "message-1")
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, "new.pdf", artifacts[0].FileName)
	require.Equal(t, source.contents[newFile.Path], store.saved[artifacts[0].URL])
	require.Equal(t, []string{newFile.Path}, source.readCalls)

	require.NoError(t, db.Model(&types.Session{}).Where("id = ?", "s-1").
		Update("sandbox_config_id", "different-config").Error)
	_, err = collector.CollectIM(ctx, snapshot, "message-2")
	require.Error(t, err, "a changed sandbox pin must not release files")
}

func TestIMCaptureFailsClosedWithoutBoundedRead(t *testing.T) {
	t.Setenv("WEKNORA_SKILL_OUTPUT_DIR", "")
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	pinner := NewSessionSandboxPinner(newPinTestDB(t))
	_, err := pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg-docker"})
	require.NoError(t, err)
	source := &fakeSandboxSource{entries: map[string][]sandbox.RemoteDirEntry{}}
	mgr := &artifactFallbackManager{source: source}
	collector := NewArtifactCollector(source, &fakeFileService{}, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, stubSandboxResolver{mgr: mgr}
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg-docker", 7, 7)
	require.NoError(t, err)
	source.entries["s-1"] = []sandbox.RemoteDirEntry{{
		Path: "/workspace/output/new.txt", Name: "new.txt", Type: sandbox.RemoteEntryFile, Size: 4,
	}}
	artifacts, err := collector.CollectIM(ctx, snapshot, "message-1")
	require.Error(t, err)
	require.Empty(t, artifacts)
	require.Empty(t, source.readCalls, "the unbounded reader must not be used")
}
