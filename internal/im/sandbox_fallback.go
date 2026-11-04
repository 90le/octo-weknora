package im

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// The dimensions are fixed: no request or error strings become metric labels.
var (
	sandboxPreparationStages = [...]string{
		"unknown", "pin", "configuration", "skills", "policy", "output_limit", "reconcile",
	}
	sandboxPreparationReasons = [...]string{"unavailable", "timeout", "denied"}
	sandboxPreparationCounts  [7][3]atomic.Uint64
)

// SandboxPreparationFailureCount holds a fixed-label process-lifetime degradation count.
type SandboxPreparationFailureCount struct {
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
	Count  uint64 `json:"count"`
}

func recordSandboxPreparationFailure(stage, reason string) {
	stageIndex, reasonIndex := 0, 0
	for i, label := range sandboxPreparationStages {
		if label == stage {
			stageIndex = i
			break
		}
	}
	for i, label := range sandboxPreparationReasons {
		if label == reason {
			reasonIndex = i
			break
		}
	}
	sandboxPreparationCounts[stageIndex][reasonIndex].Add(1)
}

// RuntimeSandboxPreparationFailures returns only nonzero process-lifetime counts.
func RuntimeSandboxPreparationFailures() []SandboxPreparationFailureCount {
	result := make([]SandboxPreparationFailureCount, 0)
	for i, stage := range sandboxPreparationStages {
		for j, reason := range sandboxPreparationReasons {
			if count := sandboxPreparationCounts[i][j].Load(); count > 0 {
				result = append(result, SandboxPreparationFailureCount{Stage: stage, Reason: reason, Count: count})
			}
		}
	}
	return result
}

type sandboxUnavailableKey struct{}

const sandboxUnavailableNotice = "本轮未能启用沙箱，终端和 Skill 未执行；回答仅使用其他已授权能力。"

// Only fixed diagnostic labels are logged; backend errors may contain paths,
// endpoint addresses or credentials and must not be printed here.
func sandboxPreparationDiagnostic(err error) (string, string) {
	stage, reason := "unknown", "unavailable"
	var carrier interface{ SandboxPreparationStage() string }
	if errors.As(err, &carrier) {
		candidate := carrier.SandboxPreparationStage()
		switch candidate {
		case "pin", "configuration", "skills", "policy", "output_limit", "reconcile":
			stage = candidate
		}
	}
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	case errors.Is(err, types.ErrIMSandboxDenied):
		reason = "denied"
	case errors.As(err, &network) && network.Timeout():
		reason = "timeout"
	}
	return stage, reason
}

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
	stage, reason := sandboxPreparationDiagnostic(err)
	recordSandboxPreparationFailure(stage, reason)
	logger.Warnf(ctx, "[IM] sandbox preparation failed: stage=%s reason=%s", stage, reason)
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
