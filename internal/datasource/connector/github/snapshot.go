package github

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

// BuildSnapshot preserves the SnapshotConnector contract for direct callers.
// The service invokes BuildSnapshotIncremental when a trusted prior manifest is
// available.
func (c *Connector) BuildSnapshot(ctx context.Context, cfg *types.DataSourceConfig, b *snapshot.Builder) error {
	return c.BuildSnapshotIncremental(ctx, cfg, b, nil)
}

// BuildSnapshotIncremental uses a private, partial Git cache when it is
// available. That avoids downloading the complete GitHub archive for every
// source update and lets unchanged blobs reuse the previous source object.
// The archive implementation remains a compatibility fallback only when the
// container does not have Git installed.
func (c *Connector) BuildSnapshotIncremental(ctx context.Context, cfg *types.DataSourceConfig, b *snapshot.Builder, previous *types.SourceSnapshot) error {
	return c.withSyncGate(ctx, func() error {
		if c.useGitCache {
			err := c.buildGitSnapshot(ctx, cfg, b, previous)
			if !errors.Is(err, errGitUnavailable) {
				return err
			}
		}
		return c.buildArchiveSnapshot(ctx, cfg, b)
	})
}

// buildArchiveSnapshot is retained for images without the Git executable. The
// authenticated API redirect is consumed only for codeload.github.com; the
// token is never forwarded to the redirected request or written to logs.
func (c *Connector) buildArchiveSnapshot(ctx context.Context, cfg *types.DataSourceConfig, b *snapshot.Builder) error {
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
	archiveURL := apiBase + "/repos/" + s.Repository + "/tarball/" + commit
	if token(cfg) == "" {
		archiveURL = "https://codeload.github.com/" + s.Repository + "/tar.gz/" + commit
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return &Error{Code: "github_archive_request", Message: "GitHub archive request is invalid"}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "octo-weknora")
	if t := token(cfg); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Code: "github_archive_connection", Message: "GitHub archive request failed"}
	}
	if resp.StatusCode == http.StatusFound {
		location, locationErr := resp.Location()
		resp.Body.Close()
		if locationErr != nil || location.Scheme != "https" || location.Host != "codeload.github.com" || location.User != nil {
			return &Error{Code: "github_archive_redirect", Message: "GitHub archive redirect was not authorized"}
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
		if err != nil {
			return &Error{Code: "github_archive_request", Message: "GitHub archive location is invalid"}
		}
		req.Header.Set("User-Agent", "octo-weknora")
		resp, err = c.http.Do(req)
		if err != nil {
			return &Error{Code: "github_archive_connection", Message: "GitHub archive download failed"}
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubHTTPError(resp)
	}
	compressed := &io.LimitedReader{R: resp.Body, N: (256 << 20) + 1}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return &Error{Code: "github_archive_invalid", Message: "GitHub archive is invalid"}
	}
	defer gz.Close()
	expanded := &io.LimitedReader{R: gz, N: (1 << 30) + 1}
	tr := tar.NewReader(expanded)
	prefix := ""
	found := map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return &Error{Code: "github_archive_incomplete", Message: "GitHub archive ended before it was complete; previous snapshot remains available"}
		}
		// GitHub emits a global PAX header carrying the commit comment before
		// the repository directory. It is metadata, not the archive root.
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !snapshot.SafePath(name) {
			return &Error{Code: "github_archive_invalid_path", Message: "GitHub archive contains an invalid path"}
		}
		parts := strings.SplitN(name, "/", 2)
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix {
			return &Error{Code: "github_archive_invalid", Message: "GitHub archive has inconsistent repository roots"}
		}
		if len(parts) == 1 {
			continue
		}
		p := parts[1]
		for _, root := range s.Paths {
			if p == root || strings.HasPrefix(p, root+"/") {
				found[root] = true
			}
		}
		if !selected(p, s.Paths) {
			continue
		}
		if snapshot.Excluded(p, snapshot.Excludes(cfg)) {
			b.Skip("excluded")
			continue
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			b.Skip("symlink_or_special_file")
			continue
		}
		if header.Size > snapshot.MaxFileBytes {
			b.Skip("too_large")
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, snapshot.MaxFileBytes+1))
		if err != nil || int64(len(body)) != header.Size {
			return &Error{Code: "github_archive_file_incomplete", Message: "GitHub archive file could not be read completely; previous snapshot remains available"}
		}
		u := githubBlobURL(s.Repository, commit, p)
		if err = b.Add(ctx, p, body, u, commit); err != nil {
			return err
		}
	}
	for _, root := range s.Paths {
		if root != "" && !found[root] {
			return &Error{Code: "github_selected_path_missing", Message: "Selected GitHub path no longer exists; review source selection"}
		}
	}
	if compressed.N <= 1 || expanded.N <= 1 {
		return &Error{Code: "github_archive_limit", Message: "GitHub archive exceeds its transfer size limit; use Git cache source sync or narrow the repository selection"}
	}
	return nil
}
