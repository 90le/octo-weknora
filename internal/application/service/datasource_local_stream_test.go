package service

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamToFileHeaderPreservesLargeContent(t *testing.T) {
	// Above the previous 16 MiB local-folder limit and close to the PPTX that
	// originally failed, while remaining a synthetic fixture.
	data := bytes.Repeat([]byte("synthetic-training-document"), 700000)
	file, form, err := streamToFileHeader(bytes.NewReader(data), "training.pptx")
	require.NoError(t, err)
	defer func() { _ = form.RemoveAll() }()
	require.Equal(t, int64(len(data)), file.Size)
	r, err := file.Open()
	require.NoError(t, err)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, data, got)
}
