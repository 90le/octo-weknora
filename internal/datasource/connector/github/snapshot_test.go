package github

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestArchiveSnapshotUsesAllTextLanguagesWithoutForwardingToken(t *testing.T) {
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "pinned commit"}}))
	for _, p := range []string{"main.js", "main.php", "main.py", ".env", "node_modules/lib.js"} {
		body := []byte("first\nsecond\n")
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "repo-root/" + p, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	c, cfg, _ := fixture(t, nil, false)
	cfg.Settings["mode"] = "source"
	cfg.Credentials = map[string]interface{}{"access_token": "private-token"}
	base := c.http.Transport
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/tarball/") {
			require.Equal(t, "Bearer private-token", r.Header.Get("Authorization"))
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://codeload.github.com/test/docs/archive"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		if r.URL.Host == "codeload.github.com" {
			require.Empty(t, r.Header.Get("Authorization"))
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data.Bytes())), Header: make(http.Header), Request: r}, nil
		}
		return base.RoundTrip(r)
	})
	store := &snapshot.Store{Base: t.TempDir()}
	builder, err := store.Begin(&types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb"}, cfg)
	require.NoError(t, err)
	require.NoError(t, c.BuildSnapshot(context.Background(), cfg, builder))
	m, err := builder.Finish()
	require.NoError(t, err)
	require.Len(t, m.Files, 3)
	for _, f := range m.Files {
		require.Contains(t, f.SourceURL, "/blob/"+strings.Repeat("a", 40)+"/")
	}
}
