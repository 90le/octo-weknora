package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent/skills"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

// approvedIMSkills only returns explicitly enabled, installed versions from
// the selected sandbox config and intersects the Agent selection by name.
// Bundles need an immutable digest before they may be staged into a clean VM.
func (s *sessionService) approvedIMSkills(
	ctx context.Context,
	grant types.IMSandboxGrant,
	mode string,
	selected []string,
) ([]*types.TenantSkillEntity, error) {
	if s.sandboxConfigRepo == nil || s.tenantSkillRepo == nil {
		return nil, errors.New("octo sandbox: Skill catalog is unavailable")
	}
	cfg, err := s.sandboxConfigRepo.GetByID(ctx, grant.TenantID, grant.ConfigID)
	if err != nil || cfg == nil || cfg.Config == nil {
		return nil, errors.New("octo sandbox: selected Skill config is unavailable")
	}
	if !sandbox.SkillImageActive(cfg.Config) {
		return nil, nil
	}
	rows, err := s.tenantSkillRepo.ListSkillsByConfig(ctx, grant.TenantID, grant.ConfigID)
	if err != nil {
		return nil, fmt.Errorf("octo sandbox: load approved Skills: %w", err)
	}
	wanted := map[string]bool{}
	for _, name := range selected {
		wanted[name] = true
	}
	seen := map[string]bool{}
	out := make([]*types.TenantSkillEntity, 0, len(rows))
	for _, row := range rows {
		if row == nil || !row.Enabled || (mode == "selected" && !wanted[row.Name]) {
			continue
		}
		served := row.ServedView()
		if served == nil {
			continue
		}
		if served.TenantID != grant.TenantID ||
			served.SandboxConfigID != grant.ConfigID ||
			!sandbox.IsValidSkillName(served.Name) ||
			seen[served.Name] {
			return nil, errors.New("octo sandbox: ambiguous Skill identity")
		}
		if strings.TrimSpace(served.BundleSHA256) == "" {
			return nil, fmt.Errorf("octo sandbox: Skill %q has no verifiable bundle digest", served.Name)
		}
		seen[served.Name] = true
		safe := *served
		// No synthetic system user or tenant-wide credential reaches the IM run.
		safe.Envs = nil
		out = append(out, &safe)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func imSkillFingerprint(rows []*types.TenantSkillEntity) ([32]byte, error) {
	type entry struct{ ID, Name, Version, Digest, Snapshot, BundleRef, CatalogID, Instructions string }
	values := make([]entry, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			return [32]byte{}, errors.New("octo sandbox: invalid Skill")
		}
		values = append(
			values,
			entry{
				row.ID, row.Name, row.Version, row.BundleSHA256,
				row.InstalledSnapshotID, row.BundleRef, row.CatalogID, row.Instructions,
			},
		)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func (s *sessionService) withIMSkillsCheck(ctx context.Context, cfg *types.AgentConfig) (context.Context, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || !grant.AllowSkills {
		return ctx, nil
	}
	if cfg == nil || !cfg.SkillsEnabled {
		return nil, errors.New("octo sandbox: Agent has no enabled Skills")
	}
	// This immutable selection comes from the saved Agent admitted by IM,
	// not from a request-supplied or later-mutated Agent copy.
	mode := grant.SkillsSelectionMode
	selected := append([]string(nil), grant.SelectedSkillNames...)
	if mode != "all" && (mode != "selected" || len(selected) == 0) {
		return nil, types.ErrIMSandboxDenied
	}
	rows, err := s.approvedIMSkills(ctx, grant, mode, selected)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		// Enabling the channel capability does not imply that an administrator
		// has installed a Skill. Keep ordinary QA and the independent shell
		// available while exposing no Skill tools for this turn.
		cfg.SkillsEnabled = false
		cfg.AllowedSkills = nil
		cfg.PinnedSkillNames = nil
	}
	cfg.TenantSkills = rows
	allowedNames := make([]string, 0, len(rows))
	for _, row := range rows {
		allowedNames = append(allowedNames, row.Name)
	}
	if len(allowedNames) == 0 {
		cfg.PinnedSkillNames = nil
	} else {
		cfg.PinnedSkillNames = pinPreservingRequestOrder(cfg.PinnedSkillNames, allowedNames)
	}
	if mode == "selected" {
		cfg.AllowedSkills = nil
		for _, row := range rows {
			cfg.AllowedSkills = append(cfg.AllowedSkills, row.Name)
		}
		if len(rows) == 0 {
			cfg.SkillsEnabled = false
		}
	}
	fingerprint, err := imSkillFingerprint(rows)
	if err != nil {
		return nil, err
	}
	if previous, ok := ctx.Value(imSkillFingerprintKey{}).([32]byte); ok && previous != fingerprint {
		return nil, errors.New("octo sandbox: Skill authorization changed")
	}
	ctx = context.WithValue(ctx, imSkillFingerprintKey{}, fingerprint)
	return withIMSkillRevalidator(ctx, func(checkCtx context.Context) error {
		fresh, err := s.approvedIMSkills(checkCtx, grant, mode, selected)
		if err != nil {
			return err
		}
		sum, err := imSkillFingerprint(fresh)
		if err != nil || sum != fingerprint {
			return errors.New("octo sandbox: Skill authorization changed")
		}
		return nil
	}), nil
}

// imStagedSkillSource deliberately hides image paths so the existing skill
// manager stages only these approved archives into the clean session sandbox.
func (s *agentService) authorizeIMSkillSource(ctx context.Context, sessionID string) error {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || !grant.AllowSkills {
		return types.ErrIMSandboxDenied
	}
	if _, err := ValidateIMSandboxPin(ctx, s.sandboxPinner, sessionID, false); err != nil {
		return err
	}
	check, ok := ctx.Value(imSkillRevalidatorKey{}).(imSkillRevalidator)
	if !ok || check == nil {
		return errors.New("octo sandbox: Skill authorization unavailable")
	}
	return check(ctx)
}

func (s *agentService) authorizeIMSkill(
	ctx context.Context,
	sessionID string,
	config *types.AgentConfig,
	name string,
) error {
	if err := s.authorizeIMSkillSource(ctx, sessionID); err != nil {
		return err
	}
	if config != nil {
		for _, row := range config.TenantSkills {
			if row != nil && row.Name == name {
				return nil
			}
		}
	}
	return fmt.Errorf("octo sandbox: Skill %q is not approved for this Agent", name)
}

func (s *agentService) authorizeIMSkillResource(
	ctx context.Context,
	sessionID string,
	config *types.AgentConfig,
	name string,
) error {
	if config != nil {
		for _, row := range config.TenantSkills {
			if row != nil && row.Name == name {
				return s.authorizeIMSkill(ctx, sessionID, config, name)
			}
		}
	}
	// Native channel instruction resources are authorized by their own source
	// and do not become executable sandbox Skills.
	if source := skills.InstructionSourceFromContext(ctx); source != nil {
		rows, err := source.DiscoverSkills()
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row != nil && row.Name == name {
				return nil
			}
		}
	}
	return fmt.Errorf("octo sandbox: Skill %q is not authorized", name)
}

type imStagedSkillSource struct {
	skills.SkillSource
	authorize func() error
}

func (s imStagedSkillSource) check() error {
	if s.authorize == nil {
		return types.ErrIMSandboxDenied
	}
	return s.authorize()
}

func (s imStagedSkillSource) ListSkillFiles(name string) ([]string, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return s.SkillSource.ListSkillFiles(name)
}

func (s imStagedSkillSource) GetSkillBasePath(string) (string, error) {
	return "", errors.New("octo Skill is staged per session")
}

func (s imStagedSkillSource) DiscoverSkills() ([]*skills.SkillMetadata, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	rows, err := s.SkillSource.DiscoverSkills()
	if err != nil {
		return nil, err
	}
	out := make([]*skills.SkillMetadata, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			cp := *row
			cp.BasePath = ""
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s imStagedSkillSource) LoadSkillInstructions(name string) (*skills.Skill, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	row, err := s.SkillSource.LoadSkillInstructions(name)
	if err != nil {
		return nil, err
	}
	cp := *row
	cp.BasePath = ""
	cp.FilePath = ""
	return &cp, nil
}

func (s imStagedSkillSource) LoadSkillFile(name, rel string) (*skills.SkillFile, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	file, err := s.SkillSource.LoadSkillFile(name, rel)
	if err != nil {
		return nil, err
	}
	cp := *file
	cp.Path = ""
	return &cp, nil
}
