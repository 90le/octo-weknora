package file

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalBackendWrapperMayShareOrSeparatePhysicalFiles(t *testing.T) {
	ctx := context.Background()
	sharedBase, separateBase := t.TempDir(), t.TempDir()
	plain := NewLocalFileService(sharedBase, "")
	path, err := plain.SaveBytes(ctx, []byte("protected"), 7, "report.pdf", false)
	require.NoError(t, err)
	wrappedPath := "storage://backend-a/" + path
	shared := NewBackendScopedFileService("backend-a", NewLocalFileService(sharedBase, ""))
	reader, err := shared.GetFile(ctx, wrappedPath)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, "protected", string(got), "same base makes the wrapped locator read the plain file")
	name := filepath.Base(path)
	require.NoError(t, os.MkdirAll(filepath.Join(separateBase, "7", "exports"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(separateBase, "7", "exports", name), []byte("other"), 0o600))
	separate := NewBackendScopedFileService("backend-a", NewLocalFileService(separateBase, ""))
	reader, err = separate.GetFile(ctx, wrappedPath)
	require.NoError(t, err)
	got, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, "other", string(got), "different backend base is a distinct physical file")
	_, err = NewBackendScopedFileService("backend-b", NewLocalFileService(sharedBase, "")).GetFile(ctx, wrappedPath)
	require.Error(t, err, "a wrapper cannot be used with a different backend ID")
}
