package github

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

// Source mode uses one pinned archive rather than a request for every code
// file. The authenticated API redirect is consumed only for codeload.github.com;
// the token is never forwarded to the redirected request or written to logs.
func (c *Connector) BuildSnapshot(ctx context.Context, cfg *types.DataSourceConfig, b *snapshot.Builder) error {
	s, err := parseSelection(cfg)
	if err != nil {
		return err
	}
	commit, _, err := c.head(ctx, cfg, s)
	if err != nil {
		return err
	}
	b.SetRevision(commit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/repos/"+s.Repository+"/tarball/"+commit, nil)
	if err != nil {
		return errors.New("invalid repository archive request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "octo-weknora")
	if t := token(cfg); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("GitHub archive request failed")
	}
	if resp.StatusCode == http.StatusFound {
		location, locationErr := resp.Location()
		resp.Body.Close()
		if locationErr != nil || location.Scheme != "https" || location.Host != "codeload.github.com" || location.User != nil {
			return errors.New("GitHub archive redirect was not authorized")
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
		if err != nil {
			return errors.New("invalid archive location")
		}
		req.Header.Set("User-Agent", "octo-weknora")
		resp, err = c.http.Do(req)
		if err != nil {
			return errors.New("GitHub archive download failed")
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("GitHub archive unavailable; check access or rate limits")
	}
	compressed := &io.LimitedReader{R: resp.Body, N: (256 << 20) + 1}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return errors.New("invalid GitHub archive")
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
			return errors.New("GitHub archive is incomplete or exceeds its size limit")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !snapshot.SafePath(name) {
			return errors.New("invalid path in GitHub archive")
		}
		parts := strings.SplitN(name, "/", 2)
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix {
			return errors.New("inconsistent GitHub archive root")
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
			return errors.New("incomplete repository file")
		}
		segments := strings.Split(p, "/")
		for i := range segments {
			segments[i] = url.PathEscape(segments[i])
		}
		u := "https://github.com/" + s.Repository + "/blob/" + commit + "/" + strings.Join(segments, "/")
		if err = b.Add(ctx, p, body, u, commit); err != nil {
			return err
		}
	}
	for _, root := range s.Paths {
		if root != "" && !found[root] {
			return errors.New("selected repository path is missing")
		}
	}
	if compressed.N <= 1 || expanded.N <= 1 {
		return errors.New("repository archive exceeds its size limit")
	}
	return nil
}
