// Package snapshot provides language-independent, read-only text snapshots.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func ValidateSettings(config *types.DataSourceConfig) error {
	if config == nil {
		return errors.New("source configuration missing")
	}
	if value, exists := config.Settings["mode"]; exists {
		mode, ok := value.(string)
		if !ok || (mode != "" && mode != "documents" && mode != "source") {
			return errors.New("source mode must be documents or source")
		}
	}
	if value, exists := config.Settings["exclude"]; exists && !isJSONNull(value) {
		var rules []string
		b, err := json.Marshal(value)
		if err != nil || json.Unmarshal(b, &rules) != nil || len(rules) > 100 {
			return errors.New("exclusions must be a list of at most 100 paths")
		}
		for _, rule := range rules {
			if len(rule) > 1024 || strings.ContainsAny(rule, "\\\x00\r\n") {
				return errors.New("invalid exclusion rule")
			}
		}
	}
	return nil
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
	if config == nil || config.Settings == nil {
		return append([]string(nil), DefaultExcludes...)
	}
	value, ok := config.Settings["exclude"]
	if !ok || isJSONNull(value) {
		return append([]string(nil), DefaultExcludes...)
	}
	b, _ := json.Marshal(value)
	var rules []string
	if json.Unmarshal(b, &rules) != nil {
		return append([]string(nil), DefaultExcludes...)
	}
	return rules
}

// isJSONNull treats both a decoded JSON null and a typed nil slice/map as an
// absent optional setting. Legacy batch rows stored exclude:null, so they need
// the same default exclusion policy as rows where the setting was omitted.
func isJSONNull(value interface{}) bool {
	if value == nil {
		return true
	}
	b, err := json.Marshal(value)
	return err == nil && string(b) == "null"
}
func DocumentPath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown", ".mdx", ".txt", ".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx", ".csv", ".html", ".htm", ".epub", ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}
