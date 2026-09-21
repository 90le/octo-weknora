package localfolder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

const discoveryBoundary = "/source-roots"
const directoryPageSize = 200
const directoryScanLimit = 10000

type SpaceSummary struct {
	Space
	Status              string `json:"status"`
	Readable            bool   `json:"readable"`
	AuthorizedRootCount int    `json:"authorized_root_count"`
	EnabledRootCount    int    `json:"enabled_root_count"`
	DataSourceCount     int    `json:"data_source_count"`
	UsageComplete       bool   `json:"usage_complete"`
}

type RootSummary struct {
	Root
	DataSourceCount int  `json:"data_source_count"`
	UsageComplete   bool `json:"usage_complete"`
}

func (r *Registry) RootSummaries(ctx context.Context, tenant uint64) ([]RootSummary, error) {
	roots, err := r.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]RootSummary, 0, len(roots))
	indices := map[string]int{}
	for _, root := range roots {
		indices[root.ID] = len(out)
		out = append(out, RootSummary{Root: root, UsageComplete: true})
	}
	sources, err := r.sourceReferences(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		id, complete := source.rootID()
		if !complete {
			for i := range out {
				out[i].UsageComplete = false
			}
			continue
		}
		if i, ok := indices[id]; ok {
			out[i].DataSourceCount++
		}
	}
	return out, nil
}

// Space summaries are global infrastructure data. Only the SystemAdmin API
// exposes them. Count source references using both tenant and root identity;
// disabled sources remain references, soft-deleted sources do not.
func (r *Registry) SpaceSummaries(ctx context.Context) ([]SpaceSummary, error) {
	spaces, err := r.Spaces(ctx)
	if err != nil {
		return nil, err
	}
	var roots []Root
	if err = r.db.WithContext(ctx).Find(&roots).Error; err != nil {
		return nil, err
	}
	sources, err := r.sourceReferences(ctx, 0)
	if err != nil {
		return nil, err
	}
	type rootKey struct {
		tenant uint64
		id     string
	}
	indices := map[string]int{}
	grants := map[rootKey]string{}
	out := make([]SpaceSummary, 0, len(spaces))
	for _, space := range spaces {
		status := spaceStatus(space)
		readable := status == "ready"
		indices[space.ID] = len(out)
		out = append(out, SpaceSummary{Space: space, Status: status, Readable: readable, UsageComplete: true})
	}
	for _, root := range roots {
		if i, ok := indices[root.SpaceID]; ok {
			out[i].AuthorizedRootCount++
			if root.Enabled {
				out[i].EnabledRootCount++
			}
			grants[rootKey{root.TenantID, root.ID}] = root.SpaceID
		}
	}
	for _, source := range sources {
		rootID, complete := source.rootID()
		if !complete {
			for i := range out {
				out[i].UsageComplete = false
			}
			continue
		}
		if spaceID, ok := grants[rootKey{source.TenantID, rootID}]; ok {
			out[indices[spaceID]].DataSourceCount++
		}
	}
	return out, nil
}

func spaceStatus(space Space) string {
	f, err := openRegisteredRoot(Root{SpacePath: space.Path})
	if errors.Is(err, ErrRootUnsafe) {
		return "unsafe"
	}
	if err != nil {
		return "unavailable"
	}
	defer f.Close()
	h, err := f.Open(".")
	if err != nil {
		return "unavailable"
	}
	defer h.Close()
	_, err = h.ReadDir(1)
	if err == nil || errors.Is(err, io.EOF) {
		return "ready"
	}
	return "unavailable"
}
func readableSpace(space Space) bool { return spaceStatus(space) == "ready" }

type DirectoryEntry struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
}
type DirectoryPage struct {
	Directory  string           `json:"directory"`
	Entries    []DirectoryEntry `json:"entries"`
	HasMore    bool             `json:"has_more"`
	NextOffset int              `json:"next_offset"`
	// A very large directory is bounded, rather than allocating arbitrary memory.
	// Users can enter a more specific relative directory when this is true.
	Truncated bool `json:"truncated"`
}

func (r *Registry) BrowseSpace(ctx context.Context, id, directory string, offset int) (*DirectoryPage, error) {
	if id == "" || len(id) > 128 || !validDirectory(directory) || offset < 0 || offset > directoryScanLimit {
		return nil, ErrRootInvalid
	}
	var space Space
	if err := r.db.WithContext(ctx).First(&space, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return browseDirectories(ctx, Root{SpacePath: space.Path}, directory, offset)
}

// Root browsing is safe for workspace datasource administrators: it always
// rereads the current enabled grant, and returns no physical/space path.
func (r *Registry) BrowseRoot(ctx context.Context, tenant uint64, id, directory string, offset int) (*DirectoryPage, error) {
	if tenant == 0 || id == "" || len(id) > 128 || !validDirectory(directory) || offset < 0 || offset > directoryScanLimit {
		return nil, ErrRootInvalid
	}
	var root Root
	if err := r.db.WithContext(ctx).First(&root, "tenant_id = ? AND id = ? AND enabled = ?", tenant, id, true).Error; err != nil {
		return nil, err
	}
	var space Space
	if err := r.db.WithContext(ctx).First(&space, "id = ?", root.SpaceID).Error; err != nil {
		return nil, err
	}
	root.SpacePath = space.Path
	return browseDirectories(ctx, root, directory, offset)
}

func browseDirectories(ctx context.Context, root Root, directory string, offset int) (*DirectoryPage, error) {
	if !validDirectory(directory) || offset < 0 || offset > directoryScanLimit {
		return nil, ErrRootInvalid
	}
	parent, err := openRegisteredRoot(root)
	if errors.Is(err, ErrRootUnsafe) {
		return nil, ErrRootUnsafe
	}
	if err != nil {
		return nil, ErrRootUnavailable
	}
	defer parent.Close()
	child, err := descend(parent, directory)
	if errors.Is(err, ErrRootUnsafe) {
		return nil, ErrRootUnsafe
	}
	if err != nil {
		return nil, ErrRootUnavailable
	}
	defer child.Close()
	f, err := child.Open(".")
	if err != nil {
		return nil, ErrRootUnavailable
	}
	defer f.Close()
	entries := []DirectoryEntry{}
	scanned := 0
	truncated := false
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		batch, e := f.ReadDir(256)
		if e != nil && !errors.Is(e, io.EOF) {
			return nil, ErrRootUnavailable
		}
		for _, item := range batch {
			scanned++
			if scanned > directoryScanLimit {
				truncated = true
				break
			}
			if !listedDirectoryName(item.Name()) {
				continue
			}
			// Lstat relative to the held directory rejects links, including a link
			// swapped in after ReadDir. Traversal revalidates every component again.
			info, statErr := child.Lstat(item.Name())
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				continue
			}
			entries = append(entries, DirectoryEntry{Name: item.Name(), Directory: path.Join(directory, item.Name())})
		}
		if truncated || errors.Is(e, io.EOF) {
			break
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	start := min(offset, len(entries))
	end := min(start+directoryPageSize, len(entries))
	return &DirectoryPage{Directory: directory, Entries: entries[start:end], HasMore: end < len(entries), NextOffset: end, Truncated: truncated}, nil
}

func listedDirectoryName(name string) bool {
	if !validDirectory(name) || strings.Contains(name, "/") || strings.HasPrefix(name, ".") {
		return false
	}
	switch strings.ToLower(name) {
	case ".git", ".hg", ".svn", "__pycache__", "lost+found", "$recycle.bin", "system volume information":
		return false
	}
	return true
}

type DiscoveredSpaces struct {
	Status    string           `json:"status"`
	Available bool             `json:"available"`
	Entries   []DirectoryEntry `json:"entries"`
	Truncated bool             `json:"truncated"`
}
type SpaceRegistration struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
}

func (r *Registry) discoveryBase() string {
	if r.discoveryPath != "" {
		return r.discoveryPath
	}
	return discoveryBoundary
}

// Discovery is not mounting: only direct, real directories that the deployment
// already exposed inside /source-roots can be considered. Nothing scans /home,
// the host's disks, Docker mounts, or a caller-controlled absolute path.
func (r *Registry) DiscoverSpaces(ctx context.Context) (*DiscoveredSpaces, error) {
	out := &DiscoveredSpaces{Status: spaceStatus(Space{Path: r.discoveryBase()}), Entries: []DirectoryEntry{}}
	spaces, err := r.Spaces(ctx)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, s := range spaces {
		registered[filepath.Clean(s.Path)] = true
	}
	page, err := browseDirectories(ctx, Root{SpacePath: r.discoveryBase()}, "", 0)
	if errors.Is(err, ErrRootUnavailable) || errors.Is(err, ErrRootUnsafe) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.Available = true
	out.Truncated = page.Truncated || page.HasMore
	for _, item := range page.Entries {
		candidate := filepath.Join(r.discoveryBase(), item.Name)
		if registered[filepath.Clean(candidate)] || !readableSpace(Space{Path: candidate}) {
			continue
		}
		out.Entries = append(out.Entries, item)
	}
	return out, nil
}
func (r *Registry) RegisterDiscoveredSpace(ctx context.Context, req SpaceRegistration) (*Space, error) {
	req.Name = strings.TrimSpace(req.Name)
	if !validName(req.Name) || req.Directory == "" || !listedDirectoryName(req.Directory) {
		return nil, ErrRootInvalid
	}
	base := r.discoveryBase()
	candidate := filepath.Join(base, req.Directory)
	// Check the entire held path, not just the leaf or a lexical prefix.
	f, err := openRegisteredRoot(Root{SpacePath: base, Directory: req.Directory})
	if errors.Is(err, ErrRootUnsafe) {
		return nil, ErrRootUnsafe
	}
	if err != nil {
		return nil, ErrRootUnavailable
	}
	defer f.Close()
	if !readableSpace(Space{Path: candidate}) {
		return nil, ErrRootUnavailable
	}
	sum := sha256.Sum256([]byte(candidate))
	space := Space{ID: "discovered-" + hex.EncodeToString(sum[:10]), Name: req.Name, Path: candidate}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old Space
		err := tx.First(&old, "path = ?", candidate).Error
		if err == nil {
			return ErrRootExists
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&space).Error
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil, ErrRootExists
	}
	return &space, err
}

// Counting references must not decrypt source credentials or fail JSON scanning
// before we can report an incomplete count. Physical access still fails closed.
type sourceReference struct {
	TenantID uint64
	Config   string
}

func (s sourceReference) rootID() (string, bool) {
	if s.Config == "" {
		return "", true
	}
	var cfg struct {
		Settings map[string]any `json:"settings"`
	}
	if json.Unmarshal([]byte(s.Config), &cfg) != nil {
		return "", false
	}
	value, exists := cfg.Settings["root_id"]
	if !exists {
		return "", true
	}
	id, ok := value.(string)
	return id, ok
}
func (r *Registry) sourceReferences(ctx context.Context, tenant uint64) ([]sourceReference, error) {
	rows := []sourceReference{}
	q := r.db.WithContext(ctx).Model(&types.DataSource{}).Select("tenant_id, config").Where("type = ?", Type)
	if tenant != 0 {
		q = q.Where("tenant_id = ?", tenant)
	}
	err := q.Scan(&rows).Error
	return rows, err
}
