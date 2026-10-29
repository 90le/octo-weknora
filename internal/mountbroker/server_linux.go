//go:build linux

// Package mountbroker provides a narrowly scoped local host mount service.
// Its socket is intended only for the WeKnora application, which must still
// authenticate and authorize every request before forwarding it.
package mountbroker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

var rootIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Root identifies an allowed host directory.
type Root struct {
	ID   string `json:"id"`
	Path string `json:"-"`
}

// Mount records a managed read-only directory mount.
type Mount struct {
	ID        string `json:"id"`
	Root      string `json:"root"`
	Directory string `json:"directory"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Status    string `json:"status"`
}

// Server manages the broker mount state and local HTTP interface.
type Server struct {
	mu              sync.Mutex
	roots           map[string]string
	base, statePath string
	mounts          []Mount
}

// New validates fixed administrator-provided roots and reads only this
// service's state file. It never accepts a host absolute path from a request.
func New(roots map[string]string, base, statePath string) (*Server, error) {
	if len(roots) == 0 || !filepath.IsAbs(base) || filepath.Clean(base) == "/" || !filepath.IsAbs(statePath) {
		return nil, errors.New("mount broker configuration is invalid")
	}
	copyRoots := map[string]string{}
	for id, p := range roots {
		if !rootIDPattern.MatchString(id) || !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return nil, errors.New("mount broker root is invalid")
		}
		if overlaps(filepath.Clean(p), filepath.Clean(base)) ||
			overlaps(filepath.Clean(p), filepath.Dir(filepath.Clean(statePath))) {
			return nil, errors.New("mount control paths must be outside allowed roots")
		}
		copyRoots[id] = filepath.Clean(p)
	}
	s := &Server{roots: copyRoots, base: filepath.Clean(base), statePath: statePath, mounts: []Mount{}}
	if info, err := os.Lstat(statePath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("mount broker state is unsafe")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return nil, errors.New("mount broker state must be root owned")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	data, err := os.ReadFile(statePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(data) != 0 {
		if len(data) > 1<<20 || json.Unmarshal(data, &s.mounts) != nil || len(s.mounts) > 256 {
			return nil, errors.New("mount broker state is invalid")
		}
		for _, m := range s.mounts {
			if _, ok := s.roots[m.Root]; !ok || !safeRelative(m.Directory) || m.ID != mountID(m.Root, m.Directory) {
				return nil, errors.New("mount broker state is invalid")
			}
		}
	}
	return s, nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) ||
		strings.HasPrefix(b, a+string(os.PathSeparator))
}

// ValidateSharedBase is a startup gate: Docker must receive future child mount
// events, otherwise a successful host mount would appear empty in the app.
func ValidateSharedBase(base string) error {
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("mount base is unavailable or unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("mount base must be root owned")
	}
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || fields[4] != base {
			continue
		}
		for _, field := range fields[6:] {
			if field == "-" {
				break
			}
			if strings.HasPrefix(field, "shared:") {
				return nil
			}
		}
	}
	return errors.New("mount base is not a shared mount")
}

func safeRelative(p string) bool {
	if p == "" {
		return true
	}
	if len(p) > 2048 || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.ContainsAny(p, "\\:\x00\r\n\t") {
		return false
	}
	for _, component := range strings.Split(p, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") {
			return false
		}
	}
	return true
}

func mountID(root, dir string) string {
	h := sha256.Sum256([]byte(root + "\x00" + dir))
	return "m-" + hex.EncodeToString(h[:10])
}

// openDirectory rejects links at every component and compares identities after
// opening. The resulting held fd, not the caller's path, is used for mounting.
func (s *Server) openDirectory(id, dir string) (*os.Root, error) {
	p, ok := s.roots[id]
	if !ok || !safeRelative(dir) {
		return nil, errors.New("invalid root or directory")
	}
	r, err := os.OpenRoot(p)
	if err != nil {
		return nil, errors.New("allowed root unavailable")
	}
	for _, part := range strings.Split(dir, "/") {
		if part == "" {
			continue
		}
		before, e := r.Lstat(part)
		if e != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			_ = r.Close()
			return nil, errors.New("directory unavailable or unsafe")
		}
		next, e := r.OpenRoot(part)
		if e != nil {
			_ = r.Close()
			return nil, errors.New("directory unavailable or unsafe")
		}
		after, e := next.Stat(".")
		_ = r.Close()
		if e != nil || !os.SameFile(before, after) {
			_ = next.Close()
			return nil, errors.New("directory changed")
		}
		r = next
	}
	return r, nil
}

func identity(r *os.Root) (uint64, uint64, error) {
	info, err := r.Stat(".")
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("directory identity unavailable")
	}
	return uint64(st.Dev), st.Ino, nil
}

func (s *Server) save() error {
	b, err := json.Marshal(s.mounts)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.statePath), ".mounts-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, s.statePath)
}

func (s *Server) mount(m *Mount) error {
	r, err := s.openDirectory(m.Root, m.Directory)
	if err != nil {
		return err
	}
	defer r.Close()
	dev, ino, err := identity(r)
	if err != nil || dev != m.Device || ino != m.Inode {
		return errors.New("source identity changed")
	}
	target := filepath.Join(s.base, m.ID)
	if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("mount target unsafe")
	}
	// A pre-existing mounted target must be the same held source and read-only.
	if mounted, ro := mountStatus(target); mounted {
		actual, e := os.Stat(target)
		source, se := r.Stat(".")
		if e != nil || se != nil || !os.SameFile(actual, source) || !ro {
			return errors.New("existing mount does not match")
		}
		return nil
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		return errors.New("mount target is not empty")
	}
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	// Prepare the detached mount read-only before publishing it into the
	// shared tree. A traditional bind followed by remount would expose a brief
	// writable interval to the application container.
	tree, err := unix.OpenTree(int(f.Fd()), "", unix.OPEN_TREE_CLONE|unix.AT_EMPTY_PATH|unix.O_CLOEXEC)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(tree) }()
	attr := &unix.MountAttr{
		Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC,
	}
	if err := unix.MountSetattr(tree, "", unix.AT_EMPTY_PATH, attr); err != nil {
		return err
	}
	return unix.MoveMount(tree, "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// mountStatus parses kernel mountinfo; service-managed IDs contain no spaces.
func mountStatus(target string) (bool, bool) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && fields[4] == target {
			return true, strings.Contains(","+fields[5]+",", ",ro,")
		}
	}
	return false, false
}

// Restore re-establishes only entries recorded by this broker; a changed host
// directory identity fails closed and remains unavailable until reviewed.
func (s *Server) Restore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.mounts {
		s.mounts[i].Status = "unavailable"
		if s.mount(&s.mounts[i]) == nil {
			s.mounts[i].Status = "ready"
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && r.URL.Path == "/v1/roots" {
		roots := make([]Root, 0, len(s.roots))
		for id := range s.roots {
			roots = append(roots, Root{ID: id})
		}
		sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
		_ = json.NewEncoder(w).Encode(roots)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/browse" {
		s.browse(w, r)
		return
	}
	if r.URL.Path == "/v1/mounts" && r.Method == http.MethodGet {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s.mounts)
		return
	}
	if r.URL.Path == "/v1/mounts" && r.Method == http.MethodPost {
		s.add(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/mounts/") && r.Method == http.MethodDelete {
		s.remove(w, strings.TrimPrefix(r.URL.Path, "/v1/mounts/"))
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func (s *Server) remove(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.mounts {
		if s.mounts[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		http.Error(w, "mount not found", http.StatusNotFound)
		return
	}
	m := s.mounts[index]
	target := filepath.Join(s.base, m.ID)
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "mount target unsafe", http.StatusConflict)
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "mount target unavailable", http.StatusConflict)
		return
	}
	if mounted, ro := mountStatus(target); mounted {
		info, err := os.Stat(target)
		if err != nil || !ro {
			http.Error(w, "mount identity unavailable", http.StatusConflict)
			return
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || uint64(stat.Dev) != m.Device || stat.Ino != m.Inode {
			http.Error(w, "mount identity changed", http.StatusConflict)
			return
		}
		if err := unix.Unmount(target, 0); err != nil {
			http.Error(w, "mount is in use", http.StatusConflict)
			return
		}
	}
	previous := s.mounts
	s.mounts = append(append([]Mount{}, previous[:index]...), previous[index+1:]...)
	if err := s.save(); err != nil {
		s.mounts = previous
		_ = s.mount(&s.mounts[index])
		http.Error(w, "mount state unavailable", http.StatusInternalServerError)
		return
	}
	// Remove only an empty broker-owned mountpoint. Never recursively delete.
	_ = os.Remove(target)
	_ = json.NewEncoder(w).Encode(map[string]bool{"removed": true, "source_files_preserved": true})
}

func (s *Server) browse(w http.ResponseWriter, req *http.Request) {
	dir := req.URL.Query().Get("directory")
	offset, err := strconv.Atoi(req.URL.Query().Get("offset"))
	if req.URL.Query().Get("offset") == "" {
		offset, err = 0, nil
	}
	if err != nil || offset < 0 || offset > 10000 {
		http.Error(w, "invalid offset", http.StatusBadRequest)
		return
	}
	r, err := s.openDirectory(req.URL.Query().Get("root"), dir)
	if err != nil {
		http.Error(w, "directory unavailable or unsafe", http.StatusBadRequest)
		return
	}
	defer r.Close()
	f, err := r.Open(".")
	if err != nil {
		http.Error(w, "directory unavailable", http.StatusBadRequest)
		return
	}
	defer func() { _ = f.Close() }()
	type entry struct {
		Name      string `json:"name"`
		Directory string `json:"directory"`
	}
	entries := []entry{}
	truncated := false
	for scanned := 0; scanned < 10000; {
		batch, e := f.ReadDir(256)
		if e != nil && !errors.Is(e, io.EOF) {
			http.Error(w, "directory unavailable", http.StatusBadRequest)
			return
		}
		for _, item := range batch {
			scanned++
			if scanned > 10000 {
				break
			}
			p := path.Join(dir, item.Name())
			if !safeRelative(p) {
				continue
			}
			info, err := r.Lstat(item.Name())
			if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				entries = append(entries, entry{Name: item.Name(), Directory: p})
			}
		}
		if scanned >= 10000 && !errors.Is(e, io.EOF) {
			truncated = true
		}
		if errors.Is(e, io.EOF) || scanned >= 10000 {
			break
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	start := min(offset, len(entries))
	end := min(start+200, len(entries))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"directory": dir, "entries": entries[start:end], "truncated": truncated,
		"has_more": end < len(entries), "next_offset": end,
	})
}

func (s *Server) add(w http.ResponseWriter, req *http.Request) {
	defer func() { _ = req.Body.Close() }()
	var input struct {
		Root      string `json:"root"`
		Directory string `json:"directory"`
	}
	d := json.NewDecoder(io.LimitReader(req.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || !safeRelative(input.Directory) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mounts) >= 256 {
		http.Error(w, "mount limit reached", http.StatusConflict)
		return
	}
	id := mountID(input.Root, input.Directory)
	for _, m := range s.mounts {
		if m.ID == id {
			http.Error(w, "mount already exists", http.StatusConflict)
			return
		}
	}
	r, err := s.openDirectory(input.Root, input.Directory)
	if err != nil {
		http.Error(w, "directory unavailable or unsafe", http.StatusBadRequest)
		return
	}
	dev, ino, err := identity(r)
	r.Close()
	if err != nil {
		http.Error(w, "directory unavailable", http.StatusBadRequest)
		return
	}
	m := Mount{ID: id, Root: input.Root, Directory: input.Directory, Device: dev, Inode: ino, Status: "pending"}
	// Persist before mounting so a crash cannot leave an untracked active mount.
	s.mounts = append(s.mounts, m)
	if err := s.save(); err != nil {
		s.mounts = s.mounts[:len(s.mounts)-1]
		http.Error(w, "mount state unavailable", http.StatusInternalServerError)
		return
	}
	if err := s.mount(&m); err != nil {
		// Keep the recorded entry for explicit operator recovery if mounting or
		// cleanup fails; do not delete or reuse unknown filesystem paths.
		s.mounts[len(s.mounts)-1].Status = "unavailable"
		_ = s.save()
		http.Error(w, "mount unavailable", http.StatusBadRequest)
		return
	}
	m.Status = "ready"
	s.mounts[len(s.mounts)-1] = m
	_ = s.save()
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(m)
}
