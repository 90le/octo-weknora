package octobusiness

import "context"

type KnowledgeTarget struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Name            string `json:"name"`
}

type Configuration struct {
	CanCreateKnowledgeBase   bool              `json:"can_create_knowledge_base"`
	ScopeID                  string            `json:"scope_id"`
	ScopeName                string            `json:"scope_name"`
	GroupID                  string            `json:"group_id"`
	SubareaID                string            `json:"subarea_id"`
	IsDirect                 bool              `json:"is_direct"`
	CanManageScope           bool              `json:"can_manage_scope"`
	ReadableKnowledgeBases   []KnowledgeTarget `json:"readable_knowledge_bases"`
	ManageableKnowledgeBases []KnowledgeTarget `json:"manageable_knowledge_bases"`
}

// Configuration exposes only current conversation targets, including delegated
// assets that are no longer bound for retrieval. Listing their names never adds
// them to a retrieval scope or changes a binding.
func (s *Service) Configuration(ctx context.Context) (*Configuration, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	load := func(ids []string) ([]KnowledgeTarget, error) {
		rows := []KnowledgeTarget{}
		if len(ids) == 0 {
			return rows, nil
		}
		err := s.db.WithContext(ctx).Table("knowledge_bases").Select("id AS knowledge_base_id, name").Where("tenant_id = ? AND id IN ? AND deleted_at IS NULL", p.TenantID, ids).Order("name, id").Scan(&rows).Error
		return rows, err
	}
	readable, err := load(p.KnowledgeBaseIDs)
	if err != nil {
		return nil, err
	}
	manageable, err := load(p.ManageKnowledgeBaseIDs)
	if err != nil {
		return nil, err
	}
	return &Configuration{CanCreateKnowledgeBase: p.CanCreateKnowledgeBase, ScopeID: p.ScopeID, ScopeName: p.ScopeName, GroupID: p.GroupID, SubareaID: p.SubareaID, IsDirect: p.IsDirect, CanManageScope: p.CanManageScope, ReadableKnowledgeBases: readable, ManageableKnowledgeBases: manageable}, nil
}
