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
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRootInvalid     = errors.New("invalid server folder registration")
	ErrRootUnavailable = errors.New("folder is unavailable or contains a symlink; check the server mount and read permission")
	ErrRootInUse       = errors.New("folder is still used by a data source; remove the data source first")
	ErrRootExists      = errors.New("folder is already registered in this workspace")
	ErrRootUnsafe      = errors.New("folder contains an unsafe path or symbolic link")
)

// Space is a deployment-provided boundary. System administrators may also
// register a discovered direct directory of /source-roots, but never supply or
// modify an arbitrary absolute path through the API.
type Space struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"-"`
}

func (Space) TableName() string { return "local_source_spaces" }

type Root struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name"`
	SpaceID   string    `json:"space_id"`
	Directory string    `json:"directory"`
	TenantID  uint64    `json:"tenant_id"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Resolved paths never leave the connector or platform-administration API.
	Path      string `json:"-" gorm:"-"`
	SpacePath string `json:"-" gorm:"-"`
}

func (Root) TableName() string { return "local_source_roots" }

type registryState struct {
	ID         string `gorm:"primaryKey"`
	ImportedAt time.Time
}

func (registryState) TableName() string { return "local_source_registry_state" }

type Registry struct {
	db *gorm.DB
	// Test seam only. Production always discovers the fixed container boundary.
	discoveryPath string
}

// NewRegistry consumes the old root list once. After initialization, changing
// DATASOURCE_LOCAL_ROOTS cannot resurrect revoked grants or alter source paths.
// DATASOURCE_LOCAL_SPACES is infrastructure-only: new mounted spaces may be
// provisioned on restart, but an existing space's path is immutable.
func NewRegistry(db *gorm.DB) (*Registry, error) {
	r := &Registry{db: db}
	if err := r.initialize(os.Getenv("DATASOURCE_LOCAL_SPACES"), os.Getenv("DATASOURCE_LOCAL_ROOTS")); err != nil {
		return nil, err
	}
	return r, nil
}
func (r *Registry) initialize(spacesJSON, legacyJSON string) error {
	var spaces []Space
	if strings.TrimSpace(spacesJSON) != "" {
		if json.Unmarshal([]byte(spacesJSON), &spaces) != nil {
			return errors.New("DATASOURCE_LOCAL_SPACES is invalid")
		}
	}
	for i := range spaces {
		if spaces[i].ID == "" || !validName(spaces[i].Name) || !validSpacePath(spaces[i].Path) {
			return errors.New("mounted source space configuration is invalid")
		}
		spaces[i].Path = filepath.Clean(spaces[i].Path)
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, space := range spaces {
			var old Space
			err := tx.First(&old, "id = ?", space.ID).Error
			if err == nil {
				if old.Path != space.Path {
					return errors.New("a mounted source space path cannot change; register a new space ID")
				}
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err := tx.Create(&space).Error; err != nil {
				return err
			}
		}
		// This insert also serializes first-start imports in multi-replica deployments.
		marker := registryState{ID: "legacy-roots-v1", ImportedAt: time.Now().UTC()}
		imported := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&marker)
		if imported.Error != nil {
			return imported.Error
		}
		if imported.RowsAffected == 0 {
			return nil
		}
		if strings.TrimSpace(legacyJSON) == "" {
			return nil
		}
		var legacy []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Path     string `json:"path"`
			TenantID uint64 `json:"tenant_id"`
		}
		if json.Unmarshal([]byte(legacyJSON), &legacy) != nil {
			return errors.New("legacy source root configuration is invalid")
		}
		for _, item := range legacy {
			if item.ID == "" || !validName(item.Name) || item.TenantID == 0 || !validSpacePath(item.Path) {
				return errors.New("legacy source root configuration is invalid")
			}
			path := filepath.Clean(item.Path)
			sum := sha256.Sum256([]byte(path))
			space := Space{ID: "imported-" + hex.EncodeToString(sum[:10]), Name: item.Name, Path: path}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&space).Error; err != nil {
				return err
			}
			root := Root{ID: item.ID, Name: item.Name, SpaceID: space.ID, TenantID: item.TenantID, Enabled: true}
			if err := tx.Create(&root).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func validName(name string) bool {
	return strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 128
}
func validSpacePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) != filepath.VolumeName(path)+string(os.PathSeparator)
}
func validDirectory(dir string) bool {
	return dir == "" || (len(dir) <= 2048 && utf8.ValidString(dir) && snapshot.SafePath(dir) && !filepath.IsAbs(dir) && !strings.ContainsAny(dir, "\\:\t") && filepath.Clean(dir) != ".")
}

func (r *Registry) Spaces(ctx context.Context) ([]Space, error) {
	out := []Space{}
	err := r.db.WithContext(ctx).Order("name, id").Find(&out).Error
	return out, err
}
func (r *Registry) List(ctx context.Context, tenant uint64) ([]Root, error) {
	if tenant == 0 {
		return nil, ErrRootInvalid
	}
	out := []Root{}
	err := r.db.WithContext(ctx).Where("tenant_id = ?", tenant).Order("name, id").Find(&out).Error
	return out, err
}
func (r *Registry) Roots(ctx context.Context, tenant uint64) ([]Root, error) {
	rows, err := r.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := []Root{}
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		var space Space
		if err := r.db.WithContext(ctx).First(&space, "id = ?", row.SpaceID).Error; err != nil {
			return nil, err
		}
		row.SpacePath = space.Path
		row.Path = filepath.Join(space.Path, filepath.FromSlash(row.Directory))
		out = append(out, row)
	}
	return out, nil
}

type Registration struct {
	Name      string `json:"name"`
	SpaceID   string `json:"space_id"`
	Directory string `json:"directory"`
}

func (r *Registry) resolve(ctx context.Context, tenant uint64, req Registration) (Root, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Directory = strings.TrimSpace(req.Directory)
	// Reject absolute/traversal input before normalization, including Windows paths.
	if strings.HasPrefix(strings.TrimSpace(req.Directory), "/") || !validDirectory(req.Directory) || tenant == 0 || !validName(req.Name) {
		return Root{}, ErrRootInvalid
	}
	var space Space
	if err := r.db.WithContext(ctx).First(&space, "id = ?", req.SpaceID).Error; err != nil {
		return Root{}, ErrRootInvalid
	}
	return Root{ID: uuid.NewString(), Name: req.Name, SpaceID: space.ID, Directory: req.Directory, TenantID: tenant, Enabled: true, Path: filepath.Join(space.Path, filepath.FromSlash(req.Directory)), SpacePath: space.Path}, nil
}
func (r *Registry) Probe(ctx context.Context, tenant uint64, req Registration) error {
	root, err := r.resolve(ctx, tenant, req)
	if err != nil {
		return err
	}
	dir, err := openRegisteredRoot(root)
	if err != nil {
		return ErrRootUnavailable
	}
	defer dir.Close()
	f, err := dir.Open(".")
	if err != nil {
		return ErrRootUnavailable
	}
	defer f.Close()
	_, err = f.ReadDir(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrRootUnavailable
	}
	return nil
}
func (r *Registry) Create(ctx context.Context, tenant uint64, req Registration) (*Root, error) {
	if err := r.Probe(ctx, tenant, req); err != nil {
		return nil, err
	}
	root, err := r.resolve(ctx, tenant, req)
	if err != nil {
		return nil, err
	}
	var count int64
	if err = r.db.WithContext(ctx).Model(&Root{}).Where("tenant_id = ? AND space_id = ? AND directory = ?", tenant, root.SpaceID, root.Directory).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, ErrRootExists
	}
	if err = r.db.WithContext(ctx).Table("tenants").Where("id = ? AND deleted_at IS NULL", tenant).Count(&count).Error; err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, ErrRootInvalid
	}
	err = r.db.WithContext(ctx).Create(&root).Error
	return &root, err
}
func (r *Registry) Update(ctx context.Context, tenant uint64, id, name string, enabled bool) (*Root, error) {
	if tenant == 0 || !validName(name) {
		return nil, ErrRootInvalid
	}
	var root Root
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, "tenant_id = ? AND id = ?", tenant, id).Error; err != nil {
			return err
		}
		return tx.Model(&root).Updates(map[string]any{"name": strings.TrimSpace(name), "enabled": enabled}).Error
	})
	if err == nil {
		root.Name = strings.TrimSpace(name)
		root.Enabled = enabled
	}
	return &root, err
}
func (r *Registry) Delete(ctx context.Context, tenant uint64, id string) error {
	if tenant == 0 {
		return ErrRootInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root Root
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, "tenant_id = ? AND id = ?", tenant, id).Error; err != nil {
			return err
		}
		var sources []types.DataSource
		if err := tx.Where("tenant_id = ? AND type = ?", tenant, Type).Find(&sources).Error; err != nil {
			return err
		}
		for _, source := range sources {
			cfg, err := source.ParseConfig()
			if err != nil {
				return fmt.Errorf("cannot verify folder references: %w", err)
			}
			if cfg != nil && cfg.Settings["root_id"] == id {
				return ErrRootInUse
			}
		}
		// Only the grant is deleted. Neither original files nor synced documents are removed.
		return tx.Delete(&root).Error
	})
}

// All source file access starts from held directory handles. Neither an original
// symlink nor a replaced parent directory may redirect a request outside its space.
func openRegisteredRoot(root Root) (*os.Root, error) {
	if !validSpacePath(root.SpacePath) || !validDirectory(root.Directory) {
		return nil, ErrRootUnsafe
	}
	clean := filepath.Clean(root.SpacePath)
	volume := filepath.VolumeName(clean) + string(os.PathSeparator)
	base, err := os.OpenRoot(volume)
	if err != nil {
		return nil, ErrRootUnavailable
	}
	defer base.Close()
	rel, err := filepath.Rel(volume, clean)
	if err != nil {
		return nil, ErrRootUnavailable
	}
	space, err := descend(base, filepath.ToSlash(rel))
	if err != nil {
		if errors.Is(err, ErrRootUnsafe) {
			return nil, ErrRootUnsafe
		}
		return nil, ErrRootUnavailable
	}
	defer space.Close()
	return descend(space, root.Directory)
}
