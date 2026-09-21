package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func fastPathConfig() *types.DataSourceConfig {
	return &types.DataSourceConfig{
		Credentials: map[string]interface{}{"access_token": "test-token"},
		Settings: map[string]interface{}{
			"mode":       "source",
			"repository": "test/docs",
			"ref":        "main",
		},
	}
}

func fastPathHeadConnector(t *testing.T, commit string) (*Connector, *[]string) {
	t.Helper()
	requests := []string{}
	c := NewConnector()
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path)
		var payload interface{}
		switch r.URL.Path {
		case "/repos/test/docs":
			payload = map[string]string{"full_name": "test/docs", "default_branch": "main"}
		case "/repos/test/docs/commits/main":
			payload = map[string]interface{}{
				"sha":    commit,
				"commit": map[string]interface{}{"tree": map[string]string{"sha": strings.Repeat("b", 40)}},
			}
		default:
			t.Fatalf("unexpected GitHub request: %s", r.URL.Path)
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})
	return c, &requests
}

func fastPathPreviousSnapshot(t *testing.T, store *snapshot.Store, ds *types.DataSource, cfg *types.DataSourceConfig, commit string) *types.SourceSnapshot {
	t.Helper()
	b, err := store.Begin(ds, cfg)
	require.NoError(t, err)
	b.SetRevision(commit)
	b.Skip("excluded")
	require.NoError(t, b.AddGit(context.Background(), "src/main.go", []byte("package main\n"), githubBlobURL("test/docs", commit, "src/main.go"), commit, strings.Repeat("c", 40)))
	previous, err := b.Finish()
	require.NoError(t, err)
	return previous
}

func TestGitSnapshotFastPathReusesSameRevisionWithoutGitCache(t *testing.T) {
	commit := strings.Repeat("a", 40)
	cfg := fastPathConfig()
	store := &snapshot.Store{Base: t.TempDir()}
	ds := &types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb"}
	previous := fastPathPreviousSnapshot(t, store, ds, cfg, commit)
	next, err := store.Begin(ds, cfg)
	require.NoError(t, err)

	// If the fast path tries any Git cache operation it cannot find Git here.
	// The only allowed network work is the authenticated remote ref resolution.
	t.Setenv("PATH", t.TempDir())
	c, requests := fastPathHeadConnector(t, commit)
	require.NoError(t, c.buildGitSnapshot(context.Background(), cfg, next, previous))
	current, err := next.Finish()
	require.NoError(t, err)

	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests)
	require.Equal(t, previous.ID, current.ID)
	require.Equal(t, previous.Revision, current.Revision)
	require.Equal(t, previous.Files, current.Files)
	require.Equal(t, previous.Skipped, current.Skipped)
}

func TestGitSnapshotFastPathFallsBackWhenPriorObjectIsMissing(t *testing.T) {
	commit := strings.Repeat("a", 40)
	cfg := fastPathConfig()
	store := &snapshot.Store{Base: t.TempDir()}
	ds := &types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb"}
	previous := fastPathPreviousSnapshot(t, store, ds, cfg, commit)

	objects, err := filepath.Glob(filepath.Join(store.Base, "*", "objects", previous.Files[0].Object))
	require.NoError(t, err)
	require.Len(t, objects, 1)
	require.NoError(t, os.Remove(objects[0]))
	next, err := store.Begin(ds, cfg)
	require.NoError(t, err)

	// The missing object must reject reuse and reach the normal Git rebuild
	// branch. An empty PATH turns that branch into the explicit unavailable-Git
	// sentinel instead of allowing an accidental manifest reuse.
	t.Setenv("PATH", t.TempDir())
	c, requests := fastPathHeadConnector(t, commit)
	err = c.buildGitSnapshot(context.Background(), cfg, next, previous)
	require.ErrorIs(t, err, errGitUnavailable)
	require.Equal(t, []string{"/repos/test/docs", "/repos/test/docs/commits/main"}, *requests)
}
