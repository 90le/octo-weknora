package octointegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errStalePreview  = errors.New("octo configuration changed after it was read")
	errScopeChildren = errors.New("octo main group still has connected subareas")
	errScopeSchedule = errors.New("octo scope still has active scheduled reports")
)

// ScopeACL holds a region's direct query bindings and independent maintenance grants.
type ScopeACL struct {
	QueryIDs  []string `json:"query_ids"`
	ManageIDs []string `json:"manage_ids"`
}

// ScopeConfiguration is a complete configuration submitted for review.
type ScopeConfiguration struct {
	ScopeACL
	InheritParent          *bool `json:"inherit_parent"`
	AllowKnowledgeCreation bool  `json:"allow_knowledge_creation"`
	AggregateChildIssues   bool  `json:"aggregate_child_issues"`
	AllowPublicWeb         bool  `json:"allow_public_web"`
}

type accessState struct {
	QueryMode string `json:"query_mode"`
	Manage    bool   `json:"can_manage"`
}

// KnowledgeImpact compares one library's effective permissions.
type KnowledgeImpact struct {
	KnowledgeBaseID string      `json:"knowledge_base_id"`
	Before          accessState `json:"before"`
	After           accessState `json:"after"`
}

// ScopeImpact lists the changes for one exact Bot and region.
type ScopeImpact struct {
	ScopeID     string            `json:"scope_id"`
	DisplayName string            `json:"display_name"`
	SubareaID   string            `json:"subarea_id"`
	Changes     []KnowledgeImpact `json:"changes"`
}

// ConfigurationSnapshot contains the local settings and their concurrency revision.
type ConfigurationSnapshot struct {
	Scope *Scope `json:"scope"`
	ScopeACL
	ParentQueryIDs []string    `json:"parent_query_ids"`
	ChildAreas     []AreaEntry `json:"child_areas"`
	Revision       string      `json:"revision"`
}

// ConfigurationPreview binds the requested change to the reviewed revision.
type ConfigurationPreview struct {
	Token   string        `json:"token"`
	Impacts []ScopeImpact `json:"impacts"`
}

type familySnapshot struct {
	ConfigurationSnapshot
	children []Scope
	acls     map[string]ScopeACL
}

func unionIDs(a, b []string) []string {
	r := append(append([]string{}, a...), b...)
	slices.Sort(r)
	return slices.Compact(r)
}

func loadScopeACL(db *gorm.DB, tenant uint64, ids []string) (map[string]ScopeACL, error) {
	result := make(map[string]ScopeACL, len(ids))
	for _, id := range ids {
		result[id] = ScopeACL{QueryIDs: []string{}, ManageIDs: []string{}}
	}
	for batch := range slices.Chunk(ids, 300) {
		for _, table := range []string{"octo_scope_bindings", "octo_scope_knowledge_grants"} {
			var rows []struct{ ScopeID, KnowledgeBaseID string }
			if err := db.Table(table+" AS a").Select("a.scope_id, a.knowledge_base_id").
				Joins("JOIN knowledge_bases AS k ON k.id = a.knowledge_base_id "+
					"AND k.tenant_id = a.tenant_id AND k.deleted_at IS NULL").
				Where("a.tenant_id = ? AND a.scope_id IN ?", tenant, batch).
				Order("a.scope_id, a.knowledge_base_id").Scan(&rows).Error; err != nil {
				return nil, err
			}
			for _, row := range rows {
				acl := result[row.ScopeID]
				if table == "octo_scope_bindings" {
					acl.QueryIDs = append(acl.QueryIDs, row.KnowledgeBaseID)
				} else {
					acl.ManageIDs = append(acl.ManageIDs, row.KnowledgeBaseID)
				}
				result[row.ScopeID] = acl
			}
		}
	}
	return result, nil
}

// All writers of a scope's configuration (including legacy endpoints) lock
// the Bot's main row first, so a preview cannot race an inheritance change or
// a subarea attach. SQLite serializes writes and fails a busy transaction.
func lockScopeFamily(tx *gorm.DB, tenant uint64, id string) (*Scope, *Scope, error) {
	scope, err := NewStore(tx).Get(tx.Statement.Context, tenant, id)
	if err != nil {
		return nil, nil, err
	}
	var parent Scope
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND account_id = ? AND group_id = ?", tenant, scope.AccountID, scope.GroupID).
		Where("subarea_id = ''").
		First(&parent).Error; err != nil {
		return nil, nil, err
	}
	if parent.ID == id {
		return &parent, nil, nil
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND id = ?", tenant, id).First(scope).Error; err != nil {
		return nil, nil, err
	}
	return scope, &parent, nil
}

func fingerprint(data any) string {
	encoded, _ := json.Marshal(data) // all revision inputs are JSON DTOs
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func readFamily(tx *gorm.DB, tenant uint64, id string) (*familySnapshot, error) {
	scope, parent, err := lockScopeFamily(tx, tenant, id)
	if err != nil {
		return nil, err
	}
	f := &familySnapshot{
		ConfigurationSnapshot: ConfigurationSnapshot{Scope: scope, ParentQueryIDs: []string{}, ChildAreas: []AreaEntry{}},
		children:              []Scope{},
	}
	ids := []string{id}
	if parent != nil {
		ids = append(ids, parent.ID)
	} else {
		if err := tx.Where("tenant_id = ? AND account_id = ? AND group_id = ?", tenant, scope.AccountID, scope.GroupID).
			Where("subarea_id <> ''").
			Order("subarea_id, id").Find(&f.children).Error; err != nil {
			return nil, err
		}
		for _, child := range f.children {
			ids = append(ids, child.ID)
			f.ChildAreas = append(f.ChildAreas, AreaEntry{SubareaID: child.SubareaID, DisplayName: child.DisplayName})
		}
	}
	f.acls, err = loadScopeACL(tx, tenant, ids)
	if err != nil {
		return nil, err
	}
	f.ScopeACL = f.acls[id]
	if parent != nil {
		f.ParentQueryIDs = f.acls[parent.ID].QueryIDs
	}
	f.Revision = fingerprint([]any{tenant, scope, parent, f.children, f.acls})
	return f, nil
}

func (s *Store) configuration(ctx context.Context, tenant uint64, id string) (out ConfigurationSnapshot, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		f, err := readFamily(tx, tenant, id)
		if err == nil {
			out = f.ConfigurationSnapshot
		}
		return err
	})
	return
}

func normalizeConfiguration(cfg ScopeConfiguration, scope *Scope) (ScopeConfiguration, error) {
	if cfg.InheritParent == nil || cfg.QueryIDs == nil || cfg.ManageIDs == nil ||
		len(cfg.QueryIDs) > 2000 || len(cfg.ManageIDs) > 2000 ||
		(scope.SubareaID == "" && *cfg.InheritParent) ||
		(scope.SubareaID != "" && cfg.AggregateChildIssues && !scope.AggregateChildIssues) {
		return cfg, ErrInvalid
	}
	for _, id := range unionIDs(cfg.QueryIDs, cfg.ManageIDs) {
		if !validID(id) {
			return cfg, ErrInvalid
		}
	}
	cfg.QueryIDs = unionIDs(cfg.QueryIDs, nil)
	cfg.ManageIDs = unionIDs(cfg.ManageIDs, nil)
	return cfg, nil
}

func effectiveState(kb string, acl ScopeACL, parent []string) accessState {
	mode := "none"
	if _, found := slices.BinarySearch(acl.QueryIDs, kb); found {
		mode = "direct"
	} else if _, found := slices.BinarySearch(parent, kb); found {
		mode = "inherited"
	}
	_, managed := slices.BinarySearch(acl.ManageIDs, kb)
	return accessState{QueryMode: mode, Manage: managed}
}

func impact(scope *Scope, before, after ScopeACL, parentBefore, parentAfter []string) ScopeImpact {
	r := ScopeImpact{
		ScopeID: scope.ID, SubareaID: scope.SubareaID, DisplayName: scope.DisplayName, Changes: []KnowledgeImpact{},
	}
	ids := unionIDs(unionIDs(before.QueryIDs, before.ManageIDs), unionIDs(after.QueryIDs, after.ManageIDs))
	ids = unionIDs(ids, unionIDs(parentBefore, parentAfter))
	for _, id := range ids {
		old, next := effectiveState(id, before, parentBefore), effectiveState(id, after, parentAfter)
		if old != next {
			r.Changes = append(r.Changes, KnowledgeImpact{KnowledgeBaseID: id, Before: old, After: next})
		}
	}
	return r
}

// Resolve the same native Editor grant as the individual binding endpoints.
// The scope store accepts only locally owned KBs; shared KBs are not bindable.
func authorizeACL(tx *gorm.DB, caller types.Caller, tenant uint64, ids []string, write bool) error {
	if caller.TenantID != tenant {
		return access.ErrForbidden
	}
	for batch := range slices.Chunk(ids, 300) {
		var kbs []types.KnowledgeBase
		q := tx.Select("id, tenant_id").
			Where("tenant_id = ? AND id IN ? AND deleted_at IS NULL", tenant, batch).Order("id")
		if write {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := q.Find(&kbs).Error; err != nil {
			return err
		}
		if len(kbs) != len(batch) {
			return access.ErrNotFound
		}
		for i := range kbs {
			if _, err := access.ResolveKB(tx.Statement.Context, access.KBRequest{Caller: caller},
				&kbs[i], types.OrgRoleEditor, nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) changeConfiguration(
	ctx context.Context, caller types.Caller, tenant uint64, id, revision, token string, cfg ScopeConfiguration, save bool,
) (out ConfigurationPreview, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		f, err := readFamily(tx, tenant, id)
		if err != nil {
			return err
		}
		if revision != f.Revision {
			return errStalePreview
		}
		cfg, err = normalizeConfiguration(cfg, f.Scope)
		if err != nil {
			return err
		}
		out = ConfigurationPreview{Token: fingerprint([]any{revision, cfg}), Impacts: []ScopeImpact{}}
		if save && token != out.Token {
			return errStalePreview
		}
		kbs := unionIDs(unionIDs(f.QueryIDs, f.ManageIDs), unionIDs(cfg.QueryIDs, cfg.ManageIDs))
		if f.Scope.InheritParent || *cfg.InheritParent {
			kbs = unionIDs(kbs, f.ParentQueryIDs)
		}
		if err := authorizeACL(tx, caller, tenant, kbs, save); err != nil {
			return err
		}
		// KB deletion locks the KB before removing old bindings. Check again
		// after obtaining the same locks, before writing the reviewed change.
		if save {
			fresh, err := readFamily(tx, tenant, id)
			if err != nil {
				return err
			}
			if fresh.Revision != revision {
				return errStalePreview
			}
		}
		var beforeParent, afterParent []string
		if f.Scope.InheritParent {
			beforeParent = f.ParentQueryIDs
		}
		if *cfg.InheritParent {
			afterParent = f.ParentQueryIDs
		}
		out.Impacts = append(out.Impacts, impact(f.Scope, f.ScopeACL, cfg.ScopeACL, beforeParent, afterParent))
		if !slices.Equal(f.QueryIDs, cfg.QueryIDs) {
			for i := range f.children {
				child := &f.children[i]
				if child.InheritParent {
					acl := f.acls[child.ID]
					if diff := impact(child, acl, acl, f.QueryIDs, cfg.QueryIDs); len(diff.Changes) > 0 {
						out.Impacts = append(out.Impacts, diff)
					}
				}
			}
		}
		if !save {
			return nil
		}
		for _, table := range []string{"octo_scope_bindings", "octo_scope_knowledge_grants"} {
			old, next := f.QueryIDs, cfg.QueryIDs
			if table == "octo_scope_knowledge_grants" {
				old, next = f.ManageIDs, cfg.ManageIDs
			}
			for _, kb := range old {
				if !slices.Contains(next, kb) {
					if err := tx.Exec("DELETE FROM "+table+" WHERE tenant_id = ? AND scope_id = ? AND knowledge_base_id = ?",
						tenant, id, kb).Error; err != nil {
						return err
					}
				}
			}
			for _, kb := range next {
				if slices.Contains(old, kb) {
					continue
				}
				var row any = &Binding{TenantID: tenant, ScopeID: id, KnowledgeBaseID: kb}
				if table == "octo_scope_knowledge_grants" {
					row = &KnowledgeManagementGrant{TenantID: tenant, ScopeID: id, KnowledgeBaseID: kb}
				}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
					return err
				}
			}
		}
		settings := map[string]any{
			"inherit_parent":           *cfg.InheritParent,
			"allow_knowledge_creation": cfg.AllowKnowledgeCreation,
			"aggregate_child_issues":   cfg.AggregateChildIssues,
			"allow_public_web":         cfg.AllowPublicWeb,
		}
		if err := tx.Model(&Scope{}).Where("tenant_id = ? AND id = ?", tenant, id).Updates(settings).Error; err != nil {
			return err
		}
		return audit(tx, ctx, tenant, id, "octo.scope.configured", map[string]any{
			"before": f.ScopeACL, "after": cfg, "prior_inherit_parent": f.Scope.InheritParent,
			"prior_allow_knowledge_creation": f.Scope.AllowKnowledgeCreation,
			"prior_aggregate_child_issues":   f.Scope.AggregateChildIssues,
			"prior_allow_public_web":         f.Scope.AllowPublicWeb,
		})
	})
	return
}

func (s *Store) removeScope(ctx context.Context, caller types.Caller, tenant uint64, id, revision string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		f, err := readFamily(tx, tenant, id)
		if err != nil {
			return err
		}
		if revision != f.Revision {
			return errStalePreview
		}
		if len(f.children) > 0 {
			return errScopeChildren
		}
		var activeReports int64
		if err := tx.Table("octo_report_schedules").Where("tenant_id = ? AND scope_id = ? AND enabled = ?", tenant, id, true).
			Count(&activeReports).Error; err != nil {
			return err
		}
		if activeReports > 0 {
			return errScopeSchedule
		}
		if err := authorizeACL(tx, caller, tenant, unionIDs(f.QueryIDs, f.ManageIDs), true); err != nil {
			return err
		}
		if err := tx.Where("tenant_id = ? AND scope_id = ?", tenant, id).
			Delete(&KnowledgeManagementGrant{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tenant_id = ? AND scope_id = ?", tenant, id).Delete(&Binding{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tenant_id = ? AND id = ?", tenant, id).Delete(&Scope{}).Error; err != nil {
			return err
		}
		return audit(tx, ctx, tenant, id, "octo.scope.removed", map[string]any{
			"account_id": f.Scope.AccountID, "group_id": f.Scope.GroupID,
			"subarea_id": f.Scope.SubareaID, "prior_configuration": f.ScopeACL,
		})
	})
}
