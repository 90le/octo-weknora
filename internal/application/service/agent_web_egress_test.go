package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestScopedWebEgressRequiresNativeIMAuthority(t *testing.T) {
	if scopedWebEgressGuard(withScopedWebEgress(context.Background(), "user question")) != nil {
		t.Fatal("ordinary sessions must not acquire an Octo web egress guard")
	}
	im := types.WithIMKnowledgeScope(context.Background(), []string{"kb"})
	if scopedWebEgressGuard(withScopedWebEgress(im, "user question")) != nil {
		t.Fatal("a knowledge scope alone is not public-web authorization")
	}
	checks := 0
	im = types.WithIMPublicWebAuthorizer(im, func(context.Context) error {
		checks++
		return errors.New("revoked")
	})
	guard := scopedWebEgressGuard(withScopedWebEgress(im, "user question"))
	if guard == nil || guard.SearchQuery() != "user question" {
		t.Fatal("trusted Octo question did not create its own egress guard")
	}
	if err := guard.Authorize(im); err == nil || checks != 1 {
		t.Fatal("network guard did not retain the current native authorization check")
	}
}
