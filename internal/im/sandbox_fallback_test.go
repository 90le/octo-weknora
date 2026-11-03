package im

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestSandboxPreparationFailureRetainsOnlyKnowledgeAuthority(t *testing.T) {
	ctx := types.WithIMKnowledgeScope(context.Background(), []string{"allowed-kb"})
	ctx = types.WithIMSandboxRequests(ctx, true, true)
	ctx = types.WithIMSandboxPolicy(ctx, "old-policy")
	ctx = types.WithIMSandboxGrant(ctx, types.IMSandboxGrant{
		SessionID: "s", TenantID: 7, ConfigID: "cfg", AllowShell: true, AllowSkills: true,
		Revalidate: func(context.Context) error { return nil },
	})
	degraded, active, err := prepareSandboxOrContinue(ctx, func(context.Context) (context.Context, error) {
		return nil, errors.New("docker unavailable")
	})
	if err != nil || active || degraded == nil {
		t.Fatalf("degradation failed: active=%v err=%v", active, err)
	}
	if _, ok := types.IMSandboxGrantFromContext(degraded); ok {
		t.Fatal("sandbox grant survived degradation")
	}
	if req := types.IMSandboxRequestsFromContext(degraded); req.Shell || req.Skills ||
		types.IMSandboxPolicyFromContext(degraded) != "" {
		t.Fatal("sandbox request or policy survived degradation")
	}
	config := types.RestrictIMAgentConfig(
		degraded,
		&types.AgentConfig{KnowledgeBases: []string{"other"}, SandboxConfigID: "cfg", SkillsEnabled: true},
	)
	if len(config.KnowledgeBases) != 1 || config.KnowledgeBases[0] != "allowed-kb" || config.SandboxConfigID != "" ||
		config.SkillsEnabled {
		t.Fatalf("unexpected degraded config: %+v", config)
	}
	answer := appendSandboxUnavailableNotice(degraded, "知识答案")
	if !strings.Contains(answer, "知识答案") || !strings.Contains(answer, sandboxUnavailableNotice) ||
		appendSandboxUnavailableNotice(degraded, answer) != answer {
		t.Fatalf("incorrect notice: %q", answer)
	}
	if _, ok := types.IMSandboxGrantFromContext(ctx); !ok {
		t.Fatal("parent context was changed")
	}
}

func TestSandboxFallbackDeliversKnowledgeAnswerInBothOutputModes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx := types.WithIMKnowledgeScope(context.Background(), []string{"kb"})
			ctx = types.WithIMSandboxRequests(ctx, true, false)
			ctx = types.WithIMSandboxGrant(
				ctx,
				types.IMSandboxGrant{
					SessionID:  "session",
					TenantID:   7,
					ConfigID:   "cfg",
					AllowShell: true,
					Revalidate: func(context.Context) error { return nil },
				},
			)
			ctx, _, err := prepareSandboxOrContinue(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			result := startQAExitHandler(ctx, stream, true, func(callCtx context.Context, bus *event.EventBus) error {
				if _, granted := types.IMSandboxGrantFromContext(callCtx); granted ||
					!types.HasIMKnowledgeScope(callCtx) {
					return errors.New("unexpected authority in knowledge QA")
				}
				if err := bus.Emit(callCtx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.
					AgentFinalAnswerData{Content: "知识答案", Done: true}}); err != nil {
					return err
				}
				return bus.Emit(
					callCtx,
					event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "知识答案"}},
				)
			})
			checkQAExitResult(t, result, stream, false, "知识答案\n\n"+sandboxUnavailableNotice)
		})
	}
}

func TestSandboxPreparationDoesNotBypassRevocationOrCancellation(t *testing.T) {
	denied := errors.New("scope denied")
	ctx := types.WithIMSandboxGrant(context.Background(), types.IMSandboxGrant{
		SessionID: "s", TenantID: 7, ConfigID: "cfg", AllowShell: true,
		Revalidate: func(context.Context) error { return denied },
	})
	if _, _, err := prepareSandboxOrContinue(ctx, nil); !errors.Is(err, denied) {
		t.Fatalf("revoked scope was allowed: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := prepareSandboxOrContinue(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request continued: %v", err)
	}
}

func TestSandboxPreparationSuccessPreservesGrant(t *testing.T) {
	ctx := types.WithIMSandboxGrant(context.Background(), types.IMSandboxGrant{
		SessionID: "s", TenantID: 7, ConfigID: "cfg", AllowShell: true,
		Revalidate: func(context.Context) error { return nil },
	})
	prepared, active, err := prepareSandboxOrContinue(
		ctx,
		func(ctx context.Context) (context.Context, error) { return ctx, nil },
	)
	if err != nil || !active {
		t.Fatalf("successful preparation failed: %v", err)
	}
	if _, ok := types.IMSandboxGrantFromContext(prepared); !ok ||
		appendSandboxUnavailableNotice(prepared, "ok") != "ok" {
		t.Fatal("successful preparation changed authority or added a notice")
	}
}
