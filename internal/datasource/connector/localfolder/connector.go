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

	"github.com/Tencent/WeKnora/internal/datasource"
	githubsource "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

const Type = "local_folder"

type Root struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	TenantID uint64 `json:"tenant_id"`
}
type settings struct {
	RootID    string `json:"root_id"`
	Directory string `json:"directory"`
	Mode      string `json:"mode"`
}
type Connector struct{}

func NewConnector() *Connector  { return &Connector{} }
func (*Connector) Type() string { return Type }
func Roots(tenant uint64) ([]Root, error) {
	var all []Root
	if json.Unmarshal([]byte(os.Getenv("DATASOURCE_LOCAL_ROOTS")), &all) != nil {
		return nil, errors.New("server folder roots are not configured")
	}
	out := []Root{}
	seen := map[string]bool{}
	for _, r := range all {
		if r.ID == "" || seen[r.ID] || !filepath.IsAbs(r.Path) || r.TenantID == 0 {
			return nil, errors.New("server folder root configuration is invalid")
		}
		seen[r.ID] = true
		if r.TenantID == tenant {
			out = append(out, r)
		}
	}
	return out, nil
}
func AuthorizedRoot(tenant uint64, cfg *types.DataSourceConfig) (Root, error) {
	var c settings
	b, _ := json.Marshal(cfg.Settings)
	if json.Unmarshal(b, &c) != nil {
		return Root{}, datasource.ErrInvalidConfig
	}
	roots, err := Roots(tenant)
	if err != nil {
		return Root{}, err
	}
	for _, r := range roots {
		if r.ID == c.RootID {
			return r, nil
		}
	}
	return Root{}, errors.New("server folder root is not authorized for this knowledge base")
}
func parse(cfg *types.DataSourceConfig) (settings, error) {
	var c settings
	if cfg == nil {
		return c, datasource.ErrInvalidConfig
	}
	b, _ := json.Marshal(cfg.Settings)
	if json.Unmarshal(b, &c) != nil {
		return c, datasource.ErrInvalidConfig
	}
	if c.Directory != "" && !snapshot.SafePath(c.Directory) {
		return c, datasource.ErrInvalidConfig
	}
	if c.Mode != "" && c.Mode != "documents" && c.Mode != "source" {
		return c, datasource.ErrInvalidConfig
	}
	return c, nil
}
func (c *Connector) Validate(ctx context.Context, cfg *types.DataSourceConfig) error {
	if err := snapshot.ValidateSettings(cfg); err != nil {
		return err
	}
	tenant, _ := types.TenantIDFromContext(ctx)
	s, err := parse(cfg)
	if err != nil {
		return err
	}
	if s.RootID == "" {
		roots, err := Roots(tenant)
		if err != nil {
			return err
		}
		if len(roots) == 0 {
			return errors.New("no server folders are authorized for this workspace")
		}
		return nil
	}
	r, err := AuthorizedRoot(tenant, cfg)
	if err != nil {
		return err
	}
	if snapshot.IsSource(cfg) {
		if _, err = snapshot.FromEnvironment(); err != nil {
			return err
		}
	}
	f, err := os.OpenRoot(r.Path)
	if err != nil {
		return errors.New("server folder is unavailable")
	}
	defer f.Close()
	if s.Directory != "" {
		child, err := descend(f, s.Directory)
		if err != nil {
			return err
		}
		defer child.Close()
	}
	return nil
}

// Open each directory relative to a held root, and compare identity before
// descending. Symlink substitution cannot redirect a scan to another subtree.
func descend(root *os.Root, dir string) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, errors.New("cannot open source directory")
	}
	for _, name := range strings.Split(dir, "/") {
		if name == "" {
			continue
		}
		info, err := current.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, errors.New("source directory must not be a symlink")
		}
		next, err := current.OpenRoot(name)
		if err != nil {
			current.Close()
			return nil, errors.New("cannot open source directory")
		}
		actual, err := next.Stat(".")
		current.Close()
		if err != nil || !os.SameFile(info, actual) {
			next.Close()
			return nil, errors.New("source directory changed during scan")
		}
		current = next
	}
	return current, nil
}
func (c *Connector) ListResources(ctx context.Context, cfg *types.DataSourceConfig, parent string) ([]types.Resource, error) {
	tenant, _ := types.TenantIDFromContext(ctx)
	out := []types.Resource{}
	s, err := parse(cfg)
	if err != nil {
		return nil, err
	}
	if s.RootID == "" {
		roots, err := Roots(tenant)
		if err != nil {
			return nil, err
		}
		for _, r := range roots {
			out = append(out, types.Resource{ExternalID: r.ID, Name: r.Name, Type: "folder", HasChildren: true})
		}
		return out, nil
	}
	r, err := AuthorizedRoot(tenant, cfg)
	if err != nil {
		return nil, err
	}
	if parent != "" && !snapshot.SafePath(parent) {
		return nil, datasource.ErrInvalidConfig
	}
	f, err := os.OpenRoot(r.Path)
	if err != nil {
		return nil, errors.New("source folder unavailable")
	}
	defer f.Close()
	dir, err := descend(f, path.Join(s.Directory, parent))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	h, err := dir.Open(".")
	if err != nil {
		return nil, errors.New("source folder unavailable")
	}
	defer h.Close()
	entries, err := h.ReadDir(-1)
	if err != nil {
		return nil, errors.New("cannot list source folder")
	}
	for _, e := range entries {
		p := path.Join(parent, e.Name())
		if e.IsDir() && !snapshot.Excluded(p, snapshot.Excludes(cfg)) {
			out = append(out, types.Resource{ExternalID: p, Name: e.Name(), Type: "directory", ParentID: parent, HasChildren: true})
		}
	}
	return out, nil
}
func (*Connector) ResolveResourceAncestors(context.Context, *types.DataSourceConfig, []string) ([]string, error) {
	return []string{}, nil
}

func (c *Connector) walk(ctx context.Context, cfg *types.DataSourceConfig, visit func(string, []byte) error, skip func(string)) error {
	tenant, _ := types.TenantIDFromContext(ctx)
	r, err := AuthorizedRoot(tenant, cfg)
	if err != nil {
		return err
	}
	s, err := parse(cfg)
	if err != nil {
		return err
	}
	// A generated cache must never become an input source on the next scan.
	if cache := os.Getenv("DATASOURCE_SNAPSHOT_DIR"); cache != "" {
		rel, err := filepath.Rel(r.Path, cache)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return errors.New("source storage must be outside approved input folders")
		}
	}
	top, err := os.OpenRoot(r.Path)
	if err != nil {
		return errors.New("source folder unavailable")
	}
	defer top.Close()
	root, err := descend(top, s.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	var walk func(*os.Root, string) error
	walk = func(dir *os.Root, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := dir.Open(".")
		if err != nil {
			return errors.New("source directory read failed")
		}
		entries, err := h.ReadDir(-1)
		h.Close()
		if err != nil {
			return errors.New("source directory read failed")
		}
		for _, e := range entries {
			p := path.Join(prefix, e.Name())
			if snapshot.Excluded(p, snapshot.Excludes(cfg)) {
				skip("excluded")
				continue
			}
			before, err := dir.Lstat(e.Name())
			if err != nil {
				return errors.New("source changed during scan")
			}
			if before.Mode()&os.ModeSymlink != 0 {
				skip("symlink")
				continue
			}
			if before.IsDir() {
				child, err := descend(dir, e.Name())
				if err != nil {
					return err
				}
				err = walk(child, p)
				child.Close()
				if err != nil {
					return err
				}
				continue
			}
			if !before.Mode().IsRegular() {
				skip("special_file")
				continue
			}
			if !snapshot.IsSource(cfg) && !snapshot.DocumentPath(p) {
				skip("not_a_document")
				continue
			}
			limit := int64(snapshot.MaxFileBytes)
			if !snapshot.IsSource(cfg) {
				limit = 16 << 20
			}
			if before.Size() > limit {
				if snapshot.IsSource(cfg) {
					skip("too_large")
					continue
				}
				return fmt.Errorf("source file exceeds size limit: %s", p)
			}
			f, err := dir.Open(e.Name())
			if err != nil {
				return errors.New("source file read failed")
			}
			actual, err := f.Stat()
			if err != nil || !os.SameFile(before, actual) {
				f.Close()
				return errors.New("source file changed during scan")
			}
			body, readErr := io.ReadAll(io.LimitReader(f, limit+1))
			after, statErr := f.Stat()
			f.Close()
			if readErr != nil || statErr != nil || len(body) > int(limit) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				return errors.New("source file changed during scan; previous snapshot preserved")
			}
			if err = visit(p, body); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, s.Directory)
}
func (c *Connector) BuildSnapshot(ctx context.Context, cfg *types.DataSourceConfig, b *snapshot.Builder) error {
	tenant, _ := types.TenantIDFromContext(ctx)
	root, err := AuthorizedRoot(tenant, cfg)
	if err != nil {
		return err
	}
	git := inspectGit(ctx, root.Path)
	if git.Remote != "" {
		git.Verified = githubsource.VerifyCommit(ctx, git.Remote, git.Commit, cfg.Credentials)
	}
	if git.Verified {
		b.SetRevision(git.Commit)
	} else if git.Commit != "" {
		b.SetRevision("local:" + git.Commit)
	}
	return c.walk(ctx, cfg, func(p string, body []byte) error {
		u, revision := git.reference(p, body)
		return b.Add(ctx, p, body, u, revision)
	}, b.Skip)
}
func (c *Connector) FetchAll(ctx context.Context, cfg *types.DataSourceConfig, _ []string) ([]types.FetchedItem, error) {
	items, _, err := c.FetchIncremental(ctx, cfg, nil)
	return items, err
}
func (c *Connector) FetchIncremental(ctx context.Context, cfg *types.DataSourceConfig, old *types.SyncCursor) ([]types.FetchedItem, *types.SyncCursor, error) {
	if snapshot.IsSource(cfg) {
		return nil, nil, errors.New("source mode must use the snapshot pipeline")
	}
	s, err := parse(cfg)
	if err != nil {
		return nil, nil, err
	}
	previous := map[string]string{}
	selection := snapshot.Selection(cfg)
	if old != nil && old.ConnectorCursor["selection"] == selection {
		b, _ := json.Marshal(old.ConnectorCursor["files"])
		_ = json.Unmarshal(b, &previous)
	}
	files := map[string]string{}
	items := []types.FetchedItem{}
	total := 0
	err = c.walk(ctx, cfg, func(p string, body []byte) error {
		h := sha256.Sum256(body)
		version := hex.EncodeToString(h[:])
		files[p] = version
		if previous[p] == version {
			return nil
		}
		total += len(body)
		if total > 64<<20 || len(items) >= 2000 {
			return errors.New("folder document batch limit exceeded; narrow directory selection")
		}
		items = append(items, types.FetchedItem{ExternalID: "local:" + s.RootID + ":" + p, FileName: path.Join(s.RootID, p), Title: p, Content: body, SourceResourceID: s.RootID, Metadata: map[string]string{"channel": Type, "source_type": Type, "source_version": version, "source_path": p}})
		if len(body) == 0 {
			items[len(items)-1].Metadata["fetch_error"] = "empty source document; previous version preserved"
		}
		return nil
	}, func(string) {})
	if err != nil {
		return nil, nil, err
	}
	if old != nil && old.ConnectorCursor["selection"] == selection {
		for p := range previous {
			if _, ok := files[p]; !ok {
				items = append(items, types.FetchedItem{ExternalID: "local:" + s.RootID + ":" + p, IsDeleted: true})
			}
		}
	}
	return items, &types.SyncCursor{ConnectorCursor: map[string]interface{}{"selection": selection, "files": files}}, nil
}
