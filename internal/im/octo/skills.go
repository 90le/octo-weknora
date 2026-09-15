package octo

import (
	"embed"
	"errors"

	"github.com/Tencent/WeKnora/internal/agent/skills"
)

//go:embed plugin-skills/*/SKILL.md
var bundledSkills embed.FS

// ChannelSkillSource reuses native progressive disclosure. Construct it only for
// an authorized Octo turn; never add it to the workspace's global skill catalog.
// These are read-only instructions, with no executable host or sandbox directory.
type ChannelSkillSource struct{}

var _ skills.SkillSource = (*ChannelSkillSource)(nil)

var channelSkillNames = []string{"octo-bot-api", "octo-card-message"}

func (s *ChannelSkillSource) DiscoverSkills() ([]*skills.SkillMetadata, error) {
	out := make([]*skills.SkillMetadata, 0, len(channelSkillNames))
	for _, name := range channelSkillNames {
		skill, err := s.LoadSkillInstructions(name)
		if err != nil {
			return nil, err
		}
		out = append(out, skill.ToMetadata())
	}
	return out, nil
}

func (s *ChannelSkillSource) LoadSkillInstructions(name string) (*skills.Skill, error) {
	if name != "octo-bot-api" && name != "octo-card-message" {
		return nil, errors.New("unknown Octo channel skill")
	}
	data, err := bundledSkills.ReadFile("plugin-skills/" + name + "/SKILL.md")
	if err != nil {
		return nil, err
	}
	return skills.ParseSkillFile(string(data))
}

func (s *ChannelSkillSource) LoadSkillFile(name, relativePath string) (*skills.SkillFile, error) {
	if _, err := s.LoadSkillInstructions(name); err != nil {
		return nil, err
	}
	if relativePath != "SKILL.md" {
		return nil, errors.New("unknown Octo skill resource")
	}
	data, err := bundledSkills.ReadFile("plugin-skills/" + name + "/SKILL.md")
	if err != nil {
		return nil, err
	}
	return &skills.SkillFile{Name: "SKILL.md", Content: string(data), IsScript: false}, nil
}

func (s *ChannelSkillSource) ListSkillFiles(name string) ([]string, error) {
	if _, err := s.LoadSkillInstructions(name); err != nil {
		return nil, err
	}
	return []string{"SKILL.md"}, nil
}

func (s *ChannelSkillSource) GetSkillBasePath(string) (string, error) {
	return "", errors.New("Octo channel skills have no executable directory")
}
