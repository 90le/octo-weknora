//go:build linux

package mountbroker

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowseIsConfinedAndDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "allowed")
	base := filepath.Join(dir, "mounts")
	state := filepath.Join(dir, "state", "mounts.json")
	for _, p := range []string{root, base, filepath.Dir(state), filepath.Join(root, "visible"), filepath.Join(root, ".private")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	s, err := New(map[string]string{"E": root}, base, state)
	if err != nil {
		t.Fatal(err)
	}
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		return w
	}
	w := get("/v1/browse?root=E&directory=")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "visible") || strings.Contains(w.Body.String(), "outside") || strings.Contains(w.Body.String(), ".private") || strings.Contains(w.Body.String(), root) {
		t.Fatalf("unexpected browse result: %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{"root=E&directory=..%2Foutside", "root=E&directory=outside", "root=other&directory=", "root=E&directory=%2Fetc"} {
		if w := get("/v1/browse?" + query); w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe browse accepted: %s: %d", query, w.Code)
		}
	}
}

func TestConfigurationRejectsOverlappingAndForgedState(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "allowed")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(map[string]string{"E": root}, filepath.Join(root, "mounts"), filepath.Join(dir, "state.json")); err == nil {
		t.Fatal("overlapping mount root accepted")
	}
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`[{"id":"m-forged","root":"E","directory":"data"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(map[string]string{"E": root}, filepath.Join(dir, "mounts"), state); err == nil {
		t.Fatal("forged mount identity accepted")
	}
}
