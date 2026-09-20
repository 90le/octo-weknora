package octo

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf16"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/octobusiness"
)

// Contact entities are clickable native identities, not outbound notifications.
// Only the original questioner remains in mention.uids. Contacts must both
// belong to a currently authorized KB and resolve through Octo's user API.
func (a *Adapter) contactEntities(ctx context.Context, in *im.IncomingMessage, text string, payload map[string]any) {
	if a.db == nil {
		return
	}
	scope, err := a.AuthorizeExecution(ctx, nil, in)
	if err != nil || len(scope.KnowledgeBaseIDs) == 0 {
		return
	}
	var contacts []octobusiness.Contact
	if a.db.WithContext(ctx).Select("uid,name").Where("tenant_id = ? AND knowledge_base_id IN ?", a.tenantID, scope.KnowledgeBaseIDs).Limit(100).Find(&contacts).Error != nil {
		return
	}
	mention, ok := payload["mention"].(map[string]any)
	if !ok {
		mention = map[string]any{"uids": []string{}, "entities": []wire.Entity{}}
	}
	entities, _ := mention["entities"].([]wire.Entity)
	seen := map[string]bool{}
	for _, c := range contacts {
		if c.UID == "" || c.Name == "" || len(c.Name) > 128 || strings.ContainsAny(c.Name, "\r\n\t") || seen[c.UID] {
			continue
		}
		label := "@" + c.Name
		at := strings.Index(text, label)
		if at < 0 {
			continue
		}
		var user struct {
			UID string `json:"uid"`
		}
		if a.api.request(ctx, http.MethodGet, "/v1/bot/user/info?uid="+url.QueryEscape(c.UID), nil, &user) != nil || user.UID != c.UID {
			continue
		}
		seen[c.UID] = true
		entities = append(entities, wire.Entity{UID: c.UID, Offset: len(utf16.Encode([]rune(text[:at]))), Length: len(utf16.Encode([]rune(label)))})
		if len(seen) >= 5 {
			break
		}
	}
	if len(entities) > 0 {
		mention["entities"] = entities
		payload["mention"] = mention
	}
}
