package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
)

// documentExcludes reads only explicit document-mode exclusions. The source
// snapshot's defaults are deliberately not applied to legacy nil/null/empty
// settings: older document sources never honored user exclusions, and a new
// default must not silently narrow their published scope.
func documentExcludes(cfg *types.DataSourceConfig) ([]string, error) {
	if err := snapshot.ValidateSettings(cfg); err != nil {
		return nil, err
	}
	if cfg.Settings == nil || cfg.Settings["exclude"] == nil {
		return nil, nil
	}
	b, err := json.Marshal(cfg.Settings["exclude"])
	if err != nil || string(b) == "null" {
		return nil, err
	}
	var rules []string
	if err := json.Unmarshal(b, &rules); err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, nil
	}
	for _, rule := range rules {
		if _, err := path.Match(rule, ""); err != nil {
			return nil, &Error{Code: "github_exclusion_invalid", Message: "Invalid GitHub document exclusion glob"}
		}
	}
	sort.Strings(rules)
	return rules, nil
}

// documentSelectionKey retains the previous on-disk fingerprint when there
// are no user exclusions. Non-empty exclusions get a distinct, sorted key so
// an unchanged remote commit cannot take the fast path after the user changes
// the document scope. The cursor is acknowledged only after indexing succeeds.
func documentSelectionKey(s selection, excludes []string) string {
	b, _ := json.Marshal(s)
	if len(excludes) > 0 {
		b, _ = json.Marshal(struct {
			Selection selection `json:"selection"`
			Exclude   []string  `json:"exclude"`
		}{Selection: s, Exclude: excludes})
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func documentCursorReusable(cfg *types.DataSourceConfig, prev cursor, key, commit string) bool {
	return cfg != nil && cfg.SyncSource != nil && prev.Selection == key && prev.Commit == commit
}

func documentInCurrentScope(e entry, s selection, excludes []string) bool {
	return allowedGitHubDocument(e) && selected(e.Path, s.Paths) && !snapshot.Excluded(e.Path, excludes)
}

func documentHistoricalOutOfScope(p string, s selection, excludes []string) bool {
	return !selected(p, s.Paths) || snapshot.Excluded(p, excludes)
}
