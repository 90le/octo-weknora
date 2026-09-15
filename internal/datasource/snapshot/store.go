package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrUnavailable = errors.New("source snapshot unavailable; run a successful sync first")

type Store struct{ Base string }

func FromEnvironment() (*Store, error) {
	p := strings.TrimSpace(os.Getenv("DATASOURCE_SNAPSHOT_DIR"))
	if p == "" || !filepath.IsAbs(p) {
		return nil, errors.New("DATASOURCE_SNAPSHOT_DIR must be configured as a private persistent directory")
	}
	return &Store{Base: p}, nil
}
func (s *Store) scope(ds *types.DataSource) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", ds.TenantID, ds.KnowledgeBaseID, ds.ID)))
	return filepath.Join(s.Base, hex.EncodeToString(h[:]))
}

type Builder struct {
	store    *Store
	ds       *types.DataSource
	manifest *types.SourceSnapshot
	size     int64
}

func (s *Store) Begin(ds *types.DataSource, cfg *types.DataSourceConfig) (*Builder, error) {
	if ds == nil || ds.TenantID == 0 || ds.ID == "" || ds.KnowledgeBaseID == "" {
		return nil, ErrUnavailable
	}
	if err := os.MkdirAll(filepath.Join(s.scope(ds), "objects"), 0o700); err != nil {
		return nil, errors.New("cannot prepare private source storage")
	}
	return &Builder{store: s, ds: ds, manifest: &types.SourceSnapshot{TenantID: ds.TenantID, KnowledgeBaseID: ds.KnowledgeBaseID, DataSourceID: ds.ID, Selection: Selection(cfg), Files: []types.SourceFile{}, Skipped: map[string]int{}}}, nil
}
func (b *Builder) SetRevision(revision string) { b.manifest.Revision = revision }
func (b *Builder) Skip(reason string)          { b.manifest.Skipped[reason]++ }
func (b *Builder) Add(ctx context.Context, p string, body []byte, sourceURL, revision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !SafePath(p) {
		return errors.New("invalid source file path")
	}
	if len(body) > MaxFileBytes {
		return fmt.Errorf("source file exceeds %d bytes: %s", MaxFileBytes, p)
	}
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		b.Skip("binary_or_unsupported_encoding")
		return nil
	}
	if bytes.HasPrefix(body, []byte("version https://git-lfs.github.com/spec/v1")) {
		b.Skip("git_lfs_pointer")
		return nil
	}
	if len(b.manifest.Files) >= MaxFiles || b.size+int64(len(body)) > MaxTotalBytes {
		return errors.New("source snapshot limit exceeded; narrow the selected paths")
	}
	h := sha256.Sum256(body)
	object := hex.EncodeToString(h[:])
	target := filepath.Join(b.store.scope(b.ds), "objects", object)
	if _, err := os.Stat(target); err != nil {
		if !os.IsNotExist(err) {
			return errors.New("source object unavailable")
		}
		f, err := os.CreateTemp(filepath.Dir(target), ".object-")
		if err != nil {
			return errors.New("cannot write source object")
		}
		name := f.Name()
		defer os.Remove(name)
		_, writeErr := f.Write(body)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			return errors.New("source object write failed")
		}
		if err = os.Rename(name, target); err != nil {
			return errors.New("source object publish failed")
		}
	}
	lines := bytes.Count(body, []byte{'\n'}) + 1
	if len(body) == 0 {
		lines = 0
	} else if body[len(body)-1] == '\n' {
		lines--
	}
	b.manifest.Files = append(b.manifest.Files, types.SourceFile{Path: p, Object: object, Size: int64(len(body)), Lines: lines, SourceURL: sourceURL, Revision: revision})
	b.size += int64(len(body))
	return nil
}
func (b *Builder) Finish() (*types.SourceSnapshot, error) {
	sort.Slice(b.manifest.Files, func(i, j int) bool { return b.manifest.Files[i].Path < b.manifest.Files[j].Path })
	for i := 1; i < len(b.manifest.Files); i++ {
		if b.manifest.Files[i].Path == b.manifest.Files[i-1].Path {
			return nil, errors.New("duplicate source path")
		}
	}
	data, err := json.Marshal(b.manifest)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	b.manifest.ID = hex.EncodeToString(sum[:])
	b.manifest.CreatedAt = time.Now().UTC()
	dir := filepath.Join(b.store.scope(b.ds), "snapshots")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("cannot prepare source manifest")
	}
	data, err = json.Marshal(b.manifest)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".manifest-")
	if err != nil {
		return nil, errors.New("cannot write source manifest")
	}
	name := f.Name()
	defer os.Remove(name)
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return nil, errors.New("source manifest write failed")
	}
	if err = os.Rename(name, filepath.Join(dir, b.manifest.ID+".json")); err != nil {
		return nil, errors.New("source manifest publish failed")
	}
	return b.manifest, nil
}
func (s *Store) Load(ds *types.DataSource, cfg *types.DataSourceConfig, id string) (*types.SourceSnapshot, error) {
	if !hashPattern.MatchString(id) {
		return nil, ErrUnavailable
	}
	f, err := os.Open(filepath.Join(s.scope(ds), "snapshots", id+".json"))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	var m types.SourceSnapshot
	if json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&m) != nil || m.ID != id || m.DataSourceID != ds.ID || m.TenantID != ds.TenantID || m.KnowledgeBaseID != ds.KnowledgeBaseID || m.Selection != Selection(cfg) {
		return nil, ErrUnavailable
	}
	check := m
	check.ID = ""
	check.CreatedAt = time.Time{}
	data, err := json.Marshal(check)
	if err != nil {
		return nil, ErrUnavailable
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != id {
		return nil, ErrUnavailable
	}
	return &m, nil
}
func (s *Store) Content(ds *types.DataSource, f types.SourceFile) ([]byte, error) {
	if !hashPattern.MatchString(f.Object) {
		return nil, ErrUnavailable
	}
	r, err := os.Open(filepath.Join(s.scope(ds), "objects", f.Object))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
	if err != nil || len(b) > MaxFileBytes {
		return nil, ErrUnavailable
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != f.Object {
		return nil, ErrUnavailable
	}
	return b, nil
}

// Delete removes only the generated cache namespace. It never touches source roots.
func (s *Store) Delete(ds *types.DataSource) error {
	if ds == nil || ds.TenantID == 0 || ds.ID == "" || ds.KnowledgeBaseID == "" {
		return ErrUnavailable
	}
	return os.RemoveAll(s.scope(ds))
}
