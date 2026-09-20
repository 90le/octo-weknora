package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Preserve all metadata until these deletions succeed. Catalog-bound objects
// shared with another KB or message are retained. A retry after a successful
// unlink treats a missing local file as success, not a permanent cleanup failure.
func (s *knowledgeBaseService) deleteKnowledgeBaseFiles(ctx context.Context, knowledge []*types.Knowledge, images []string, plan *interfaces.KBDeletionPlan) error {
	protected := map[string]bool{}
	known := map[string]bool{}
	candidates := map[string]bool{}
	if plan != nil {
		for _, entry := range plan.Resources {
			resource := entry.Resource
			ref := types.BuildResourcePath(resource.Handle)
			known[ref] = true
			known[resource.PhysicalPath] = true
			if entry.Shared || resource.DeletedAt.Valid || resource.State == types.ResourceStateDeleted {
				protected[ref] = true
				protected[resource.PhysicalPath] = true
			} else {
				candidates[ref] = true
			}
		}
	}
	for _, item := range knowledge {
		if item.FilePath != "" && !known[item.FilePath] {
			candidates[item.FilePath] = true
		}
	}
	for _, image := range images {
		if image != "" && !known[image] {
			candidates[image] = true
		}
	}
	for ref := range candidates {
		if protected[ref] {
			continue
		}
		// Original remote images are not application-owned storage objects.
		if strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") {
			continue
		}
		if s.fileSvc == nil {
			return errors.New("file service unavailable during KB cleanup")
		}
		if err := s.fileSvc.DeleteFile(ctx, ref); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete KB stored file: %w", err)
		}
	}
	return nil
}
