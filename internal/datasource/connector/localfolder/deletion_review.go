package localfolder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/Tencent/WeKnora/internal/types"
)

// DeletionReview records what a complete scan observed, not authority to
// delete anything. A later manual run must re-observe the same set.
type DeletionReview struct {
	Digest string   `json:"digest"`
	Paths  []string `json:"paths"`
}

func deletionReview(selection string, files map[string]string, missing []string) *DeletionReview {
	paths := append([]string(nil), missing...)
	sort.Strings(paths)
	type item struct{ Path, Version string }
	items := make([]item, 0, len(paths))
	for _, p := range paths {
		items = append(items, item{Path: p, Version: files[p]})
	}
	b, _ := json.Marshal(struct {
		Selection string
		Items     []item
	}{selection, items})
	sum := sha256.Sum256(b)
	return &DeletionReview{Digest: hex.EncodeToString(sum[:]), Paths: paths}
}

// DeletionReviewFromCursor extracts a pending deletion review from a saved cursor.
func DeletionReviewFromCursor(cursor *types.SyncCursor) (*DeletionReview, error) {
	if cursor == nil || cursor.ConnectorCursor["deletion_review"] == nil {
		return nil, nil
	}
	b, err := json.Marshal(cursor.ConnectorCursor["deletion_review"])
	if err != nil {
		return nil, err
	}
	var review DeletionReview
	if err := json.Unmarshal(b, &review); err != nil || len(review.Digest) != 64 || len(review.Paths) == 0 {
		return nil, errors.New("invalid deletion review")
	}
	return &review, nil
}
