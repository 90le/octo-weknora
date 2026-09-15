package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func instructionFixture(t *testing.T, name string) *Loader {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Read-only test instructions\n---\n# Instructions\nUse current tools.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return NewLoader([]string{root})
}

func TestInstructionSourcePreservesTenantAndCannotExecute(t *testing.T) {
	tenant := instructionFixture(t, "tenant-skill")
	channel := instructionFixture(t, "channel-skill")
	m := NewManager(&ManagerConfig{Enabled: true}, nil).WithTenantSource(tenant).WithInstructionSource(channel)
	if err := m.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.GetAllMetadata()) != 2 {
		t.Fatal("tenant skills replaced")
	}
	if m.resolveSource("tenant-skill") != tenant {
		t.Fatal("tenant execution source changed")
	}
	if _, err := m.LoadSkill(context.Background(), "channel-skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resolveSource("channel-skill").GetSkillBasePath("channel-skill"); err == nil {
		t.Fatal("instruction execution allowed")
	}
	if _, ok := m.SandboxSkillDir("channel-skill"); ok {
		t.Fatal("instruction sandbox path exposed")
	}
	file, err := m.resolveSource("channel-skill").LoadSkillFile("channel-skill", "SKILL.md")
	if err != nil || file.Path != "" || file.IsScript {
		t.Fatal("host file exposed")
	}
	collision := NewManager(&ManagerConfig{Enabled: true}, nil).WithTenantSource(tenant).WithInstructionSource(tenant)
	if err = collision.Initialize(context.Background()); err == nil {
		t.Fatal("duplicate silently replaced tenant skill")
	}
	if InstructionSourceFromContext(context.Background()) != nil {
		t.Fatal("global instructions leaked")
	}
}
