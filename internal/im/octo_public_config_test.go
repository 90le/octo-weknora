package im

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestOctoEditableSettingsNeverExposeCredentialFields(t *testing.T) {
	channels := []IMChannel{{Platform: "octo", Credentials: types.JSON(`{"account_id":"knowledge","bot_uid":"bot","allowed_dm_uids":["owner"],"allowed_bot_uids":[],"bot_token":"bf_never_return_this","api_key":"private-key"}`)}}
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		if SummarizeIMChannelsForRole(channels, role)[0].PublicConfig != nil {
			t.Fatal("access settings exposed to non-admin")
		}
	}
	summary := SummarizeIMChannelsForRole(channels, types.TenantRoleAdmin)
	if summary[0].PublicConfig["account_id"] != "knowledge" || summary[0].PublicConfig["bot_uid"] != "bot" {
		t.Fatal("editable settings lost")
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"bf_never_return_this", "private-key", "bot_token", "api_key"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("credential exposed")
		}
	}
	channels[0].Platform = "wecom"
	if SummarizeIMChannelsForRole(channels, types.TenantRoleOwner)[0].PublicConfig != nil {
		t.Fatal("unrelated platform credentials exposed")
	}
}
