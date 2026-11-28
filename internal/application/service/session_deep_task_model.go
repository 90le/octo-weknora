package service

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// deepTaskArtifactKeywords identifies explicit deliverable / deep analytical report
// requests where an agent that explicitly configured DeepTaskModelID should upgrade
// from its fast default Q&A model to its configured deep-task model.
// Ordinary Q&A never matches and stays 100% on the agent's default model_id.
var deepTaskArtifactKeywords = []string{
	"pptx", "ppt", "演示文稿", "幻灯片", "汇报材料", "汇报方案",
	"行业分析报告", "行业研报", "研究报告", "深度分析报告", "咨询报告", "商业计划书",
	"竞品分析报告", "市场分析报告", "策略报告", "复盘报告", "白皮书",
	"executive deck", "slide deck", "presentation", "industry report",
	"xlsx", "excel", "docx", "word文档", "pdf报告",
}

var deepTaskActionHints = []string{
	"生成", "制作", "做一份", "做一个", "出一份", "出一个", "写一份", "撰写",
	"导出", "输出", "改成", "整理成", "重构", "优化", "重写", "create", "build", "generate", "export",
}

// resolveAgentTurnModelID resolves the effective chat model for an AgentQA turn.
// By default (when DeepTaskModelID is empty), it strictly follows the agent's
// configured model_id (or explicit per-request SummaryModelID). When the agent
// explicitly configures DeepTaskModelID, only deep report / deliverable turns
// escalate to it; ordinary Q&A stays on the fast default model.
func (s *sessionService) resolveAgentTurnModelID(
	ctx context.Context,
	req *types.QARequest,
	agentConfig *types.AgentConfig,
	baseModelID string,
) string {
	if req == nil || req.CustomAgent == nil || agentConfig == nil {
		return baseModelID
	}
	// Honor an explicit per-request model override and keep shared read-only agents
	// pinned unless they are running in their configured owner scope.
	if strings.TrimSpace(req.SummaryModelID) != "" {
		return baseModelID
	}
	deepModelID := strings.TrimSpace(req.CustomAgent.Config.DeepTaskModelID)
	if deepModelID == "" || deepModelID == baseModelID {
		return baseModelID
	}
	if !shouldEscalateToDeepTaskModel(req, agentConfig) {
		return baseModelID
	}
	if s.modelService == nil {
		return baseModelID
	}
	model, err := s.modelService.GetModelByID(ctx, deepModelID)
	if err != nil || model == nil || model.Type != types.ModelTypeKnowledgeQA {
		logger.Warnf(ctx, "Configured deep_task_model_id %s is unavailable for agent %s, falling back to %s",
			deepModelID, req.CustomAgent.ID, baseModelID)
		return baseModelID
	}
	logger.Infof(ctx, "Escalating deep report/deliverable turn to agent's deep_task_model_id: %s (default: %s)",
		deepModelID, baseModelID)
	return deepModelID
}

func shouldEscalateToDeepTaskModel(req *types.QARequest, agentConfig *types.AgentConfig) bool {
	if req == nil || agentConfig == nil {
		return false
	}
	for _, skillName := range agentConfig.PinnedSkillNames {
		lower := strings.ToLower(strings.TrimSpace(skillName))
		if strings.Contains(lower, "pptx") || strings.Contains(lower, "report") || strings.Contains(lower, "deck") {
			return true
		}
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if query == "" {
		return false
	}
	hasArtifact := false
	for _, kw := range deepTaskArtifactKeywords {
		if strings.Contains(query, kw) {
			hasArtifact = true
			break
		}
	}
	if !hasArtifact {
		return false
	}
	// Explicit file format requests (ppt/pptx/slide deck/行业分析报告/研报/咨询报告/白皮书)
	// directly indicate a deep deliverable even with brief follow-ups ("改成15页PPT").
	for _, strong := range []string{
		"pptx", "ppt", "演示文稿", "幻灯片", "行业分析报告", "行业研报", "深度分析报告",
		"咨询报告", "竞品分析报告", "市场分析报告", "商业计划书", "白皮书", "slide deck", "presentation",
	} {
		if strings.Contains(query, strong) {
			return true
		}
	}
	for _, act := range deepTaskActionHints {
		if strings.Contains(query, act) {
			return true
		}
	}
	return false
}
