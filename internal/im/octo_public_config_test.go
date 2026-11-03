package im

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestOctoEditableSettingsNeverExposeCredentialFields(t *testing.T) {
	credentials := `{"account_id":"knowledge","bot_uid":"bot","allowed_dm_uids":["owner"],` +
		`"allowed_bot_uids":[],"allow_generated_files":true,"bot_token":"bf_never_return_this",` +
		`"api_key":"private-key"}`
	channels := []IMChannel{{Platform: "octo", Credentials: types.JSON(credentials)}}
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		if SummarizeIMChannelsForRole(channels, role)[0].PublicConfig != nil {
			t.Fatal("access settings exposed to non-admin")
		}
	}
	summary := SummarizeIMChannelsForRole(channels, types.TenantRoleAdmin)
	if summary[0].PublicConfig["account_id"] != "knowledge" || summary[0].PublicConfig["bot_uid"] != "bot" {
		t.Fatal("editable settings lost")
	}
	if summary[0].PublicConfig["allow_generated_files"] != true {
		t.Fatal("file capability not shown to the channel administrator")
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

func TestOctoGeneratedFileSettingMustBeBoolean(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{`true`, true}, {`false`, true}, {`"true"`, false}, {`1`, false}, {`null`, false},
	} {
		credentials := `{"account_id":"account","bot_uid":"bot","allow_generated_files":` + tc.value + `}`
		ch := &IMChannel{
			Platform: "octo", Mode: "websocket", SessionMode: "user",
			Credentials: types.JSON(credentials),
		}
		if (ch.validateSessionMode() == nil) != tc.valid {
			t.Fatalf("invalid validation result for %s", tc.value)
		}
	}
}
