package service

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
)

type octoArtifactRepository interface {
	OctoArtifactChannels(context.Context, uint64, *types.StoredResource) ([]string, error)
	OctoArtifactReferences(context.Context, uint64, []string, string) (map[string][]string, error)
	LocalResourcesAtLocator(context.Context, uint64, string) ([]*types.StoredResource, error)
	ProviderResourcesAtLocator(context.Context, uint64, string) ([]*types.StoredResource, error)
	OctoWebOwnerExists(context.Context, uint64, string, string) (bool, error)
}

// CheckOctoArtifactAccess applies only to traceable artifacts. A false match
// does not establish that an unregistered or legacy locator is safe.
func (s *resourceCatalog) CheckOctoArtifactAccess(ctx context.Context, tenantID uint64, ref string) (bool, bool, error) {
	repo, ok := s.repo.(octoArtifactRepository)
	if !ok {
		return false, false, errors.New("artifact origin lookup unavailable")
	}
	canonical, local, err := access.CanonicalLocalLocator(tenantID, ref)
	if err != nil {
		return false, false, err
	}
	_, resource, err := s.ResolvePath(ctx, ref)
	if err != nil {
		return false, false, err
	}
	if resource == nil {
		resource, err = s.repo.GetByTenantLocation(ctx, tenantID, resourceLocationHash(ref))
		if err != nil {
			return false, false, err
		}
	}
	if resource == nil && local && canonical != ref {
		resource, err = s.repo.GetByTenantLocation(ctx, tenantID, resourceLocationHash(canonical))
		if err != nil {
			return false, false, err
		}
	}
	if resource != nil && resource.TenantID != tenantID {
		return false, false, nil
	}
	var candidates []*types.StoredResource
	seen := map[string]bool{}
	add := func(item *types.StoredResource) {
		if item != nil && item.TenantID == tenantID && !seen[item.ID] {
			seen[item.ID] = true
			candidates = append(candidates, item)
		}
	}
	add(resource)
	// A handle's registered physical path, or a raw path after cleaning,
	// must also be compared to other local backend registrations.
	localPath := canonical
	if resource != nil {
		localPath, local, err = access.CanonicalLocalLocator(tenantID, resource.PhysicalPath)
		if err != nil {
			return false, false, err
		}
	}
	if local {
		if _, inner, scoped := types.ParseStorageBackendPath(localPath); scoped {
			localPath = inner
		}
		equivalent, err := repo.LocalResourcesAtLocator(ctx, tenantID, localPath)
		if err != nil {
			return false, false, err
		}
		for _, item := range equivalent {
			add(item)
		}
	} else {
		providerPath := ref
		if resource != nil {
			providerPath = resource.PhysicalPath
		}
		equivalent, err := repo.ProviderResourcesAtLocator(ctx, tenantID, providerPath)
		if err != nil {
			return false, false, err
		}
		for _, item := range equivalent {
			add(item)
		}
	}
	caller := types.CallerFromContext(ctx)
	principal, principalOK := ctx.Value(types.PrincipalContextKey).(types.Principal)
	_, apiKey := types.TenantAPIKeyScopeFromContext(ctx)
	references := []string{ref}
	for _, item := range candidates {
		references = append(references, types.BuildResourcePath(item.Handle), item.PhysicalPath)
	}
	if !local {
		localPath = ""
	}
	origins, err := repo.OctoArtifactReferences(ctx, tenantID, references, localPath)
	if err != nil {
		return false, false, err
	}
	// A raw locator with neither a registry entry nor a persisted channel
	// origin has no evidence from which this proxy can establish ownership.
	// Independently authorized KB routes do not use this generic decision.
	if len(candidates) == 0 && len(origins) == 0 {
		return true, false, nil
	}
	matched := false
	for _, channels := range origins {
		matched = true
		if apiKey || !principalOK || principal.Type != types.PrincipalWebUser || principal.ID == "" || principal.ID != caller.UserID || caller.TenantID != tenantID {
			return true, false, nil
		}
		allowed := false
		for _, channel := range channels {
			if channel == "" {
				continue
			}
			owner, err := repo.OctoWebOwnerExists(ctx, tenantID, channel, principal.ID)
			if err != nil {
				return true, false, err
			}
			allowed = allowed || owner
		}
		if !allowed {
			return true, false, nil
		}
	}
	for _, item := range candidates {
		channels, err := repo.OctoArtifactChannels(ctx, tenantID, item)
		if err != nil {
			return false, false, err
		}
		if len(channels) == 0 {
			continue
		}
		matched = true
		if apiKey || !principalOK || principal.Type != types.PrincipalWebUser || principal.ID == "" || principal.ID != caller.UserID || caller.TenantID != tenantID {
			return true, false, nil
		}
		allowed := false
		for _, channel := range channels {
			if channel == "" {
				continue
			}
			owner, err := repo.OctoWebOwnerExists(ctx, tenantID, channel, principal.ID)
			if err != nil {
				return true, false, err
			}
			if owner {
				allowed = true
				break
			}
		}
		if !allowed {
			return true, false, nil
		}
	}
	return matched, matched, nil
}
