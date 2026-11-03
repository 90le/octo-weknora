package im

import (
	"context"
	"errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

type sandboxUnavailableKey struct{}

const sandboxUnavailableNotice = "本次沙箱不可用，终端和 Skill 未执行；以下回答仅使用当前授权的其他能力。"

// missingSandboxFallback applies only after the caller has revalidated the
// channel scope and confirmed the scoped Agent has no named sandbox.
func missingSandboxFallback(ctx context.Context, missingNamedSandbox bool) context.Context {
	requests := types.IMSandboxRequestsFromContext(ctx)
	_, granted := types.IMSandboxGrantFromContext(ctx)
	if !missingNamedSandbox || granted || !types.HasIMKnowledgeScope(ctx) || (!requests.Shell && !requests.Skills) {
		return ctx
	}
	return context.WithValue(types.WithoutIMSandboxExecution(ctx), sandboxUnavailableKey{}, true)
}

// prepareSandboxOrContinue never broadens access: if preparation fails, the
// original scope must still be valid before sandbox authority is masked.
func prepareSandboxOrContinue(
	ctx context.Context,
	prepare func(context.Context) (context.Context, error),
) (context.Context, bool, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || (!grant.AllowShell && !grant.AllowSkills) {
		return ctx, false, nil
	}
	var prepared context.Context
	var err error
	if prepare == nil {
		err = errors.New("sandbox turn preparation unavailable")
	} else {
		prepared, err = prepare(ctx)
		if err == nil && prepared == nil {
			err = errors.New("sandbox turn preparation returned no context")
		}
	}
	if err == nil {
		return prepared, true, nil
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if err := grant.Revalidate(ctx); err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	disabled := types.WithoutIMSandboxExecution(ctx)
	return context.WithValue(disabled, sandboxUnavailableKey{}, true), false, nil
}

func appendSandboxUnavailableNotice(ctx context.Context, answer string) string {
	unavailable, _ := ctx.Value(sandboxUnavailableKey{}).(bool)
	if !unavailable || strings.Contains(answer, sandboxUnavailableNotice) {
		return answer
	}
	if strings.TrimSpace(answer) == "" {
		return sandboxUnavailableNotice
	}
	return answer + "\n\n" + sandboxUnavailableNotice
}
