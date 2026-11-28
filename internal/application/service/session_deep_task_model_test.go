package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type fakeDeepTaskModelSvc struct {
	interfaces.ModelService
	models map[string]*types.Model
}

func (f *fakeDeepTaskModelSvc) GetModelByID(_ context.Context, id string) (*types.Model, error) {
	return f.models[id], nil
}

func TestResolveAgentTurnModelID(t *testing.T) {
	svc := &sessionService{
		modelService: &fakeDeepTaskModelSvc{
			models: map[string]*types.Model{
				"fast-model": {ID: "fast-model", Type: types.ModelTypeKnowledgeQA},
				"deep-model": {ID: "deep-model", Type: types.ModelTypeKnowledgeQA},
			},
		},
	}

	t.Run("default empty deep_task_model_id always follows default model_id", func(t *testing.T) {
		req := &types.QARequest{
			Query: "帮我生成一份15页新能源汽车行业分析报告PPT",
			CustomAgent: &types.CustomAgent{
				ID:     "agent-1",
				Config: types.CustomAgentConfig{ModelID: "fast-model"},
			},
		}
		got := svc.resolveAgentTurnModelID(context.Background(), req, &types.AgentConfig{}, "fast-model")
		require.Equal(t, "fast-model", got)
	})

	t.Run("ordinary QA stays on fast default model even when deep_task_model_id is set", func(t *testing.T) {
		req := &types.QARequest{
			Query: "介绍一下秒针系统的核心产品和价格方案",
			CustomAgent: &types.CustomAgent{
				ID:     "agent-1",
				Config: types.CustomAgentConfig{ModelID: "fast-model", DeepTaskModelID: "deep-model"},
			},
		}
		got := svc.resolveAgentTurnModelID(context.Background(), req, &types.AgentConfig{}, "fast-model")
		require.Equal(t, "fast-model", got)
	})

	t.Run("PPT and industry report requests escalate when deep_task_model_id is configured", func(t *testing.T) {
		for _, q := range []string{
			"请结合知识库做一份15页社媒营销行业分析报告PPT",
			"改成15页PPT，并结合知识库配图丰富内容",
			"撰写一份深度分析报告并导出docx",
		} {
			req := &types.QARequest{
				Query: q,
				CustomAgent: &types.CustomAgent{
					ID:     "agent-1",
					Config: types.CustomAgentConfig{ModelID: "fast-model", DeepTaskModelID: "deep-model"},
				},
			}
			got := svc.resolveAgentTurnModelID(context.Background(), req, &types.AgentConfig{}, "fast-model")
			require.Equal(t, "deep-model", got, "query=%q", q)
		}
	})

	t.Run("pinned executive-pptx skill escalates when deep_task_model_id is configured", func(t *testing.T) {
		req := &types.QARequest{
			Query: "继续完善刚才的汇报",
			CustomAgent: &types.CustomAgent{
				ID:     "agent-1",
				Config: types.CustomAgentConfig{ModelID: "fast-model", DeepTaskModelID: "deep-model"},
			},
		}
		got := svc.resolveAgentTurnModelID(context.Background(), req, &types.AgentConfig{
			PinnedSkillNames: []string{"executive-pptx"},
		}, "fast-model")
		require.Equal(t, "deep-model", got)
	})

	t.Run("explicit per-request summary model override wins over deep_task_model_id", func(t *testing.T) {
		req := &types.QARequest{
			Query:          "做一份行业分析报告PPT",
			SummaryModelID: "explicit-override",
			CustomAgent: &types.CustomAgent{
				ID:     "agent-1",
				Config: types.CustomAgentConfig{ModelID: "fast-model", DeepTaskModelID: "deep-model"},
			},
		}
		got := svc.resolveAgentTurnModelID(context.Background(), req, &types.AgentConfig{}, "explicit-override")
		require.Equal(t, "explicit-override", got)
	})

	t.Run("missing deep_task_model_id gracefully falls back to default model_id", func(t *testing.T) {
		req := &types.QARequest{
			Query: "做一份行业分析报告PPT",
			CustomAgent: &types.CustomAgent{
				ID:     "agent-1",
				Config: types.CustomAgentConfig{ModelID: "fast-model", DeepTaskModelID: "deleted-model"},
			},
		}
		got := svc.resolveAgentTurnModelID(context.Background(), req, &types.AgentConfig{}, "fast-model")
		require.Equal(t, "fast-model", got)
	})
}
