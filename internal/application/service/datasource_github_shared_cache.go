package service

import (
	"context"
	"errors"
	"strings"

	githubConnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type githubTenantSubscriberRepository interface {
	FindGitHubByTenant(context.Context, uint64) ([]*types.DataSource, error)
}

// cleanupGitHubSharedCache consults the database under the physical cache
// lock. A source's old config is passed after update/delete so a twin source
// retains the mirror. If identity cannot be verified, keeping bytes is safer
// than removing an object store that another source may still be reading.
func cleanupGitHubSharedCache(ctx context.Context, repo interfaces.DataSourceRepository, old *types.DataSource) error {
	if old == nil || old.Type != types.ConnectorTypeGitHub ||
		(!githubConnector.SharedGitCacheEnabled() && !githubConnector.SharedGitCachePresent()) {
		return nil
	}
	lister, ok := repo.(githubTenantSubscriberRepository)
	if !ok {
		return errors.New("GitHub shared cache subscriber repository is unavailable")
	}
	oldConfig, err := old.ParseConfig()
	if err != nil {
		return err
	}
	repository, ok := githubConnector.ConfiguredRepository(oldConfig)
	if !ok {
		return errors.New("old GitHub repository identity is unavailable")
	}
	scope, err := githubDocumentCredentialScope(old, oldConfig)
	if err != nil {
		return err
	}
	return githubConnector.CleanupSharedGitCache(ctx, old.TenantID, repository, scope, func(ctx context.Context) (bool, error) {
		sources, err := lister.FindGitHubByTenant(ctx, old.TenantID)
		if err != nil {
			return true, err
		}
		for _, source := range sources {
			if source == nil || source.TenantID != old.TenantID || source.Type != types.ConnectorTypeGitHub {
				return true, errors.New("GitHub shared cache subscriber listing is invalid")
			}
			config, err := source.ParseConfig()
			if err != nil {
				return true, err
			}
			candidate, ok := githubConnector.ConfiguredRepository(config)
			if !ok {
				return true, errors.New("GitHub shared cache subscriber identity is invalid")
			}
			if !strings.EqualFold(candidate, repository) {
				continue
			}
			candidateScope, err := githubDocumentCredentialScope(source, config)
			if err != nil {
				return true, err
			}
			if candidateScope == scope {
				return true, nil
			}
		}
		return false, nil
	})
}
