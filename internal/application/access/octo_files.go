package access

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// CanonicalLocalLocator returns the local driver's lexical equivalent while
// keeping an explicit backend ID. It never cleans cloud object keys. Paths
// that leave the tenant root are rejected before a second registry lookup.
func CanonicalLocalLocator(tenantID uint64, reference string) (string, bool, error) {
	inner := reference
	backendID, scopedPath, scoped := types.ParseStorageBackendPath(reference)
	if scoped {
		inner = scopedPath
	}
	if !strings.HasPrefix(inner, "local://") {
		return "", false, nil
	}
	relative := strings.TrimLeft(strings.TrimPrefix(inner, "local://"), "/")
	slashPath := filepath.ToSlash(filepath.FromSlash(relative))
	if tenantID == 0 || relative == "" || filepath.IsAbs(filepath.FromSlash(relative)) {
		return "", true, ErrForbidden
	}
	first, _, hasFile := strings.Cut(slashPath, "/")
	if !hasFile || first != strconv.FormatUint(tenantID, 10) {
		return "", true, ErrForbidden
	}
	// Never let a lexical step leave the tenant root, even when a later
	// component would re-enter the same tenant.
	depth := 0
	for _, segment := range strings.Split(slashPath, "/") {
		switch segment {
		case "", ".":
		case "..":
			if depth <= 1 {
				return "", true, ErrForbidden
			}
			depth--
		default:
			depth++
		}
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	first, _, hasFile = strings.Cut(clean, "/")
	if !hasFile || first != strconv.FormatUint(tenantID, 10) {
		return "", true, ErrForbidden
	}
	canonical := "local://" + clean
	if scoped {
		canonical = types.BuildStorageBackendPath(backendID, canonical)
	}
	return canonical, true, nil
}

// OctoArtifactAccess is optional for catalog implementations without the
// resource registry. Errors are never interpreted as authorization.
type OctoArtifactAccess interface {
	CheckOctoArtifactAccess(context.Context, uint64, string) (matched, allowed bool, err error)
}

func CheckOctoFile(ctx context.Context, catalog interface{}, tenantID uint64, reference string) (bool, error) {
	checker, ok := catalog.(OctoArtifactAccess)
	if !ok {
		return false, nil
	}
	matched, allowed, err := checker.CheckOctoArtifactAccess(ctx, tenantID, reference)
	if err != nil {
		return matched, err
	}
	if matched && !allowed {
		return true, ErrForbidden
	}
	return matched, nil
}
