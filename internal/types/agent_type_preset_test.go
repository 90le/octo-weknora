package types

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSourceQAPresetUsesSourceEvidenceBeforeDocumentation(t *testing.T) {
	presetData, err := os.ReadFile("../../config/agent_type_presets.yaml")
	require.NoError(t, err)

	var presetFile agentTypePresetsFile
	require.NoError(t, yaml.Unmarshal(presetData, &presetFile))

	var preset *AgentTypePresetEntry
	for index := range presetFile.Presets {
		if presetFile.Presets[index].ID == AgentTypeSourceQA {
			preset = &presetFile.Presets[index]
			break
		}
	}
	require.NotNil(t, preset)
	require.NotNil(t, preset.Config)
	require.Equal(t, "source_qa_agent", preset.Config.SystemPromptID)
	require.Equal(t, 8, preset.Config.MaxIterations)
	require.ElementsMatch(t, []string{"source_browse", "knowledge_search", "get_document_info"}, preset.Config.AllowedTools)
	require.Nil(t, preset.KBFilter, "source-only knowledge bases must remain selectable")

	promptData, err := os.ReadFile("../../config/prompt_templates/agent_system_prompt.yaml")
	require.NoError(t, err)
	var promptFile struct {
		Templates []struct {
			ID      string `yaml:"id"`
			Content string `yaml:"content"`
		} `yaml:"templates"`
	}
	require.NoError(t, yaml.Unmarshal(promptData, &promptFile))

	var content string
	for _, template := range promptFile.Templates {
		if template.ID == preset.Config.SystemPromptID {
			content = template.Content
			break
		}
	}
	require.Contains(t, content, "先调用 source_browse 的 search")
	require.Contains(t, content, "继续调用 read")
	require.Contains(t, content, "不能替代源码证据")
}
