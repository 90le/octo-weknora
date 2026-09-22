package github

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

const defaultGitCacheLimit = int64(2 << 30)

type gitTreeEntry struct {
	Path string
	SHA  string
	Size int64
	Mode string
}

type gitCache struct {
	dir    string
	remote string
	token  string
}

type cacheOutput struct {
	bytes.Buffer
	max int
}

func (b *cacheOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("Git cache command output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func (c *Connector) buildGitSnapshot(ctx context.Context, cfg *types.DataSourceConfig, b *snapshot.Builder, previous *types.SourceSnapshot) error {
	s, err := parseSelection(cfg)
	if err != nil {
		return err
	}
	var commit string
	if token(cfg) == "" {
		commit, err = c.publicHead(ctx, s)
	} else {
		commit, _, err = c.head(ctx, cfg, s)
	}
	if err != nil {
		return err
	}
	b.SetRevision(commit)
	// Always resolve the remote ref first. A matching revision can then reuse a
	// complete local manifest without cloning, fetching, walking the tree, or
	// rereading Git blobs. ReuseSnapshot validates every object in this exact
	// data-source namespace; a missing or damaged object deliberately falls
	// through to a normal rebuild below.
	if previous != nil && previous.Selection == snapshot.Selection(cfg) && previous.Revision == commit {
		if err := b.ReuseSnapshot(previous); err == nil {
			return nil
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errGitUnavailable
	}
	cacheRoot, err := b.PrivateDirectory("git")
	if err != nil {
		return err
	}
	cache := gitCache{
		dir:    filepath.Join(cacheRoot, repositoryCacheKey(s.Repository)),
		remote: "https://github.com/" + s.Repository + ".git",
		token:  token(cfg),
	}
	if err = cache.fetchCommit(ctx, commit); err != nil {
		return err
	}
	if size, sizeErr := directorySize(cache.dir, githubGitCacheLimit()+1); sizeErr != nil {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot be inspected"}
	} else if size > githubGitCacheLimit() {
		_ = os.RemoveAll(cache.dir)
		return &Error{Code: "github_cache_limit", Message: "GitHub source cache exceeds its per-source limit; narrow the repository selection"}
	}
	entries, err := cache.tree(ctx, commit)
	if err != nil {
		return err
	}
	previousByPath := map[string]types.SourceFile{}
	if previous != nil && previous.Selection == snapshot.Selection(cfg) {
		for _, f := range previous.Files {
			previousByPath[f.Path] = f
		}
	}
	found := map[string]bool{}
	need := make([]gitTreeEntry, 0)
	for _, entry := range entries {
		for _, root := range s.Paths {
			if root != "" && (entry.Path == root || strings.HasPrefix(entry.Path, root+"/")) {
				found[root] = true
			}
		}
		if !selected(entry.Path, s.Paths) {
			continue
		}
		if snapshot.Excluded(entry.Path, snapshot.Excludes(cfg)) {
			b.Skip("excluded")
			continue
		}
		if entry.Mode != "100644" && entry.Mode != "100755" {
			b.Skip("symlink_or_special_file")
			continue
		}
		if entry.Size > snapshot.MaxFileBytes {
			b.Skip("too_large")
			continue
		}
		url := githubBlobURL(s.Repository, commit, entry.Path)
		if previousFile, ok := previousByPath[entry.Path]; ok && previousFile.GitBlob == entry.SHA && previousFile.Size == entry.Size {
			previousFile.SourceURL = url
			previousFile.Revision = commit
			previousFile.GitBlob = entry.SHA
			if err = b.Reuse(previousFile); err != nil {
				return &Error{Code: "github_snapshot_cache", Message: "Previous source snapshot is unavailable; retry the sync"}
			}
			continue
		}
		need = append(need, entry)
	}
	for _, root := range s.Paths {
		if root != "" && !found[root] {
			return &Error{Code: "github_selected_path_missing", Message: "Selected GitHub path no longer exists; review source selection"}
		}
	}
	return cache.readBlobs(ctx, need, func(entry gitTreeEntry, body []byte) error {
		return b.AddGit(ctx, entry.Path, body, githubBlobURL(s.Repository, commit, entry.Path), commit, entry.SHA)
	})
}

func repositoryCacheKey(repository string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(repository)))
	return hex.EncodeToString(sum[:])
}

func githubGitCacheLimit() int64 {
	raw := strings.TrimSpace(os.Getenv("DATASOURCE_GITHUB_GIT_CACHE_MAX_BYTES"))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 64<<20 {
		return defaultGitCacheLimit
	}
	if value > 8<<30 {
		return 8 << 30
	}
	return value
}

func (g *gitCache) fetchCommit(ctx context.Context, commit string) error {
	if !shaPattern.MatchString(commit) {
		return &Error{Code: "github_ref_invalid", Message: "GitHub resolved an invalid commit"}
	}
	if err := os.MkdirAll(filepath.Dir(g.dir), 0o700); err != nil {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot be prepared"}
	}
	_, err := os.Stat(filepath.Join(g.dir, "HEAD"))
	if errors.Is(err, os.ErrNotExist) {
		// A shallow clone has the complete current tree locally, so directory
		// enumeration never starts a second, hidden network fetch. Partial clones
		// were smaller on paper but some proxy paths repeatedly fetched tree
		// objects during ls-tree and left the sync running indefinitely.
		if removeErr := os.RemoveAll(g.dir); removeErr != nil {
			return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot be reset"}
		}
		if err = g.cloneShallow(ctx); err != nil {
			return err
		}
	} else if err != nil {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot be opened"}
	}
	current, err := g.run(ctx, "config", "--local", "--get", "remote.origin.url")
	if err != nil {
		if _, err = g.run(ctx, "remote", "add", "origin", g.remote); err != nil {
			return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache remote cannot be configured"}
		}
	} else if strings.TrimSpace(string(current)) != g.remote {
		return &Error{Code: "github_cache_identity", Message: "GitHub source cache identity does not match this repository"}
	}
	if _, err = g.run(ctx, "cat-file", "-e", commit+"^{commit}"); err == nil {
		return nil
	}
	if _, err = g.run(ctx, "fetch", "--no-tags", "--depth=1", "origin", "+"+commit+":refs/weknora/"+commit); err != nil {
		return &Error{Code: "github_git_fetch", Message: "GitHub Git cache could not fetch this commit; retry later or narrow the source"}
	}
	if _, err = g.run(ctx, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return &Error{Code: "github_git_fetch", Message: "GitHub Git cache did not receive the requested commit"}
	}
	return nil
}

func (g *gitCache) cloneShallow(ctx context.Context) error {
	cmd, cleanup, err := g.commandIn(ctx, "", "clone", "--bare", "--depth=1", "--no-tags", "--", g.remote, g.dir)
	if err != nil {
		return err
	}
	defer cleanup()
	var out cacheOutput
	out.max = 16 << 20
	cmd.Stdout = &out
	if err = cmd.Run(); err != nil {
		return &Error{Code: "github_git_clone", Message: "GitHub source cache could not create a shallow clone; retry later or narrow the source"}
	}
	return nil
}

func (g *gitCache) tree(ctx context.Context, commit string) ([]gitTreeEntry, error) {
	raw, err := g.run(ctx, "ls-tree", "--full-tree", "-r", "-z", "-l", commit)
	if err != nil {
		return nil, &Error{Code: "github_tree", Message: "GitHub source tree cannot be read from the cache"}
	}
	entries := make([]gitTreeEntry, 0)
	for _, row := range bytes.Split(raw, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		parts := bytes.SplitN(row, []byte{'\t'}, 2)
		if len(parts) != 2 || !snapshot.SafePath(string(parts[1])) {
			return nil, &Error{Code: "github_tree_invalid", Message: "GitHub source tree contains an invalid path"}
		}
		fields := strings.Fields(string(parts[0]))
		if len(fields) != 4 || fields[1] != "blob" || !shaPattern.MatchString(fields[2]) {
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 {
			return nil, &Error{Code: "github_tree_invalid", Message: "GitHub source tree contains an invalid file size"}
		}
		entries = append(entries, gitTreeEntry{Path: string(parts[1]), SHA: fields[2], Size: size, Mode: fields[0]})
	}
	return entries, nil
}

func (g *gitCache) readBlobs(ctx context.Context, entries []gitTreeEntry, emit func(gitTreeEntry, []byte) error) error {
	if len(entries) == 0 {
		return nil
	}
	cmd, cleanup, err := g.command(ctx, "cat-file", "--batch")
	if err != nil {
		return err
	}
	defer cleanup()
	in, err := cmd.StdinPipe()
	if err != nil {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot read files"}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot read files"}
	}
	if err = cmd.Start(); err != nil {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot start file reader"}
	}
	writeErr := make(chan error, 1)
	go func() {
		for _, entry := range entries {
			if _, err := io.WriteString(in, entry.SHA+"\n"); err != nil {
				writeErr <- err
				_ = in.Close()
				return
			}
		}
		writeErr <- in.Close()
	}()
	reader := bufio.NewReaderSize(out, 128<<10)
	for _, entry := range entries {
		header, readErr := reader.ReadString('\n')
		if readErr != nil {
			_ = cmd.Wait()
			return &Error{Code: "github_blob_incomplete", Message: "GitHub source file could not be read completely; previous snapshot remains available"}
		}
		fields := strings.Fields(strings.TrimSpace(header))
		if len(fields) != 3 || fields[0] != entry.SHA || fields[1] != "blob" {
			_ = cmd.Wait()
			return &Error{Code: "github_blob_invalid", Message: "GitHub source cache returned an unexpected file object"}
		}
		size, parseErr := strconv.ParseInt(fields[2], 10, 64)
		if parseErr != nil || size != entry.Size || size > snapshot.MaxFileBytes {
			_ = cmd.Wait()
			return &Error{Code: "github_blob_invalid", Message: "GitHub source cache returned an invalid file size"}
		}
		body := make([]byte, size)
		if _, readErr = io.ReadFull(reader, body); readErr != nil {
			_ = cmd.Wait()
			return &Error{Code: "github_blob_incomplete", Message: "GitHub source file could not be read completely; previous snapshot remains available"}
		}
		if terminator, readErr := reader.ReadByte(); readErr != nil || terminator != '\n' {
			_ = cmd.Wait()
			return &Error{Code: "github_blob_incomplete", Message: "GitHub source file ended unexpectedly; previous snapshot remains available"}
		}
		if err = emit(entry, body); err != nil {
			_ = cmd.Wait()
			return err
		}
	}
	if err = <-writeErr; err != nil {
		_ = cmd.Wait()
		return &Error{Code: "github_cache_unavailable", Message: "GitHub source cache file request failed"}
	}
	if err = cmd.Wait(); err != nil {
		return &Error{Code: "github_blob_incomplete", Message: "GitHub source cache did not finish reading files"}
	}
	return nil
}

func (g *gitCache) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd, cleanup, err := g.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var out cacheOutput
	out.max = 16 << 20
	cmd.Stdout = &out
	if err = cmd.Run(); err != nil {
		return nil, errors.New("git command failed")
	}
	return out.Bytes(), nil
}

func (g *gitCache) command(ctx context.Context, args ...string) (*exec.Cmd, func(), error) {
	return g.commandIn(ctx, g.dir, args...)
}

func (g *gitCache) commandIn(ctx context.Context, directory string, args ...string) (*exec.Cmd, func(), error) {
	base := []string{"--no-pager", "--no-optional-locks", "-c", "core.hooksPath=" + os.DevNull, "-c", "credential.helper=", "-c", "protocol.file.allow=never"}
	if directory != "" {
		base = append(base, "-C", directory)
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") && !strings.HasPrefix(value, "WEKNORA_GITHUB_TOKEN=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https")
	cleanup := func() {}
	if g.token != "" {
		// `git clone` creates g.dir itself, so an initial private-repository
		// sync cannot place the askpass helper inside that directory. Keep the
		// short-lived helper in its already-private parent instead. This also
		// makes the helper usable by both the first clone and later cache reads.
		askPassDir, err := g.askPassDirectory()
		if err != nil {
			return nil, cleanup, err
		}
		askPass, remove, err := createAskPass(askPassDir)
		if err != nil {
			// Never expose the private cache path or an OS-specific failure through
			// the sync result/UI.
			return nil, cleanup, &Error{Code: "github_cache_unavailable", Message: "GitHub source credentials cannot be prepared"}
		}
		cleanup = remove
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askPass, "GIT_ASKPASS_REQUIRE=force", "WEKNORA_GITHUB_TOKEN="+g.token)
	}
	return cmd, cleanup, nil
}

func (g *gitCache) askPassDirectory() (string, error) {
	dir := filepath.Dir(g.dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot prepare credentials"}
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", &Error{Code: "github_cache_unavailable", Message: "GitHub source cache cannot prepare credentials"}
	}
	return dir, nil
}

func createAskPass(dir string) (string, func(), error) {
	if runtime.GOOS == "windows" {
		f, err := os.CreateTemp(dir, "askpass-*.cmd")
		if err != nil {
			return "", nil, err
		}
		if _, err = f.WriteString("@echo off\r\nif /I \"%~1\"==\"Username for 'https://github.com':\" (echo x-access-token) else (echo %WEKNORA_GITHUB_TOKEN%)\r\n"); err != nil {
			f.Close()
			_ = os.Remove(f.Name())
			return "", nil, err
		}
		if err = f.Close(); err != nil {
			_ = os.Remove(f.Name())
			return "", nil, err
		}
		return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
	}
	f, err := os.CreateTemp(dir, "askpass-*")
	if err != nil {
		return "", nil, err
	}
	if _, err = f.WriteString("#!/bin/sh\ncase \"$1\" in\n  *Username*) printf '%s\\n' x-access-token ;;\n  *) printf '%s\\n' \"$WEKNORA_GITHUB_TOKEN\" ;;\nesac\n"); err != nil {
		f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err = os.Chmod(f.Name(), 0o700); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

func directorySize(root string, stopAt int64) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unexpected link in Git cache")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > stopAt {
			return io.EOF
		}
		return nil
	})
	if errors.Is(err, io.EOF) {
		return total, nil
	}
	return total, err
}

func githubBlobURL(repository, commit, sourcePath string) string {
	parts := strings.Split(sourcePath, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "https://github.com/" + repository + "/blob/" + commit + "/" + strings.Join(parts, "/")
}
