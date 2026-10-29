package localfolder

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type localStreamRecorder struct {
	items          []types.FetchedItem
	checkpoints    []*types.SyncCursor
	allowDeletions bool
	failTitle      string
	missingCurrent bool
	states         []types.LocalSourceFileState
	known          []types.LocalSourceFileState
}

func (h *localStreamRecorder) Emit(ctx context.Context, item types.FetchedItem) error {
	if item.Metadata["local_unchanged"] == "true" {
		h.items = append(h.items, item)
		return nil
	}
	_, err := h.EmitWithOutcome(ctx, item)
	return err
}

func (h *localStreamRecorder) IsCurrent(context.Context, string, string) (bool, error) {
	return !h.missingCurrent, nil
}
func (h *localStreamRecorder) DeletionsEnabled() bool { return h.allowDeletions }
func (h *localStreamRecorder) RecordState(_ context.Context, state types.LocalSourceFileState) error {
	h.states = append(h.states, state)
	return nil
}

func (h *localStreamRecorder) KnownStates(context.Context, string) ([]types.LocalSourceFileState, error) {
	return h.known, nil
}

func TestDocumentStreamReportsUnsupportedAndRepairsMissingIndexedEntry(t *testing.T) {
	ctx, root, cfg, connector := localFixture(t)
	cfg.Settings["mode"] = "documents"
	require.NoError(t, os.WriteFile(filepath.Join(root, "guide.mhtml"), []byte("synthetic"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "archive.zip"), []byte("ignored"), 0o600))
	first := &localStreamRecorder{}
	cursor, err := connector.FetchStream(ctx, cfg, nil, first)
	require.NoError(t, err)
	require.Len(t, first.items, 2)
	versions := cursor.ConnectorCursor["files"].(map[string]string)
	require.Contains(t, versions, "guide.mhtml")
	require.NotContains(t, versions, "archive.zip")
	for _, state := range first.states {
		if state.Path == "archive.zip" {
			require.Equal(t, "ignored", state.Status)
		}
	}
	second := &localStreamRecorder{missingCurrent: true}
	_, err = connector.FetchStream(ctx, cfg, cursor, second)
	require.NoError(t, err)
	for _, item := range second.items {
		if item.Title == "guide.mhtml" {
			require.NotEqual(t, "true", item.Metadata["local_unchanged"])
			return
		}
	}
	t.Fatal("missing indexed document was not retried")
}

func (h *localStreamRecorder) EmitWithOutcome(_ context.Context, item types.FetchedItem) (bool, error) {
	if item.ContentStream != nil {
		_, err := io.Copy(io.Discard, item.ContentStream)
		if err != nil {
			return false, err
		}
		item.ContentStream = nil
	}
	h.items = append(h.items, item)
	if item.IsDeleted {
		return h.allowDeletions, nil
	}
	if item.Metadata["error"] != "" || item.Title == h.failTitle {
		return false, nil
	}
	return true, nil
}

func (h *localStreamRecorder) Checkpoint(_ context.Context, cursor *types.SyncCursor) error {
	b, _ := json.Marshal(cursor)
	var snapshot types.SyncCursor
	if err := json.Unmarshal(b, &snapshot); err != nil {
		return err
	}
	h.checkpoints = append(h.checkpoints, &snapshot)
	return nil
}

func sparseFile(t *testing.T, name string, size int64) {
	t.Helper()
	f, err := os.Create(name)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

func TestDocumentStreamContinuesPastOversizeAndSupportsMoreThan16MiB(t *testing.T) {
	ctx, root, cfg, connector := localFixture(t)
	cfg.Settings["mode"] = "documents"
	t.Setenv("MAX_FILE_SIZE_MB", "20")
	require.NoError(t, os.WriteFile(filepath.Join(root, "small.pdf"), []byte("synthetic"), 0o600))
	sparseFile(t, filepath.Join(root, "large.pptx"), 16<<20+1)
	sparseFile(t, filepath.Join(root, "too-large.pptx"), 20<<20+1)
	h := &localStreamRecorder{}
	cursor, err := connector.FetchStream(ctx, cfg, nil, h)
	require.NoError(t, err)
	require.Len(t, h.items, 3)
	versions := cursor.ConnectorCursor["files"].(map[string]string)
	require.Contains(t, versions, "small.pdf")
	require.Contains(t, versions, "large.pptx")
	require.NotContains(t, versions, "too-large.pptx")
	for _, item := range h.items {
		if item.Title == "too-large.pptx" {
			require.Contains(t, item.Metadata["error"], "20 MiB")
		}
	}
	require.Len(t, h.checkpoints, 1)
}

func TestDocumentStreamPreservesDeletionBaselineUntilCompleteAndAllowed(t *testing.T) {
	ctx, root, cfg, connector := localFixture(t)
	cfg.Settings["mode"] = "documents"
	t.Setenv("MAX_FILE_SIZE_MB", "1")
	oldPath := filepath.Join(root, "old.pdf")
	require.NoError(t, os.WriteFile(oldPath, []byte("old"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.pdf"), []byte("keep"), 0o600))
	initial, err := connector.FetchStream(ctx, cfg, nil, &localStreamRecorder{})
	require.NoError(t, err)
	require.NoError(t, os.Remove(oldPath))
	sparseFile(t, filepath.Join(root, "bad.pptx"), 1<<20+1)
	partial := &localStreamRecorder{allowDeletions: true}
	afterPartial, err := connector.FetchStream(ctx, cfg, initial, partial)
	require.NoError(t, err)
	for _, item := range partial.items {
		require.False(t, item.IsDeleted)
	}
	require.Contains(t, afterPartial.ConnectorCursor["files"].(map[string]string), "old.pdf")
	require.NoError(t, os.Remove(filepath.Join(root, "bad.pptx")))
	blocked := &localStreamRecorder{}
	afterBlocked, err := connector.FetchStream(ctx, cfg, afterPartial, blocked)
	require.NoError(t, err)
	require.Len(t, blocked.items, 2)
	require.True(t, blocked.items[1].IsDeleted)
	require.Contains(t, afterBlocked.ConnectorCursor["files"].(map[string]string), "old.pdf")
	allowed := &localStreamRecorder{allowDeletions: true}
	afterAllowed, err := connector.FetchFullStream(ctx, cfg, afterBlocked, allowed)
	require.NoError(t, err)
	require.Len(t, allowed.items, 2)
	require.True(t, allowed.items[1].IsDeleted)
	require.NotContains(t, afterAllowed.ConnectorCursor["files"].(map[string]string), "old.pdf")
}

func TestDocumentStreamGuardsAgainstEmptyMountDeletion(t *testing.T) {
	ctx, root, cfg, connector := localFixture(t)
	cfg.Settings["mode"] = "documents"
	file := filepath.Join(root, "original.pdf")
	require.NoError(t, os.WriteFile(file, []byte("original"), 0o600))
	initial, err := connector.FetchStream(ctx, cfg, nil, &localStreamRecorder{})
	require.NoError(t, err)
	require.NoError(t, os.Remove(file))
	h := &localStreamRecorder{allowDeletions: true}
	cursor, err := connector.FetchStream(ctx, cfg, initial, h)
	require.NoError(t, err)
	require.Contains(t, cursor.ConnectorCursor["files"].(map[string]string), "original.pdf")
	require.Len(t, h.items, 1)
	require.False(t, h.items[0].IsDeleted)
	require.Equal(t, "local_deletion_guard", h.items[0].Metadata["error_reason_code"])
	require.Len(t, h.states, 1)
	require.Equal(t, "pending_delete", h.states[0].Status)
	review, err := DeletionReviewFromCursor(cursor)
	require.NoError(t, err)
	require.Equal(t, []string{"original.pdf"}, review.Paths)
	cfg.SyncSource = &types.DataSourceSyncSource{ApprovedDeletionDigest: strings.Repeat("0", 64)}
	wrong := &localStreamRecorder{allowDeletions: true}
	_, err = connector.FetchStream(ctx, cfg, cursor, wrong)
	require.NoError(t, err)
	require.False(t, wrong.items[0].IsDeleted)
	cfg.SyncSource.ApprovedDeletionDigest = review.Digest
	approved := &localStreamRecorder{allowDeletions: true}
	final, err := connector.FetchStream(ctx, cfg, cursor, approved)
	require.NoError(t, err)
	require.True(t, approved.items[0].IsDeleted)
	require.Empty(t, final.ConnectorCursor["files"])
}

func TestDocumentStreamRecoversPublishedFileBeforeCheckpointAndClearsOldFailure(t *testing.T) {
	ctx, root, cfg, connector := localFixture(t)
	cfg.Settings["mode"] = "documents"
	file := filepath.Join(root, "published.pdf")
	require.NoError(t, os.WriteFile(file, []byte("content"), 0o600))
	first := &localStreamRecorder{}
	_, err := connector.FetchStream(ctx, cfg, nil, first)
	require.NoError(t, err)
	require.NoError(t, os.Remove(file))
	retry := &localStreamRecorder{allowDeletions: true, known: first.states[len(first.states)-1:]}
	// A nil cursor simulates interruption after publication, before checkpoint.
	cursor, err := connector.FetchStream(ctx, cfg, nil, retry)
	require.NoError(t, err)
	require.Contains(t, cursor.ConnectorCursor["files"].(map[string]string), "published.pdf")
	require.Equal(t, "local_deletion_guard", retry.items[0].Metadata["error_reason_code"])
	processing := &localStreamRecorder{allowDeletions: true, known: first.states[:1]}
	cursor, err = connector.FetchStream(ctx, cfg, nil, processing)
	require.NoError(t, err)
	require.Contains(t, cursor.ConnectorCursor["files"].(map[string]string), "published.pdf")
	require.Equal(t, "local_deletion_guard", processing.items[0].Metadata["error_reason_code"])
	stale := &localStreamRecorder{known: []types.LocalSourceFileState{{Path: "old.zip", Status: "ignored"}}}
	_, err = connector.FetchStream(ctx, cfg, nil, stale)
	require.NoError(t, err)
	require.Len(t, stale.states, 1)
	require.Equal(t, "removed", stale.states[0].Status)
}

func TestSourceDeletionCheckRejectsReplacedPaths(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "folder"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "folder", "present.pdf"), []byte("fixture"), 0o600))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { _ = root.Close() }()
	missing, err := sourcePathMissing(root, "folder/present.pdf")
	require.NoError(t, err)
	require.False(t, missing)
	missing, err = sourcePathMissing(root, "folder/missing.pdf")
	require.NoError(t, err)
	require.True(t, missing)
	if os.Symlink(t.TempDir(), filepath.Join(dir, "link")) == nil {
		missing, err = sourcePathMissing(root, "link/missing.pdf")
		require.NoError(t, err)
		require.False(t, missing)
	}
}

func TestDocumentStreamRetriesFailedChangedVersion(t *testing.T) {
	ctx, root, cfg, connector := localFixture(t)
	cfg.Settings["mode"] = "documents"
	p := filepath.Join(root, "doc.pdf")
	require.NoError(t, os.WriteFile(p, []byte("old"), 0o600))
	old, err := connector.FetchStream(ctx, cfg, nil, &localStreamRecorder{})
	require.NoError(t, err)
	oldVersion := old.ConnectorCursor["files"].(map[string]string)["doc.pdf"]
	require.NoError(t, os.WriteFile(p, []byte("new content"), 0o600))
	failed := &localStreamRecorder{failTitle: "doc.pdf"}
	retained, err := connector.FetchStream(ctx, cfg, old, failed)
	require.NoError(t, err)
	require.Equal(t, oldVersion, retained.ConnectorCursor["files"].(map[string]string)["doc.pdf"])
	retry := &localStreamRecorder{}
	updated, err := connector.FetchStream(ctx, cfg, retained, retry)
	require.NoError(t, err)
	require.Len(t, retry.items, 1)
	require.NotEqual(t, oldVersion, updated.ConnectorCursor["files"].(map[string]string)["doc.pdf"])
	require.True(t, strings.HasPrefix(retry.items[0].ExternalID, "local:"))
}
