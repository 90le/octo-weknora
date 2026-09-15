// Package snapshot provides language-independent, read-only text snapshots.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const MaxFiles = 50000
const MaxFileBytes = 4 << 20
const MaxTotalBytes = 256 << 20

var DefaultExcludes = []string{"node_modules", "vendor", "dist", "build", ".venv", "__pycache__", "coverage"}

func IsSource(config *types.DataSourceConfig) bool {
	return config != nil && config.Settings["mode"] == "source"
}
func Selection(config *types.DataSourceConfig) string {
	b, _ := json.Marshal(config.Settings)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func SafePath(p string) bool {
	return p != "" && p != "." && p != ".." && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") && path.Clean(p) == p && !strings.ContainsAny(p, "\\\x00\r\n")
}
func Excluded(p string, excludes []string) bool {
	if !SafePath(p) {
		return true
	}
	for _, part := range strings.Split(p, "/") {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == ".hg" || lower == ".svn" || lower == "secrets" || lower == "credentials.json" || lower == ".npmrc" || lower == ".pypirc" || lower == ".netrc" || lower == "id_rsa" || lower == "id_ed25519" || lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") {
			return true
		}
		for _, rule := range excludes {
			if part == rule {
				return true
			}
		}
	}
	for _, rule := range excludes {
		if rule != "" && (p == rule || strings.HasPrefix(p, strings.TrimSuffix(rule, "/")+"/")) {
			return true
		}
		if ok, _ := path.Match(rule, p); ok {
			return true
		}
	}
	return false
}
func Excludes(config *types.DataSourceConfig) []string {
	if _, ok := config.Settings["exclude"]; !ok {
		return append([]string(nil), DefaultExcludes...)
	}
	b, _ := json.Marshal(config.Settings["exclude"])
	var rules []string
	_ = json.Unmarshal(b, &rules)
	return rules
}
func DocumentPath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown", ".mdx", ".txt", ".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx", ".csv", ".html", ".htm", ".epub", ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}
