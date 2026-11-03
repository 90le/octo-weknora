package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type deliveredFileStore struct {
	fakeFileService
	deleted []string
	onSave  func()
}

func (f *deliveredFileStore) SaveBytes(
	ctx context.Context, data []byte, tenant uint64, name string, temp bool,
) (string, error) {
	ref, err := f.fakeFileService.SaveBytes(ctx, data, tenant, name, temp)
	if f.onSave != nil {
		f.onSave()
	}
	return ref, err
}

func (f *deliveredFileStore) DeleteFile(_ context.Context, ref string) error {
	f.deleted = append(f.deleted, ref)
	delete(f.saved, ref)
	return nil
}

func TestPersistDeliveredTextScopeAndCleanup(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "session")
	store := &deliveredFileStore{}
	collector := NewArtifactCollector(nil, store, nil, nil, ArtifactCollectorConfig{})
	allow := func(context.Context) error { return nil }
	artifact, err := collector.PersistDeliveredText(ctx, "session", "message", 7, "report.txt", []byte("hello"), allow)
	require.NoError(t, err)
	require.Equal(t, "report.txt", artifact.FileName)
	require.Equal(t, "", artifact.SourcePath)
	require.Equal(t, artifactContentHash([]byte("hello")), artifact.ContentHash)
	require.Equal(t, []uint64{7}, store.tenantIDs)
	require.Equal(t, []byte("hello"), store.saved[artifact.URL])
	require.NoError(t, collector.DiscardDeliveredText(ctx, "message", artifact))
	require.Empty(t, store.saved)
	for _, tc := range []struct {
		session, message, name string
		tenant                 uint64
		auth                   func(context.Context) error
	}{
		{"other", "message", "a.txt", 7, allow},
		{"session", "message", "a.txt", 8, allow},
		{"session", "", "a.txt", 7, allow},
		{"session", "message", "../a.txt", 7, allow},
		{"session", "message", "a.txt", 7, nil},
	} {
		_, err := collector.PersistDeliveredText(ctx, tc.session, tc.message, tc.tenant, tc.name, []byte("x"), tc.auth)
		require.Error(t, err)
	}
	require.Equal(t, 1, store.seq)
}

func TestPersistDeliveredTextRevocationAfterSaveCleansUp(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "session")
	allowed := true
	store := &deliveredFileStore{onSave: func() { allowed = false }}
	collector := NewArtifactCollector(nil, store, nil, nil, ArtifactCollectorConfig{})
	_, err := collector.PersistDeliveredText(ctx, "session", "message", 7, "a.txt", []byte("secret"), func(
		context.Context,
	) error {
		if !allowed {
			return errors.New("revoked")
		}
		return nil
	})
	require.Error(t, err)
	require.Len(t, store.deleted, 1)
	require.Empty(t, store.saved)
}

func TestPersistDeliveredTextUsesMessageResourceBinding(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithSessionID(ctx, "session")
	store := &resourceRefFileService{handle: "abcdefghijklmnopqrstuv"}
	catalog := &fakeCatalog{}
	collector := NewArtifactCollector(nil, store, nil, catalog, ArtifactCollectorConfig{})
	artifact, err := collector.PersistDeliveredText(
		ctx, "session", "message", 7, "data.csv", []byte("a,b"), func(context.Context) error { return nil },
	)
	require.NoError(t, err)
	require.Equal(t, []bindCall{
		{artifact.URL, artifactBindingOwnerType, "message", artifactBindingRelation},
	}, catalog.binds)
	require.Equal(t, []uint64{7}, store.tenantIDs)
}
