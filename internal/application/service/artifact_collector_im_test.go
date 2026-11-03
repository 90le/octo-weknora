package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func imArtifactTestContext(t *testing.T, db *gorm.DB) context.Context {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&types.TenantSandboxConfigEntity{}))
	require.NoError(t, db.Create(&types.TenantSandboxConfigEntity{
		ID: "cfg-docker", TenantID: 7, Name: "test", SandboxType: "docker",
		Config: &types.TenantSandboxConfig{SandboxType: "docker"},
	}).Error)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "s-1")
	ctx = types.WithIMKnowledgeScope(ctx, []string{"kb"})
	return types.WithIMSandboxGrant(ctx, types.IMSandboxGrant{
		SessionID: "s-1", TenantID: 7, ConfigID: "cfg-docker",
		Revalidate: func(context.Context) error { return nil },
	})
}

type boundedIMTestManager struct {
	*artifactFallbackManager
	afterRead func()
}

func (m *boundedIMTestManager) ReadSessionFileBounded(
	ctx context.Context, sessionID, path string, limit int64,
) ([]byte, error) {
	data, err := m.source.ReadSessionFile(ctx, sessionID, path)
	if m.afterRead != nil {
		m.afterRead()
	}
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("bounded read failed")
	}
	return data, nil
}

func TestIMCaptureOnlyPublishesCurrentSessionChanges(t *testing.T) {
	t.Setenv("WEKNORA_SKILL_OUTPUT_DIR", "")
	db := newPinTestDB(t)
	ctx := imArtifactTestContext(t, db)
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
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg-docker", 7, 7, nil)
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
	selectNew := func(relative string, _ bool) IMArtifactSelection {
		if relative == "new.pdf" {
			return IMArtifactCurrentTurn
		}
		return IMArtifactNotSelected
	}
	artifacts, err := collector.CollectIM(ctx, snapshot, "message-1", selectNew)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, "new.pdf", artifacts[0].FileName)
	require.Equal(t, source.contents[newFile.Path], store.saved[artifacts[0].URL])
	require.Equal(t, []string{newFile.Path}, source.readCalls)

	require.NoError(t, db.Model(&types.Session{}).Where("id = ?", "s-1").
		Update("sandbox_config_id", "different-config").Error)
	_, err = collector.CollectIM(ctx, snapshot, "message-2", selectNew)
	require.Error(t, err, "a changed sandbox pin must not release files")
}

func TestIMCaptureFailsClosedWithoutBoundedRead(t *testing.T) {
	t.Setenv("WEKNORA_SKILL_OUTPUT_DIR", "")
	db := newPinTestDB(t)
	ctx := imArtifactTestContext(t, db)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg-docker"})
	require.NoError(t, err)
	source := &fakeSandboxSource{entries: map[string][]sandbox.RemoteDirEntry{}}
	mgr := &artifactFallbackManager{source: source}
	collector := NewArtifactCollector(source, &fakeFileService{}, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, stubSandboxResolver{mgr: mgr}
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg-docker", 7, 7, nil)
	require.NoError(t, err)
	source.entries["s-1"] = []sandbox.RemoteDirEntry{{
		Path: "/workspace/output/new.txt", Name: "new.txt", Type: sandbox.RemoteEntryFile, Size: 4,
	}}
	artifacts, err := collector.CollectIM(
		ctx,
		snapshot,
		"message-1",
		func(string, bool) IMArtifactSelection { return IMArtifactCurrentTurn },
	)
	require.Error(t, err)
	require.Empty(t, artifacts)
	require.Empty(t, source.readCalls, "the unbounded reader must not be used")
}

func TestIMCaptureSelectedVersionWithUnchangedMetadata(t *testing.T) {
	db := newPinTestDB(t)
	ctx := imArtifactTestContext(t, db)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg-docker"})
	require.NoError(t, err)
	entry := sandbox.RemoteDirEntry{
		Path:    "/workspace/output/report.csv",
		Name:    "report.csv",
		Type:    sandbox.RemoteEntryFile,
		Size:    3,
		ModTime: time.Unix(100, 0),
	}
	secret := sandbox.RemoteDirEntry{
		Path:    "/workspace/output/private.csv",
		Name:    "private.csv",
		Type:    sandbox.RemoteEntryFile,
		Size:    3,
		ModTime: entry.ModTime,
	}
	source := &fakeSandboxSource{
		entries:  map[string][]sandbox.RemoteDirEntry{"s-1": {entry, secret}},
		contents: map[string][]byte{entry.Path: []byte("old"), secret.Path: []byte("key")},
	}
	mgr := &boundedIMTestManager{artifactFallbackManager: &artifactFallbackManager{source: source}}
	files := &fakeFileService{}
	collector := NewArtifactCollector(source, files, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, stubSandboxResolver{mgr: mgr}
	snapshot, err := collector.SnapshotIM(
		ctx,
		"s-1",
		"cfg-docker",
		7,
		7,
		func(relative string) bool { return relative == "report.csv" },
	)
	require.NoError(t, err)
	// A rewrite can preserve both byte count and mtime. Explicit selection,
	// not those metadata fields, decides which session file may be read.
	source.contents[entry.Path] = []byte("new")
	artifacts, err := collector.CollectIM(
		ctx, snapshot, "message-1", func(relative string, _ bool) IMArtifactSelection {
			if relative == "report.csv" {
				return IMArtifactCurrentTurn
			}
			return IMArtifactNotSelected
		})
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, []byte("new"), files.saved[artifacts[0].URL])
	require.Equal(t, []string{entry.Path, entry.Path}, source.readCalls)
}

func TestIMCaptureSelectsOnlyTheRequestedRelativePath(t *testing.T) {
	db := newPinTestDB(t)
	ctx := imArtifactTestContext(t, db)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg-docker"})
	require.NoError(t, err)
	a := sandbox.RemoteDirEntry{
		Path: "/workspace/output/a/report.pdf",
		Name: "report.pdf",
		Type: sandbox.RemoteEntryFile,
		Size: 1,
	}
	b := sandbox.RemoteDirEntry{
		Path: "/workspace/output/b/report.pdf",
		Name: "report.pdf",
		Type: sandbox.RemoteEntryFile,
		Size: 1,
	}
	source := &fakeSandboxSource{
		entries:  map[string][]sandbox.RemoteDirEntry{"s-1": {}},
		contents: map[string][]byte{a.Path: []byte("a"), b.Path: []byte("b")},
	}
	mgr := &boundedIMTestManager{artifactFallbackManager: &artifactFallbackManager{source: source}}
	files := &fakeFileService{}
	collector := NewArtifactCollector(source, files, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, stubSandboxResolver{mgr: mgr}
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg-docker", 7, 7, nil)
	require.NoError(t, err)
	source.entries["s-1"] = []sandbox.RemoteDirEntry{a, b}
	artifacts, err := collector.CollectIM(
		ctx,
		snapshot,
		"message-1",
		func(relative string, ambiguous bool) IMArtifactSelection {
			require.True(t, ambiguous)
			if relative == "a/report.pdf" {
				return IMArtifactCurrentTurn
			}
			return IMArtifactNotSelected
		},
	)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, []byte("a"), files.saved[artifacts[0].URL])
	require.Equal(t, []string{a.Path}, source.readCalls)
}

func TestIMCaptureRechecksGrantAfterEachRead(t *testing.T) {
	db := newPinTestDB(t)
	ctx := imArtifactTestContext(t, db)
	allowed := true
	grant, _ := types.IMSandboxGrantFromContext(ctx)
	grant.Revalidate = func(context.Context) error {
		if !allowed {
			return errors.New("revoked")
		}
		return nil
	}
	ctx = types.WithIMSandboxGrant(ctx, grant)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg-docker"})
	require.NoError(t, err)
	a := sandbox.RemoteDirEntry{Path: "/workspace/output/a.txt", Name: "a.txt", Type: sandbox.RemoteEntryFile, Size: 1}
	b := sandbox.RemoteDirEntry{Path: "/workspace/output/b.txt", Name: "b.txt", Type: sandbox.RemoteEntryFile, Size: 1}
	source := &fakeSandboxSource{
		entries:  map[string][]sandbox.RemoteDirEntry{"s-1": {}},
		contents: map[string][]byte{a.Path: []byte("a"), b.Path: []byte("b")},
	}
	mgr := &boundedIMTestManager{
		artifactFallbackManager: &artifactFallbackManager{source: source},
		afterRead:               func() { allowed = false },
	}
	files := &fakeFileService{}
	collector := NewArtifactCollector(source, files, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, stubSandboxResolver{mgr: mgr}
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg-docker", 7, 7, nil)
	require.NoError(t, err)
	source.entries["s-1"] = []sandbox.RemoteDirEntry{a, b}
	artifacts, err := collector.CollectIM(
		ctx,
		snapshot,
		"message-1",
		func(string, bool) IMArtifactSelection { return IMArtifactCurrentTurn },
	)
	require.Error(t, err)
	require.Empty(t, artifacts)
	require.Equal(t, []string{a.Path}, source.readCalls)
	require.Empty(t, files.saved)
}

type revokingIMFileService struct {
	*fakeFileService
	revoke func()
}

func (f *revokingIMFileService) SaveBytes(
	ctx context.Context,
	data []byte,
	tenant uint64,
	name string,
	temp bool,
) (string, error) {
	ref, err := f.fakeFileService.SaveBytes(ctx, data, tenant, name, temp)
	f.revoke()
	return ref, err
}

func TestIMCaptureRechecksGrantBetweenWrites(t *testing.T) {
	db := newPinTestDB(t)
	ctx := imArtifactTestContext(t, db)
	allowed := true
	grant, _ := types.IMSandboxGrantFromContext(ctx)
	grant.Revalidate = func(context.Context) error {
		if !allowed {
			return errors.New("revoked")
		}
		return nil
	}
	ctx = types.WithIMSandboxGrant(ctx, grant)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg-docker"})
	require.NoError(t, err)
	a := sandbox.RemoteDirEntry{Path: "/workspace/output/a.txt", Name: "a.txt", Type: sandbox.RemoteEntryFile, Size: 1}
	b := sandbox.RemoteDirEntry{Path: "/workspace/output/b.txt", Name: "b.txt", Type: sandbox.RemoteEntryFile, Size: 1}
	source := &fakeSandboxSource{
		entries:  map[string][]sandbox.RemoteDirEntry{"s-1": {}},
		contents: map[string][]byte{a.Path: []byte("a"), b.Path: []byte("b")},
	}
	mgr := &boundedIMTestManager{artifactFallbackManager: &artifactFallbackManager{source: source}}
	files := &revokingIMFileService{fakeFileService: &fakeFileService{}, revoke: func() { allowed = false }}
	collector := NewArtifactCollector(source, files, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, stubSandboxResolver{mgr: mgr}
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg-docker", 7, 7, nil)
	require.NoError(t, err)
	source.entries["s-1"] = []sandbox.RemoteDirEntry{a, b}
	artifacts, err := collector.CollectIM(
		ctx,
		snapshot,
		"message-1",
		func(string, bool) IMArtifactSelection { return IMArtifactCurrentTurn },
	)
	require.Error(t, err)
	require.Empty(t, artifacts)
	require.Equal(t, 1, files.seq, "no later write may start after revocation")
}

func TestIMCaptureRejectsOldFileEvenWhenSelected(t *testing.T) {
	db := newPinTestDB(t)
	ctx := imArtifactTestContext(t, db)
	pinner := NewSessionSandboxPinner(db)
	_, err := pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg-docker"})
	require.NoError(t, err)
	old := sandbox.RemoteDirEntry{
		Path:    "/workspace/output/old.txt",
		Name:    "old.txt",
		Type:    sandbox.RemoteEntryFile,
		Size:    3,
		ModTime: time.Unix(100, 0),
	}
	source := &fakeSandboxSource{
		entries:  map[string][]sandbox.RemoteDirEntry{"s-1": {old}},
		contents: map[string][]byte{old.Path: []byte("old")},
	}
	mgr := &boundedIMTestManager{artifactFallbackManager: &artifactFallbackManager{source: source}}
	collector := NewArtifactCollector(source, &fakeFileService{}, &fakeStore{}, nil, ArtifactCollectorConfig{})
	collector.pinner, collector.resolver = pinner, stubSandboxResolver{mgr: mgr}
	snapshot, err := collector.SnapshotIM(ctx, "s-1", "cfg-docker", 7, 7, func(string) bool { return true })
	require.NoError(t, err)
	artifacts, err := collector.CollectIM(
		ctx,
		snapshot,
		"message",
		func(string, bool) IMArtifactSelection { return IMArtifactExplicit },
	)
	require.NoError(t, err)
	require.Empty(t, artifacts)
}
