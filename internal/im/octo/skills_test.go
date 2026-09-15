package octo

import "testing"

func TestChannelSkillsUseNativeParserAndRejectExecution(t *testing.T) {
	s := &ChannelSkillSource{}
	metadata, err := s.DiscoverSkills()
	if err != nil || len(metadata) != 2 {
		t.Fatalf("discover: %v %v", metadata, err)
	}
	for _, m := range metadata {
		skill, err := s.LoadSkillInstructions(m.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err = skill.Validate(); err != nil {
			t.Fatal(err)
		}
		file, err := s.LoadSkillFile(m.Name, "SKILL.md")
		if err != nil || file.IsScript || file.Path != "" {
			t.Fatal("unexpected executable resource")
		}
		if _, err = s.GetSkillBasePath(m.Name); err == nil {
			t.Fatal("host directory exposed")
		}
		if _, err = s.LoadSkillFile(m.Name, "../../credentials.json"); err == nil {
			t.Fatal("path escape accepted")
		}
	}
	if _, err = s.LoadSkillInstructions("other-agent"); err == nil {
		t.Fatal("cross-skill access")
	}
}
