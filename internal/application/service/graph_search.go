package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/access"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type graphSearchService struct {
	graph    interfaces.GraphSearchRepository
	evidence interfaces.GraphSearchEvidenceRepository
}

// NewGraphSearchService constructs the public-query provenance boundary.
func NewGraphSearchService(graph interfaces.GraphSearchRepository,
	evidence interfaces.GraphSearchEvidenceRepository,
) interfaces.GraphSearchService {
	return &graphSearchService{graph: graph, evidence: evidence}
}

func (s *graphSearchService) Search(
	ctx context.Context, kb *types.KnowledgeBase, req types.GraphSearchRequest,
) (*types.GraphSearchResult, error) {
	if kb == nil || !access.HasKBGrant(ctx, kb.ID, kb.TenantID, types.OrgRoleViewer) {
		return nil, apperrors.NewForbiddenError("An explicit read grant for this knowledge base is required")
	}
	if scope, ok := types.TenantAPIKeyScopeFromContext(ctx); ok && !scope.FullAccess &&
		!scope.HasCapability(types.APIKeyCapabilityRetrieve) {
		return nil, apperrors.NewForbiddenError("The API key requires retrieve capability")
	}
	if err := req.Normalize(); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	if !kb.IsGraphEnabled() {
		return nil, apperrors.NewConflictError("Knowledge graph is not enabled for this knowledge base")
	}
	ctx, cancel := context.WithTimeout(ctx, types.GraphSearchTimeout)
	defer cancel()
	namespace := types.NameSpace{KnowledgeBase: kb.ID}
	candidates, err := s.graph.BrowseGraph(ctx, namespace, req)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		return nil, apperrors.NewServiceUnavailableError("Knowledge graph query is temporarily unavailable")
	}
	if candidates == nil || candidates.Graph == nil {
		return nil, apperrors.NewServiceUnavailableError("Knowledge graph backend is not configured")
	}
	result := &types.GraphSearchResult{
		KnowledgeBaseID: kb.ID, Query: req.Query,
		Entities: []types.GraphSearchEntity{}, Relationships: []types.GraphSearchRelation{},
		SourceChunks: []types.GraphSearchSourceChunk{},
		Meta: types.GraphSearchMeta{
			TruncationReasons: []string{}, MaxNodes: req.MaxNodes,
			MaxRelations: req.MaxRelations, MaxSourceChunks: req.MaxSourceChunks,
		},
	}
	mark := func(reason string) {
		result.Meta.Truncated = true
		if !slices.Contains(result.Meta.TruncationReasons, reason) {
			result.Meta.TruncationReasons = append(result.Meta.TruncationReasons, reason)
		}
	}
	for _, reason := range candidates.TruncationReasons {
		mark(reason)
	}
	ids := []string{}
	layoutOnly := req.LayoutOnly()
	evidenceLimit := types.GraphSearchMaxEvidence
	if layoutOnly {
		evidenceLimit = types.GraphBrowseMaxEvidence
	}
	wanted := map[string]bool{}
	eligible := map[string][]string{}
	for _, node := range candidates.Graph.Node {
		if node == nil || node.ID == "" || node.Name == "" || node.KnowledgeID == "" || len(node.Chunks) == 0 {
			continue
		}
		if req.SourceDocument != "" && node.KnowledgeID != req.SourceDocument {
			continue
		}
		if utf8.RuneCountInString(node.Name) > types.GraphSearchMaxNameRunes ||
			len(node.KnowledgeID) > 128 || len(node.Chunks) > types.GraphSearchMaxEvidence {
			mark("entity_fields")
			continue
		}
		chunkIDs := []string{}
		additional := []string{}
		valid := true
		for _, id := range node.Chunks {
			if id == "" || len(id) > 128 {
				valid = false
				mark("entity_fields")
				break
			}
			if !slices.Contains(chunkIDs, id) {
				chunkIDs = append(chunkIDs, id)
				if !wanted[id] {
					additional = append(additional, id)
				}
			}
		}
		if !valid {
			continue
		}
		if len(ids)+len(additional) > evidenceLimit {
			mark("candidate_source_chunks")
			continue
		}
		for _, id := range additional {
			wanted[id] = true
			ids = append(ids, id)
		}
		slices.Sort(chunkIDs)
		eligible[node.ID] = chunkIDs
	}
	sources := map[string]types.GraphSearchSourceChunk{}
	if len(ids) != 0 {
		readEvidence := s.evidence.GetGraphSourceChunks
		if layoutOnly {
			readEvidence = s.evidence.GetGraphSourceMetadata
		}
		rows, err := readEvidence(ctx, kb.TenantID, kb.ID, ids)
		if err != nil {
			logger.ErrorWithFields(ctx, err, nil)
			return nil, apperrors.NewInternalServerError("Failed to validate graph source chunks")
		}
		for _, row := range rows {
			if wanted[row.ID] && row.TenantID == kb.TenantID && row.KnowledgeBaseID == kb.ID {
				sources[row.ID] = row
			}
		}
	}
	// Conservative provenance: every cited chunk must still be enabled, live
	// and in this exact document/KB. Attributes are document-level aggregates;
	// validating just one surviving chunk could expose disabled-source facts.
	nodes := map[string]types.GraphSearchEntity{}
	publicIDs := map[string]bool{}
	returnedSources := map[string]bool{}
	for i, node := range candidates.Graph.Node {
		if node == nil {
			continue
		}
		chunkIDs, ok := eligible[node.ID]
		if !ok {
			if i == 0 && (req.Mode == "focus" || req.Mode == "detail") {
				return result, nil
			}
			continue
		}
		valid, extra := true, 0
		for _, id := range chunkIDs {
			if row, ok := sources[id]; !ok || row.KnowledgeID != node.KnowledgeID {
				valid = false
				break
			}
			if !returnedSources[id] {
				extra++
			}
		}
		if !valid {
			if i == 0 && (req.Mode == "focus" || req.Mode == "detail") {
				return result, nil
			}
			continue
		}
		id := graphSearchPublicID("entity", kb.ID, node.KnowledgeID, node.Name)
		if publicIDs[id] {
			continue
		}
		if len(result.Entities) >= req.MaxNodes {
			mark("max_nodes")
			continue
		}
		if !layoutOnly && req.Mode != "detail" && len(result.SourceChunks)+extra > req.MaxSourceChunks {
			mark("max_source_chunks")
			continue
		}
		attributes := []string{}
		for i, attribute := range node.Attributes {
			if layoutOnly {
				break
			}
			if i == types.GraphSearchMaxAttributes {
				mark("entity_fields")
				break
			}
			text := []rune(attribute)
			if len(text) > types.GraphSearchMaxAttributeRunes {
				attribute = string(text[:types.GraphSearchMaxAttributeRunes])
				mark("entity_fields")
			}
			attributes = append(attributes, attribute)
		}
		entity := types.GraphSearchEntity{
			ID: id, Name: node.Name, KnowledgeID: node.KnowledgeID,
			DocumentTitle: sources[chunkIDs[0]].Title, Attributes: attributes, SourceChunkIDs: []string{},
		}
		for _, id := range chunkIDs {
			if layoutOnly {
				break
			}
			if !returnedSources[id] {
				if len(result.SourceChunks) >= req.MaxSourceChunks {
					mark("max_source_chunks")
					continue
				}
				row := sources[id]
				result.SourceChunks = append(result.SourceChunks, row)
				returnedSources[id] = true
				if row.ContentTruncated || row.TitleTruncated {
					mark("source_fields")
				}
			}
			entity.SourceChunkIDs = append(entity.SourceChunkIDs, id)
		}
		nodes[node.ID] = entity
		publicIDs[id] = true
		result.Entities = append(result.Entities, entity)
	}
	seenRelations := map[string]bool{}
	for _, relation := range candidates.Graph.Relation {
		if relation == nil {
			continue
		}
		if req.RelationType != "" && relation.Type != req.RelationType {
			continue
		}
		source, sourceOK := nodes[relation.SourceID]
		target, targetOK := nodes[relation.TargetID]
		if !sourceOK || !targetOK || source.KnowledgeID != target.KnowledgeID || relation.Type == "" {
			continue
		}
		if utf8.RuneCountInString(relation.Type) > 128 {
			mark("relation_fields")
			continue
		}
		id := graphSearchPublicID("relation", source.ID, target.ID, relation.Type)
		if seenRelations[id] {
			continue
		}
		seenRelations[id] = true
		if len(result.Relationships) >= req.MaxRelations {
			mark("max_relations")
			continue
		}
		chunkIDs := append([]string{}, source.SourceChunkIDs...)
		for _, id := range target.SourceChunkIDs {
			if !slices.Contains(chunkIDs, id) {
				chunkIDs = append(chunkIDs, id)
			}
		}
		slices.Sort(chunkIDs)
		result.Relationships = append(result.Relationships, types.GraphSearchRelation{
			ID: id, SourceID: source.ID, TargetID: target.ID, Type: relation.Type, SourceChunkIDs: chunkIDs,
		})
	}
	if req.Mode == "overview" && req.RelationType != "" {
		connected := map[string]bool{}
		for _, relation := range result.Relationships {
			connected[relation.SourceID] = true
			connected[relation.TargetID] = true
		}
		result.Entities = slices.DeleteFunc(result.Entities, func(entity types.GraphSearchEntity) bool {
			return !connected[entity.ID]
		})
	}
	return result, nil
}

// JSON tuple encoding avoids delimiter collisions; IDs survive graph reindexing
// and never expose Neo4j element IDs or conflate same-named document instances.
func graphSearchPublicID(parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
