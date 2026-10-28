package config

import (
	"os"
	"strings"
)

// PreserveDeletingKnowledge keeps pre-existing deletion states untouched during an Octo upgrade.
// The operator must separately ensure that no queued delete task can execute.
func PreserveDeletingKnowledge() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("OCTO_PRESERVE_DELETING_KNOWLEDGE")), "true")
}
