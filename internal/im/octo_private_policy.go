package im

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
)

var (
	// ErrOctoPrivateInvalid indicates an unusable private access configuration.
	ErrOctoPrivateInvalid = errors.New("invalid Octo private access policy")
	// ErrOctoPrivatePreview requires rechecking the draft against current settings.
	ErrOctoPrivatePreview = errors.New("preview the current Octo private access policy before saving")
)

const (
	octoPrivateDisabled = "disabled"
	octoPrivateOwner    = "creator_only"
	octoPrivateList     = "allowlist"
)

// OctoPrivateTools controls these permissions for direct conversations only.
type OctoPrivateTools struct {
	Files  bool `json:"allow_file_delivery"`
	Shell  bool `json:"allow_sandbox_shell"`
	Skills bool `json:"allow_sandbox_skills"`
	Legacy bool `json:"allow_generated_files"`
}

// OctoPrivatePolicy is the effective DM configuration. Legacy channels inherit
// their existing knowledge and tools until a private policy is saved.
type OctoPrivatePolicy struct {
	Mode         string           `json:"mode"`
	AllowedUIDs  []string         `json:"allowed_uids"`
	BlockedUIDs  []string         `json:"blocked_uids"`
	KnowledgeIDs []string         `json:"knowledge_base_ids"`
	Tools        OctoPrivateTools `json:"tools"`
}

var octoPrivateKeys = []string{
	"allowed_dm_uids", "dm_allowlist_uids", "blocked_dm_uids", "dm_access_mode", "dm_knowledge_base_ids", "dm_tools",
}

func validPrivateUID(uid string) bool {
	return uid != "" && len(uid) <= 128 && !strings.ContainsFunc(uid, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

func privateIDs(raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok || len(values) > 100 {
		return nil, ErrOctoPrivateInvalid
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok || !validPrivateUID(s) {
			return nil, ErrOctoPrivateInvalid
		}
		ids = append(ids, s)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// ReadOctoPrivatePolicy applies the same defaults during execution and preview.
func ReadOctoPrivatePolicy(ch *IMChannel, cfg map[string]any) (OctoPrivatePolicy, error) {
	p := OctoPrivatePolicy{
		Mode: octoPrivateList, AllowedUIDs: []string{}, BlockedUIDs: []string{}, KnowledgeIDs: []string{},
	}
	if raw, exists := cfg["dm_access_mode"]; exists {
		mode, _ := raw.(string)
		switch mode {
		case octoPrivateDisabled, octoPrivateOwner, octoPrivateList:
			p.Mode = mode
		default:
			return p, ErrOctoPrivateInvalid
		}
	}
	allowKey := "allowed_dm_uids"
	if _, ok := cfg["dm_allowlist_uids"]; ok {
		allowKey = "dm_allowlist_uids"
	}
	for key, dest := range map[string]*[]string{
		allowKey: &p.AllowedUIDs, "blocked_dm_uids": &p.BlockedUIDs, "dm_knowledge_base_ids": &p.KnowledgeIDs,
	} {
		if raw, exists := cfg[key]; exists {
			ids, err := privateIDs(raw)
			if err != nil {
				return p, err
			}
			*dest = ids
		}
	}
	if _, exists := cfg["dm_knowledge_base_ids"]; !exists && ch.KnowledgeBaseID != "" {
		p.KnowledgeIDs = []string{ch.KnowledgeBaseID}
	}
	tools := cfg
	if raw, exists := cfg["dm_tools"]; exists {
		var ok bool
		tools, ok = raw.(map[string]any)
		if !ok || len(tools) != 4 {
			return p, ErrOctoPrivateInvalid
		}
		for _, key := range []string{
			"allow_file_delivery", "allow_sandbox_shell", "allow_sandbox_skills", "allow_generated_files",
		} {
			if _, ok := tools[key].(bool); !ok {
				return p, ErrOctoPrivateInvalid
			}
		}
	}
	p.Tools = OctoPrivateTools{
		Files: GetExplicitBool(tools, "allow_file_delivery"), Shell: GetExplicitBool(tools, "allow_sandbox_shell"),
		Skills: GetExplicitBool(tools, "allow_sandbox_skills"), Legacy: GetBool(tools, "allow_generated_files"),
	}
	return p, nil
}

// AudienceDecision evaluates the UID without inheriting a knowledge scope.
func (p OctoPrivatePolicy) AudienceDecision(uid, owner string) string {
	switch {
	case !validPrivateUID(uid):
		return "invalid_uid"
	case slices.Contains(p.BlockedUIDs, uid):
		return "blocked"
	case p.Mode == octoPrivateDisabled:
		return "disabled"
	case p.Mode == octoPrivateOwner && owner == "":
		return "creator_unverified"
	case p.Mode == octoPrivateOwner && uid != owner:
		return "not_creator"
	case p.Mode == octoPrivateList && !slices.Contains(p.AllowedUIDs, uid):
		return "not_listed"
	case p.Mode == octoPrivateOwner || p.Mode == octoPrivateList:
		return "allowed"
	default:
		return "disabled"
	}
}

// Decision also requires a knowledge scope for private questions.
func (p OctoPrivatePolicy) Decision(uid, owner string) string {
	result := p.AudienceDecision(uid, owner)
	if result == "allowed" && len(p.KnowledgeIDs) == 0 {
		return "no_knowledge"
	}
	return result
}

// OctoPrivatePreview reports the same policy the next message would encounter.
type OctoPrivatePreview struct {
	Token    string              `json:"token,omitempty"`
	OwnerUID string              `json:"creator_uid,omitempty"`
	Decision string              `json:"decision,omitempty"`
	Problem  string              `json:"problem,omitempty"`
	Policy   OctoPrivatePolicy   `json:"policy"`
	Previous *OctoPrivateSummary `json:"previous,omitempty"`
}

// OctoPrivateSummary avoids putting native UIDs in the channel overview.
type OctoPrivateSummary struct {
	Mode      string `json:"mode"`
	Allowed   int    `json:"allowed"`
	Blocked   int    `json:"blocked"`
	Knowledge int    `json:"knowledge"`
}

func (p OctoPrivatePolicy) summary() OctoPrivateSummary {
	count := 0
	if p.Mode == octoPrivateList {
		for _, uid := range p.AllowedUIDs {
			if !slices.Contains(p.BlockedUIDs, uid) {
				count++
			}
		}
	}
	return OctoPrivateSummary{p.Mode, count, len(p.BlockedUIDs), len(p.KnowledgeIDs)}
}

// OctoPrivatePolicyPreview evaluates a draft without registering the Bot or
// making an Octo request. The stamp binds the draft to the durable channel.
func (s *Service) OctoPrivatePolicyPreview(
	ctx context.Context, current, proposed *IMChannel, uid string,
) (OctoPrivatePreview, error) {
	cfg, err := ParseCredentials(proposed.Credentials)
	if err != nil {
		return OctoPrivatePreview{}, ErrOctoPrivateInvalid
	}
	p, err := ReadOctoPrivatePolicy(proposed, cfg)
	if err != nil {
		return OctoPrivatePreview{}, err
	}
	out := OctoPrivatePreview{Policy: p}
	if current != nil {
		if config, err := ParseCredentials(current.Credentials); err == nil {
			if old, err := ReadOctoPrivatePolicy(current, config); err == nil {
				summary := old.summary()
				out.Previous = &summary
			}
		}
	}
	account, bot := GetString(cfg, "account_id"), GetString(cfg, "bot_uid")
	if account == "" || bot == "" {
		return out, ErrOctoPrivateInvalid
	}
	var conn octointegration.Connection
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", proposed.TenantID, account).
		First(&conn).Error; err != nil {
		return out, err
	}
	out.OwnerUID = conn.VerifiedOwnerUID(bot)
	if uid != "" {
		out.Decision = p.Decision(uid, out.OwnerUID)
	}
	switch {
	case p.Mode == octoPrivateOwner && out.OwnerUID == "":
		out.Problem = "creator_unverified"
	case len(p.KnowledgeIDs) == 0 &&
		(p.Mode == octoPrivateOwner || (p.Mode == octoPrivateList && len(p.AllowedUIDs) > 0)):
		out.Problem = "no_knowledge"
	case p.Mode != octoPrivateDisabled && len(p.KnowledgeIDs) > 0:
		var count int64
		if err := s.db.WithContext(ctx).Table("knowledge_bases").
			Where("tenant_id = ? AND id IN ? AND deleted_at IS NULL", proposed.TenantID, p.KnowledgeIDs).
			Count(&count).Error; err != nil {
			return out, err
		}
		if count != int64(len(p.KnowledgeIDs)) {
			out.Problem = "invalid_knowledge"
		}
	}
	if out.Problem != "" {
		if out.Decision == "allowed" {
			out.Decision = out.Problem
		}
		return out, nil
	}
	// Only the digest leaves the server; neither credentials nor ciphertext do.
	stamp := []any{proposed.TenantID, account, bot, conn.Token, out.OwnerUID, p}
	if current != nil {
		stamp = append(stamp, current.ID, current.UpdatedAt, current.Credentials)
	}
	payload, err := json.Marshal(stamp)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(payload)
	out.Token = hex.EncodeToString(sum[:])
	return out, nil
}

// PrepareOctoPrivateSave verifies a preview before persisting any policy change.
// Keeping the legacy key empty on opted-in rows makes rollback deny private
// conversation rather than missing a newer blacklist or granting older tools.
func (s *Service) PrepareOctoPrivateSave(ctx context.Context, current, next *IMChannel, stamp string) error {
	if next.Platform != "octo" {
		return nil
	}
	cfg, err := ParseCredentials(next.Credentials)
	if err != nil {
		return ErrOctoPrivateInvalid
	}
	nextPolicy, err := ReadOctoPrivatePolicy(next, cfg)
	if err != nil {
		return err
	}
	if current != nil && stamp == "" {
		prior, err := ParseCredentials(current.Credentials)
		if err != nil {
			return ErrOctoPrivateInvalid
		}
		oldPolicy, err := ReadOctoPrivatePolicy(current, prior)
		if err != nil {
			return err
		}
		if !samePrivatePolicy(oldPolicy, nextPolicy) || changesPrivateStorage(prior, cfg) {
			return ErrOctoPrivatePreview
		}
		if nextPolicy.Mode == octoPrivateOwner && (GetString(prior, "account_id") != GetString(cfg, "account_id") ||
			GetString(prior, "bot_uid") != GetString(cfg, "bot_uid")) {
			return ErrOctoPrivatePreview
		}
		if _, explicit := cfg["dm_access_mode"]; explicit {
			raw, e := privateIDs(cfg["allowed_dm_uids"])
			if e != nil || len(raw) > 0 {
				return ErrOctoPrivatePreview
			}
		}
		return nil
	}
	// Retain legacy API creation with no explicit private settings.
	if current == nil && stamp == "" && cfg["dm_access_mode"] == nil &&
		cfg["dm_allowlist_uids"] == nil && cfg["blocked_dm_uids"] == nil && cfg["dm_tools"] == nil &&
		len(nextPolicy.AllowedUIDs) == 0 {
		return nil
	}
	preview, err := s.OctoPrivatePolicyPreview(ctx, current, next, "")
	if err != nil {
		return err
	}
	if preview.Problem != "" {
		return ErrOctoPrivateInvalid
	}
	if stamp == "" || preview.Token != stamp {
		return ErrOctoPrivatePreview
	}
	cfg["dm_access_mode"] = nextPolicy.Mode
	cfg["allowed_dm_uids"] = []string{}
	cfg["dm_allowlist_uids"] = nextPolicy.AllowedUIDs
	cfg["blocked_dm_uids"] = nextPolicy.BlockedUIDs
	cfg["dm_knowledge_base_ids"] = nextPolicy.KnowledgeIDs
	cfg["dm_tools"] = nextPolicy.Tools
	raw, err := json.Marshal(cfg)
	next.Credentials = types.JSON(raw)
	return err
}

func samePrivatePolicy(a, b OctoPrivatePolicy) bool {
	return a.Mode == b.Mode && a.Tools == b.Tools && slices.Equal(a.AllowedUIDs, b.AllowedUIDs) &&
		slices.Equal(a.BlockedUIDs, b.BlockedUIDs) && slices.Equal(a.KnowledgeIDs, b.KnowledgeIDs)
}

func changesPrivateStorage(a, b map[string]any) bool {
	for _, key := range []string{"dm_access_mode", "dm_allowlist_uids", "blocked_dm_uids", "dm_tools"} {
		_, left := a[key]
		_, right := b[key]
		if left != right {
			return true
		}
	}
	return false
}

// Octo compares transport settings separately because its permission gates
// read the durable row at admission and again before deferred outputs.
func equalChannelRuntimeCredentials(platform string, left, right types.JSON) bool {
	if platform != "octo" {
		return equalChannelCredentials(left, right)
	}
	a, ea := ParseCredentials(left)
	b, eb := ParseCredentials(right)
	if ea != nil || eb != nil {
		return equalChannelCredentials(left, right)
	}
	for _, key := range octoPrivateKeys {
		delete(a, key)
		delete(b, key)
	}
	for _, key := range []string{
		"allowed_bot_uids", "management_bot_uids", "allow_file_delivery",
		"allow_generated_files", "allow_sandbox_shell", "allow_sandbox_skills",
	} {
		delete(a, key)
		delete(b, key)
	}
	l, _ := json.Marshal(a)
	r, _ := json.Marshal(b)
	return string(l) == string(r)
}
