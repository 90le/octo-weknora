package localfolder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

// FetchStream processes one stable document at a time and checkpoints only
// successful versions. A failed file remains eligible for a subsequent retry.
func (c *Connector) FetchStream(ctx context.Context, cfg *types.DataSourceConfig, old *types.SyncCursor, h datasource.StreamHandler) (*types.SyncCursor, error) {
	return c.streamDocuments(ctx, cfg, old, h, false)
}

// FetchFullStream reprocesses present files while retaining the previous
// manifest for safe deletion reconciliation after a complete scan.
func (c *Connector) FetchFullStream(ctx context.Context, cfg *types.DataSourceConfig, old *types.SyncCursor, h datasource.StreamHandler) (*types.SyncCursor, error) {
	return c.streamDocuments(ctx, cfg, old, h, true)
}

func (c *Connector) streamDocuments(ctx context.Context, cfg *types.DataSourceConfig, old *types.SyncCursor, h datasource.StreamHandler, full bool) (*types.SyncCursor, error) {
	if snapshot.IsSource(cfg) {
		return nil, errors.New("source mode must use the snapshot pipeline")
	}
	outcomes, ok := h.(datasource.OutcomeStreamHandler)
	if !ok {
		return nil, errors.New("local document sync requires per-file outcomes")
	}
	tenant, _ := types.TenantIDFromContext(ctx)
	registered, err := c.AuthorizedRoot(ctx, tenant, cfg)
	if err != nil {
		return nil, err
	}
	s, err := parse(cfg)
	if err != nil {
		return nil, err
	}
	// Neither the source snapshot cache nor temporary staging may become input.
	for _, candidate := range []string{os.Getenv("DATASOURCE_SNAPSHOT_DIR"), os.TempDir()} {
		if candidate == "" {
			continue
		}
		rel, relErr := filepath.Rel(registered.Path, candidate)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("source storage must be outside approved input folders")
		}
	}
	top, err := openRegisteredRoot(registered)
	if err != nil {
		return nil, errors.New("source folder unavailable")
	}
	defer top.Close()
	root, err := descend(top, s.Directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	selection := snapshot.Selection(cfg)
	files := map[string]string{}
	oldSelection := ""
	if old != nil {
		oldSelection, _ = old.ConnectorCursor["selection"].(string)
	}
	if old != nil && oldSelection == selection {
		b, _ := json.Marshal(old.ConnectorCursor["files"])
		if err := json.Unmarshal(b, &files); err != nil {
			return nil, errors.New("invalid local folder sync cursor")
		}
	}
	if files == nil {
		files = map[string]string{}
	}
	// File observations are written after successful publication but before a
	// batched cursor checkpoint. Recover those committed versions if a worker
	// stopped in that interval, so a subsequent source deletion is not lost.
	known, err := outcomes.KnownStates(ctx, selection)
	if err != nil {
		return nil, err
	}
	for _, state := range known {
		if state.SyncedVersion != "" {
			files[state.Path] = state.SyncedVersion
		}
		if state.Status == "processing" && state.SourceVersion != "" {
			current, err := outcomes.IsCurrent(ctx, "local:"+s.RootID+":"+state.Path, state.SourceVersion)
			if err != nil {
				return nil, err
			}
			if current {
				files[state.Path] = state.SourceVersion
			}
		}
	}
	cursor := &types.SyncCursor{ConnectorCursor: map[string]interface{}{"selection": selection, "files": files}}
	seen := map[string]bool{}
	complete := true
	record := func(p, status, reason string, size int64, version string, previous bool) error {
		synced := files[p]
		if status == "indexed" {
			synced = version
		}
		if status == "removed" {
			synced = ""
		}
		return outcomes.RecordState(ctx, types.LocalSourceFileState{Selection: selection, Path: p, Status: status, Reason: reason, SourceSize: size, SourceVersion: version, SyncedVersion: synced, HasPrevious: previous})
	}
	// Persist periodically rather than rewriting the complete manifest for each
	// file. Replays after interruption are idempotent and check the indexed row.
	pending := 0
	lastCheckpoint := time.Now()
	checkpoint := func(force bool) error {
		if !force {
			pending++
			if pending < 20 && time.Since(lastCheckpoint) < 30*time.Second {
				return nil
			}
		}
		if err := h.Checkpoint(ctx, cursor); err != nil {
			return err
		}
		pending = 0
		lastCheckpoint = time.Now()
		return nil
	}
	failed := func(p, code, message string) error {
		complete = false
		_, err := outcomes.EmitWithOutcome(ctx, types.FetchedItem{
			ExternalID: "local:" + s.RootID + ":" + p, Title: p,
			Metadata: map[string]string{"error": message, "error_reason_code": code, "error_reason": message, "error_reason_code_value": fmt.Sprint(utils.GetMaxFileSizeMB())},
		})
		if err != nil || p == "" {
			return err
		}
		return record(p, "failed", code, 0, "", files[p] != "")
	}
	var walk func(*os.Root, string) error
	walk = func(dir *os.Root, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dh, err := dir.Open(".")
		if err != nil {
			return errors.New("source directory read failed")
		}
		entries, err := dh.ReadDir(-1)
		_ = dh.Close()
		if err != nil {
			return errors.New("source directory read failed")
		}
		for _, entry := range entries {
			p := path.Join(prefix, entry.Name())
			if snapshot.Excluded(p, snapshot.Excludes(cfg)) {
				continue
			}
			before, err := dir.Lstat(entry.Name())
			if err != nil {
				return errors.New("source changed during scan")
			}
			if before.Mode()&os.ModeSymlink != 0 || (!before.IsDir() && !before.Mode().IsRegular()) {
				// An inaccessible replacement of a formerly indexed file is not a
				// confirmed deletion, and a symlink subtree is never traversed.
				seen[p] = true
				if err := failed(p, "local_unsafe_file", "symbolic links and special files are not read; deletion reconciliation was skipped"); err != nil {
					return err
				}
				continue
			}
			if before.IsDir() {
				child, err := descend(dir, entry.Name())
				if err != nil {
					return err
				}
				err = walk(child, p)
				_ = child.Close()
				if err != nil {
					return err
				}
				continue
			}
			seen[p] = true
			if !snapshot.DocumentPath(p) {
				// Unsupported files are intentionally ignored, rather than breaking
				// an otherwise complete scan. A previous entry at this exact path
				// remains present in seen and cannot be inferred as deleted.
				if err := h.Emit(ctx, types.FetchedItem{Title: p, Metadata: map[string]string{"local_unsupported": "true"}}); err != nil {
					return err
				}
				if err := record(p, "ignored", "unsupported_file_type", before.Size(), "", files[p] != ""); err != nil {
					return err
				}
				continue
			}
			if before.Size() <= 0 || before.Size() > utils.GetMaxFileSize() {
				if err := failed(p, "local_size", fmt.Sprintf("source document is empty or exceeds the configured %d MiB limit", utils.GetMaxFileSizeMB())); err != nil {
					return err
				}
				continue
			}
			f, err := dir.Open(entry.Name())
			if err != nil {
				if err := failed(p, "local_read", "source document could not be read"); err != nil {
					return err
				}
				continue
			}
			actual, statErr := f.Stat()
			if statErr != nil || !os.SameFile(before, actual) {
				_ = f.Close()
				if err := failed(p, "local_changed", "source document changed during scan"); err != nil {
					return err
				}
				continue
			}
			digest := sha256.New()
			n, readErr := io.Copy(digest, io.LimitReader(&contextReader{ctx: ctx, reader: f}, utils.GetMaxFileSize()+1))
			after, statErr := f.Stat()
			if readErr != nil || statErr != nil || n != before.Size() || !before.ModTime().Equal(after.ModTime()) {
				_ = f.Close()
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := failed(p, "local_changed", "source document changed or could not be read"); err != nil {
					return err
				}
				continue
			}
			version := hex.EncodeToString(digest.Sum(nil))
			if !full && files[p] == version {
				current, err := outcomes.IsCurrent(ctx, "local:"+s.RootID+":"+p, version)
				if err != nil {
					_ = f.Close()
					return err
				}
				if current {
					_ = f.Close()
					if err := h.Emit(ctx, types.FetchedItem{Title: p, Metadata: map[string]string{"local_unchanged": "true"}}); err != nil {
						return err
					}
					if err := record(p, "indexed", "", before.Size(), version, true); err != nil {
						return err
					}
					continue
				}
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				_ = f.Close()
				if err := failed(p, "local_read", "source document could not be reread"); err != nil {
					return err
				}
				continue
			}
			// The private temporary copy pins the bytes to the hash even if the
			// source changes while the downstream parser creates its upload.
			tmp, err := os.CreateTemp("", "octo-local-document-*")
			if err != nil {
				_ = f.Close()
				return errors.New("cannot stage source document")
			}
			stageName := tmp.Name()
			// On Linux an unlinked open file remains readable through its file
			// descriptor and is reclaimed automatically if the process crashes.
			// Platforms that cannot unlink an open file use the later cleanup.
			_ = os.Remove(stageName)
			stageHash := sha256.New()
			n, readErr = io.Copy(io.MultiWriter(tmp, stageHash), io.LimitReader(&contextReader{ctx: ctx, reader: f}, utils.GetMaxFileSize()+1))
			after, statErr = f.Stat()
			_ = f.Close()
			if readErr != nil || statErr != nil || n != before.Size() || !before.ModTime().Equal(after.ModTime()) || hex.EncodeToString(stageHash.Sum(nil)) != version {
				_ = tmp.Close()
				_ = os.Remove(stageName)
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := failed(p, "local_changed", "source document changed during staging"); err != nil {
					return err
				}
				continue
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				_ = tmp.Close()
				_ = os.Remove(stageName)
				return err
			}
			item := types.FetchedItem{
				ExternalID: "local:" + s.RootID + ":" + p, FileName: path.Join(s.RootID, p), Title: p,
				ContentStream: tmp, SourceResourceID: s.RootID,
				Metadata: map[string]string{"channel": Type, "source_type": Type, "source_version": version, "source_path": p},
			}
			if err := record(p, "processing", "", before.Size(), version, files[p] != ""); err != nil {
				_ = tmp.Close()
				_ = os.Remove(stageName)
				return err
			}
			accepted, emitErr := outcomes.EmitWithOutcome(ctx, item)
			_ = tmp.Close()
			_ = os.Remove(stageName)
			if emitErr != nil {
				return emitErr
			}
			if accepted {
				if err := record(p, "indexed", "", before.Size(), version, files[p] != ""); err != nil {
					return err
				}
				files[p] = version
				if err := checkpoint(false); err != nil {
					return err
				}
			} else {
				if err := record(p, "failed", "ingest_failed", before.Size(), version, files[p] != ""); err != nil {
					return err
				}
				complete = false
			}
		}
		return nil
	}
	if err := walk(root, s.Directory); err != nil {
		return nil, err
	}
	// Never infer deletion from an incomplete or partially failed scan.
	if complete {
		missing := []string{}
		for p := range files {
			if !seen[p] {
				missing = append(missing, p)
			}
		}
		if outcomes.DeletionsEnabled() {
			for _, p := range missing {
				absent, verifyErr := sourcePathMissing(top, p)
				if verifyErr != nil || !absent {
					if err := failed(p, "local_changed", "source changed after scanning; deletion reconciliation was skipped"); err != nil {
						return nil, err
					}
					break
				}
			}
		}
		// An empty/disconnected mount can look like a successful scan. Do not
		// allow that observation, or an unexpectedly large disappearance, to
		// immediately erase indexed data even when deletion sync is enabled.
		approval := ""
		if cfg.SyncSource != nil {
			approval = cfg.SyncSource.ApprovedDeletionDigest
		}
		massDisappearance := len(missing) == len(files) || (len(missing) >= 20 && len(missing)*2 >= len(files))
		if complete && outcomes.DeletionsEnabled() && len(missing) > 0 && (massDisappearance || approval != "") {
			review := deletionReview(selection, files, missing)
			approved := approval != "" && approval == review.Digest
			if !approved {
				cursor.ConnectorCursor["deletion_review"] = review
				for _, p := range missing {
					if err := record(p, "pending_delete", "local_deletion_guard", 0, files[p], true); err != nil {
						return nil, err
					}
				}
				if err := failed("", "local_deletion_guard", fmt.Sprintf("%d previously indexed files are missing; deletion reconciliation requires review", len(missing))); err != nil {
					return nil, err
				}
			}
		}
	}
	if complete {
		for p := range files {
			if seen[p] {
				continue
			}
			if outcomes.DeletionsEnabled() {
				absent, verifyErr := sourcePathMissing(top, p)
				if verifyErr != nil || !absent {
					if err := failed(p, "local_changed", "source changed before deletion; remaining deletions were skipped"); err != nil {
						return nil, err
					}
					break
				}
			}
			accepted, err := outcomes.EmitWithOutcome(ctx, types.FetchedItem{ExternalID: "local:" + s.RootID + ":" + p, Title: p, IsDeleted: true})
			if err != nil {
				return nil, err
			}
			if accepted {
				if err := record(p, "removed", "", 0, files[p], true); err != nil {
					return nil, err
				}
				delete(files, p)
				if err := checkpoint(false); err != nil {
					return nil, err
				}
			} else if !outcomes.DeletionsEnabled() {
				if err := record(p, "missing_retained", "", 0, files[p], true); err != nil {
					return nil, err
				}
			} else {
				if err := record(p, "failed", "deletion_failed", 0, files[p], true); err != nil {
					return nil, err
				}
			}
		}
	}
	if complete {
		for _, state := range known {
			if !seen[state.Path] && files[state.Path] == "" && state.Status != "removed" {
				if err := record(state.Path, "removed", "", 0, "", state.SyncedVersion != ""); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := checkpoint(true); err != nil {
		return nil, err
	}
	return cursor, nil
}

// sourcePathMissing fails closed if any path component is a symlink, changes
// identity, is inaccessible, or is replaced with a non-directory. Only a
// genuine not-found result permits source-side deletion reconciliation.
func sourcePathMissing(root *os.Root, relative string) (bool, error) {
	if !snapshot.SafePath(relative) {
		return false, errors.New("invalid source path")
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return false, err
	}
	defer func() { _ = current.Close() }()
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		before, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if before.Mode()&os.ModeSymlink != 0 {
			return false, nil
		}
		if index == len(parts)-1 {
			return false, nil
		}
		if !before.IsDir() {
			return false, nil
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			return false, err
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			next.Close()
			return false, errors.New("source directory changed")
		}
		_ = current.Close()
		current = next
	}
	return false, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
