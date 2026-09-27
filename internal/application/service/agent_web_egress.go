package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

type agentWebEgressGuardKey struct{}

// withScopedWebEgress binds public network access to this one admitted Octo
// question. Neither quoted messages, GROUP.md, knowledge snippets nor tool
// output may become a search query or an arbitrary fetch destination.
func withScopedWebEgress(ctx context.Context, userQuestion string) context.Context {
	if !types.HasIMKnowledgeScope(ctx) {
		return ctx
	}
	authorize := types.IMPublicWebAuthorizer(ctx)
	if authorize == nil {
		return ctx
	}
	guard := tools.NewWebEgressGuard(userQuestion).WithAuthorizer(authorize)
	return context.WithValue(ctx, agentWebEgressGuardKey{}, guard)
}

func scopedWebEgressGuard(ctx context.Context) *tools.WebEgressGuard {
	guard, _ := ctx.Value(agentWebEgressGuardKey{}).(*tools.WebEgressGuard)
	return guard
}
