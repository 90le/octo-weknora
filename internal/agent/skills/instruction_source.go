package skills

import (
	"context"
	"errors"
)

type instructionSourceKey struct{}

// WithInstructionSource is a per-turn host capability, not a user-supplied path.
func WithInstructionSource(ctx context.Context, source SkillSource) context.Context {
	if source == nil {
		return ctx
	}
	return context.WithValue(ctx, instructionSourceKey{}, source)
}
func InstructionSourceFromContext(ctx context.Context) SkillSource {
	source, _ := ctx.Value(instructionSourceKey{}).(SkillSource)
	return source
}

// readOnlySkillSource deliberately does not implement imageSkillSource.
type readOnlySkillSource struct{ SkillSource }

func (s readOnlySkillSource) GetSkillBasePath(string) (string, error) {
	return "", errors.New("instruction-only skill has no execution directory")
}
func (s readOnlySkillSource) LoadSkillInstructions(name string) (*Skill, error) {
	skill, err := s.SkillSource.LoadSkillInstructions(name)
	if err != nil {
		return nil, err
	}
	out := *skill
	out.BasePath = ""
	out.FilePath = ""
	return &out, nil
}
func (s readOnlySkillSource) LoadSkillFile(name, path string) (*SkillFile, error) {
	file, err := s.SkillSource.LoadSkillFile(name, path)
	if err != nil {
		return nil, err
	}
	out := *file
	out.Path = ""
	out.IsScript = false
	return &out, nil
}
