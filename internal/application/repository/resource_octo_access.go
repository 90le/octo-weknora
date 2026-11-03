package repository

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
)

// OctoArtifactReferences finds artifact rows independently of resource bindings.
// Local backends can expose the same file through different directory prefixes.
func (r *resourceRepository) OctoArtifactReferences(ctx context.Context, tenantID uint64, references []string, localLocator string) (map[string][]string, error) {
	terms := make([]string, 0, len(references)+1)
	args := make([]interface{}, 0, len(references)+1)
	for _, ref := range references {
		terms = append(terms, "a.url = ?")
		args = append(args, ref)
		if _, inner, scoped := types.ParseStorageBackendPath(ref); scoped {
			ref = inner
		}
		if types.ParseProviderScheme(ref) != "" && !strings.HasPrefix(ref, "local://") {
			escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(ref)
			terms = append(terms, "a.url LIKE ? ESCAPE '!'")
			args = append(args, "%"+escaped)
		}
	}
	if localLocator != "" {
		base := localLocator[strings.LastIndex(localLocator, "/")+1:]
		escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(base)
		terms = append(terms, "a.url LIKE ? ESCAPE '!'")
		args = append(args, "%"+escaped+"%")
	}
	if len(terms) == 0 {
		return nil, nil
	}
	var rows []struct {
		URL       string
		ChannelID string
	}
	err := r.db.WithContext(ctx).Table("message_artifacts AS a").
		Select("a.url, cs.im_channel_id AS channel_id").
		Joins("JOIN messages AS m ON m.id = a.message_id AND m.session_id = a.session_id").
		Joins("JOIN sessions AS s ON s.id = m.session_id AND s.tenant_id = ?", tenantID).
		Joins("JOIN im_channel_sessions AS cs ON cs.session_id = s.id AND cs.tenant_id = s.tenant_id AND cs.platform = ?", "octo").
		Where("("+strings.Join(terms, " OR ")+")", args...).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make(map[string][]string)
	for _, row := range rows {
		match := false
		for _, ref := range references {
			if row.URL == ref {
				match = true
				break
			}
			rowPath := row.URL
			if _, inner, scoped := types.ParseStorageBackendPath(rowPath); scoped {
				rowPath = inner
			}
			if _, inner, scoped := types.ParseStorageBackendPath(ref); scoped {
				ref = inner
			}
			if types.ParseProviderScheme(ref) != "" && !strings.HasPrefix(ref, "local://") && rowPath == ref {
				match = true
				break
			}
		}
		if !match && localLocator != "" {
			canonical, local, err := access.CanonicalLocalLocator(tenantID, row.URL)
			if local && err != nil {
				return nil, err
			}
			if local {
				if _, inner, scoped := types.ParseStorageBackendPath(canonical); scoped {
					canonical = inner
				}
				a := strings.TrimPrefix(canonical, "local://")
				b := strings.TrimPrefix(localLocator, "local://")
				match = a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
			}
		}
		if match {
			result[row.URL] = append(result[row.URL], row.ChannelID)
		}
	}
	return result, nil
}

// LocalResourcesAtLocator finds canonical local locators whose path tails
// could denote the same file under different backend directory prefixes. One
// relative path must be a suffix of the other for such prefixes to coincide.
// The basename narrows the query; exact comparisons avoid LIKE wildcard and
// collation behavior becoming authorization evidence.
func (r *resourceRepository) LocalResourcesAtLocator(ctx context.Context, tenantID uint64, locator string) ([]*types.StoredResource, error) {
	relative := strings.TrimPrefix(locator, "local://")
	slash := strings.LastIndex(relative, "/")
	if slash < 0 || slash == len(relative)-1 {
		return nil, nil
	}
	basename := relative[slash+1:]
	escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(basename)
	var resources []*types.StoredResource
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND provider = ? AND state = ? AND physical_path LIKE ? ESCAPE '!'", tenantID, "local", types.ResourceStateActive, "%"+escaped+"%").Find(&resources).Error
	if err != nil {
		return nil, err
	}
	out := make([]*types.StoredResource, 0, len(resources))
	for _, resource := range resources {
		physical, local, err := access.CanonicalLocalLocator(tenantID, resource.PhysicalPath)
		if err != nil {
			return nil, err
		}
		if !local {
			continue
		}
		if _, inner, ok := types.ParseStorageBackendPath(physical); ok {
			physical = inner
		}
		if !strings.HasPrefix(physical, "local://") {
			continue
		}
		candidate := strings.TrimPrefix(physical, "local://")
		if candidate == relative || strings.HasSuffix(candidate, "/"+relative) || strings.HasSuffix(relative, "/"+candidate) {
			out = append(out, resource)
		}
	}
	return out, nil
}

// ProviderResourcesAtLocator includes aliases registered with backend wrappers.
func (r *resourceRepository) ProviderResourcesAtLocator(ctx context.Context, tenantID uint64, locator string) ([]*types.StoredResource, error) {
	if _, inner, scoped := types.ParseStorageBackendPath(locator); scoped {
		locator = inner
	}
	if types.ParseProviderScheme(locator) == "" || strings.HasPrefix(locator, "local://") {
		return nil, nil
	}
	escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(locator)
	var resources []*types.StoredResource
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND state = ? AND physical_path LIKE ? ESCAPE '!'", tenantID, types.ResourceStateActive, "%"+escaped).Find(&resources).Error; err != nil {
		return nil, err
	}
	out := make([]*types.StoredResource, 0, len(resources))
	for _, resource := range resources {
		physical := resource.PhysicalPath
		if _, inner, scoped := types.ParseStorageBackendPath(physical); scoped {
			physical = inner
		}
		if physical == locator {
			out = append(out, resource)
		}
	}
	return out, nil
}

// OctoArtifactChannels also keeps a historical binding protective if its
// artifact row is missing; absence of that row is not proof of public access.
func (r *resourceRepository) OctoArtifactChannels(ctx context.Context, tenantID uint64, resource *types.StoredResource) ([]string, error) {
	var channels []string
	err := r.db.WithContext(ctx).Table("resource_bindings AS b").
		Joins("JOIN messages AS m ON m.id = b.owner_id").
		Joins("JOIN sessions AS s ON s.id = m.session_id AND s.tenant_id = b.tenant_id").
		Joins("JOIN im_channel_sessions AS cs ON cs.session_id = s.id AND cs.tenant_id = s.tenant_id AND cs.platform = ?", "octo").
		Where("b.tenant_id = ? AND b.resource_id = ? AND b.owner_type = ? AND b.relation = ?", tenantID, resource.ID, types.ResourceOwnerMessage, types.ResourceRelationArtifact).
		Distinct("cs.im_channel_id").Pluck("cs.im_channel_id", &channels).Error
	return channels, err
}

func (r *resourceRepository) OctoWebOwnerExists(ctx context.Context, tenantID uint64, channelID, userID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("im_channel_web_owners AS o").
		Joins("JOIN im_channels AS c ON c.id = o.im_channel_id AND c.tenant_id = o.tenant_id AND c.deleted_at IS NULL").
		Joins("JOIN tenant_members AS tm ON tm.tenant_id = o.tenant_id AND tm.user_id = o.web_user_id AND tm.status = ? AND tm.deleted_at IS NULL", "active").
		Joins("JOIN users AS u ON u.id = o.web_user_id AND u.is_active = ? AND u.deleted_at IS NULL", true).
		Where("o.tenant_id = ? AND o.im_channel_id = ? AND o.web_user_id = ? AND o.deleted_at IS NULL", tenantID, channelID, userID).Count(&count).Error
	return count > 0, err
}
