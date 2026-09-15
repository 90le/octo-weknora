package im

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent/skills"
	"github.com/Tencent/WeKnora/internal/types"
)

var ErrScopeDenied = errors.New("IM execution scope denied")

// ExecutionScope is produced by trusted adapter code, never decoded from message
// text or a callback's self-reported roles. Empty KBs deny execution explicitly.
type ExecutionScope struct {
	KnowledgeBaseIDs []string
	Revision         string
}

// ExecutionAuthorizer runs at admission and again when a queued request executes.
// Adapter implementations must validate native sender identity and membership.
type ExecutionAuthorizer interface {
	AuthorizeExecution(context.Context, *IMChannel, *IncomingMessage) (*ExecutionScope, error)
}

type ExecutionContextProvider interface {
	ExecutionContext(context.Context, *IncomingMessage) (string, skills.SkillSource, error)
}

func authorizeExecution(ctx context.Context, adapter Adapter, channel *IMChannel, msg *IncomingMessage) (*ExecutionScope, error) {
	authorizer, ok := adapter.(ExecutionAuthorizer)
	if !ok {
		if channel.Platform == "octo" {
			return nil, ErrScopeDenied
		}
		return nil, nil
	}
	scope, err := authorizer.AuthorizeExecution(ctx, channel, msg)
	if err != nil {
		return nil, err
	}
	if scope == nil || len(scope.KnowledgeBaseIDs) == 0 || strings.TrimSpace(scope.Revision) == "" {
		return nil, ErrScopeDenied
	}
	copyScope := &ExecutionScope{Revision: scope.Revision}
	seen := map[string]bool{}
	for _, id := range scope.KnowledgeBaseIDs {
		if strings.TrimSpace(id) == "" {
			return nil, ErrScopeDenied
		}
		if !seen[id] {
			copyScope.KnowledgeBaseIDs = append(copyScope.KnowledgeBaseIDs, id)
			seen[id] = true
		}
	}
	sort.Strings(copyScope.KnowledgeBaseIDs)
	return copyScope, nil
}

func scopeFingerprint(scope *ExecutionScope) string {
	if scope == nil {
		return ""
	}
	data, _ := json.Marshal(scope)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func sessionScopeMetadata(scope *ExecutionScope) types.JSON {
	if scope == nil {
		return nil
	}
	data, _ := json.Marshal(map[string]string{"execution_scope": scopeFingerprint(scope)})
	return types.JSON(data)
}

func sessionScopeMatches(session *ChannelSession, scope *ExecutionScope) bool {
	if scope == nil {
		return true
	}
	var metadata map[string]string
	if json.Unmarshal(session.Metadata, &metadata) != nil {
		return false
	}
	return metadata["execution_scope"] == scopeFingerprint(scope)
}

// scopeAgent uses a per-request deep copy. An IM user must not mutate the shared
// Agent configuration or widen a selected knowledge collection through a tool.
// Public scoped channels initially expose audited read-only knowledge tools.
// Additional tools require an explicit capability policy before being enabled.
func scopeAgent(agent *types.CustomAgent, scope *ExecutionScope) (*types.CustomAgent, error) {
	if scope == nil {
		return agent, nil
	}
	if agent == nil || len(scope.KnowledgeBaseIDs) == 0 {
		return nil, ErrScopeDenied
	}
	data, err := json.Marshal(agent)
	if err != nil {
		return nil, err
	}
	var out types.CustomAgent
	if err = json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	out.Config.KBSelectionMode = "selected"
	out.Config.KnowledgeBases = append([]string(nil), scope.KnowledgeBaseIDs...)
	out.Config.MCPSelectionMode = "none"
	out.Config.MCPServices = nil
	out.Config.SkillsSelectionMode = "none"
	out.Config.SelectedSkills = nil
	out.Config.SandboxConfigID = ""
	out.Config.WebSearchEnabled = false
	out.Config.WebFetchEnabled = false
	out.Config.DataAnalysisEnabled = false
	off := false
	out.Config.MemoryEnabled = &off
	readTools := []string{"knowledge_search", "get_document_info", "list_knowledge_chunks", "grep_chunks", "thinking", "todo_write"}
	out.Config.AllowedTools = nil
	for _, name := range readTools {
		if len(agent.Config.AllowedTools) == 0 {
			out.Config.AllowedTools = append(out.Config.AllowedTools, name)
			continue
		}
		for _, enabled := range agent.Config.AllowedTools {
			if name == enabled {
				out.Config.AllowedTools = append(out.Config.AllowedTools, name)
				break
			}
		}
	}
	if len(out.Config.AllowedTools) == 0 {
		// Empty often means default tools upstream; keep an explicit non-match.
		out.Config.AllowedTools = []string{"__no_scoped_tools__"}
	}
	return &out, nil
}
