package localfolder

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestFileStatesTrackUpdatesAndAreScopedToSourceAndSelection(t *testing.T) {
	r := registryWithSpace(t, t.TempDir())
	migration, err := os.ReadFile("../../../../migrations/sqlite/000049_local_source_file_states.up.sql")
	require.NoError(t, err)
	require.NoError(t, r.db.Exec(string(migration)).Error)
	c := NewConnector(r)
	ctx := context.Background()
	selection := strings.Repeat("a", 64)
	state := types.LocalSourceFileState{Selection: selection, Path: "chapter/doc.pdf", Status: "failed", Reason: "local_size", HasPrevious: true}
	require.NoError(t, c.RecordFileState(ctx, 7, "source", state))
	state.Status, state.Reason = "indexed", ""
	require.NoError(t, c.RecordFileState(ctx, 7, "source", state))
	require.NoError(t, c.RecordFileState(ctx, 8, "source", state))
	page, err := c.ListFileStates(ctx, 7, "source", selection, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "indexed", page.Items[0].Status)
	require.EqualValues(t, 1, page.Counts["indexed"])
	page, err = c.ListFileStates(ctx, 7, "other", selection, "", 0)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	page, err = c.ListFileStates(ctx, 7, "source", strings.Repeat("b", 64), "", 0)
	require.NoError(t, err)
	require.Empty(t, page.Items)
}
