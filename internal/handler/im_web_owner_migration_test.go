package handler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIMWebOwnerSQLiteRevocation(t *testing.T) {
	cases := []struct {
		name, mutation string
		revoke         bool
	}{
		{"unrelated option", `UPDATE im_channels SET credentials='{"bot_uid":"b","account_id":"a","allow_file_delivery":true}'`, false},
		{"account", `UPDATE im_channels SET credentials='{"account_id":"other","bot_uid":"b"}'`, true},
		{"bot", `UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"other"}'`, true},
		{"invalid json", `UPDATE im_channels SET credentials='{'`, true},
		{"missing identity", `UPDATE im_channels SET credentials='{"account_id":"a"}'`, true},
		{"duplicate identity", `UPDATE im_channels SET credentials='{"account_id":"a","account_id":"other","bot_uid":"b"}'`, true},
		{"unrelated invalid setting", `UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"b","allow_file_delivery":"yes"}'`, false},
		{"unrelated invalid list", `UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"b","allowed_bot_uids":["bad uid"]}'`, false},
		{"unrelated management list", `UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"b","management_bot_uids":["bot"]}'`, false},
		{"unrelated duplicate key", `UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"b","x":1,"x":2}'`, false},
		{"empty identity", `UPDATE im_channels SET credentials='{"account_id":"","bot_uid":"b"}'`, true},
		{"ascii whitespace account", `UPDATE im_channels SET credentials='{"account_id":" \t\r\n","bot_uid":"b"}'`, true},
		{"ascii whitespace bot", `UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"\t"}'`, true},
		{"valid bot lists", `UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"b","allowed_bot_uids":["robot"],"management_bot_uids":["robot"]}'`, false},
		{"platform", `UPDATE im_channels SET platform='slack'`, true},
		{"tenant", `UPDATE im_channels SET tenant_id=2`, true},
		{"channel id", `UPDATE im_channels SET id='new'`, true},
		{"soft delete", `UPDATE im_channels SET deleted_at=CURRENT_TIMESTAMP`, true},
		{"hard delete", `DELETE FROM im_channels`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "owner.db")), &gorm.Config{})
			require.NoError(t, err)
			for _, sql := range []string{
				`CREATE TABLE im_channels(id TEXT PRIMARY KEY, tenant_id INTEGER, platform TEXT, credentials TEXT, deleted_at DATETIME)`,
				`CREATE TABLE im_channel_sessions(id TEXT PRIMARY KEY, tenant_id INTEGER, im_channel_id TEXT, platform TEXT, deleted_at DATETIME)`,
				`CREATE TABLE audit_logs(id INTEGER PRIMARY KEY, tenant_id INTEGER, action TEXT, target_type TEXT, target_id TEXT, target_user_id TEXT, outcome TEXT)`,
				`INSERT INTO im_channels VALUES ('channel',1,'octo','{"account_id":"a","bot_uid":"b"}',NULL)`,
			} {
				require.NoError(t, db.Exec(sql).Error)
			}
			migration, err := os.ReadFile("../../migrations/sqlite/000052_im_channel_web_owners.up.sql")
			require.NoError(t, err)
			require.NoError(t, db.Exec(string(migration)).Error)
			require.NoError(t, db.Exec(`INSERT INTO im_channel_web_owners(id,tenant_id,im_channel_id,web_user_id) VALUES ('owner',1,'channel','user')`).Error)
			require.NoError(t, db.Exec(tc.mutation).Error)
			var active, audits int64
			require.NoError(t, db.Table("im_channel_web_owners").Where("deleted_at IS NULL").Count(&active).Error)
			require.NoError(t, db.Table("audit_logs").Where("action=?", "im_channel.web_owner_revoked").Count(&audits).Error)
			if tc.revoke {
				require.Zero(t, active)
				require.EqualValues(t, 1, audits)
			} else {
				require.EqualValues(t, 1, active)
				require.Zero(t, audits)
			}
		})
	}
}

func TestIMWebOwnerSQLiteWhitespaceIdentityEvenWhenUnchanged(t *testing.T) {
	for _, credentials := range []string{
		`{"account_id":" \t\r\n","bot_uid":"b"}`,
		`{"account_id":"a","bot_uid":" \t\r\n"}`,
	} {
		db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "owner.db")), &gorm.Config{})
		require.NoError(t, err)
		for _, sql := range []string{
			`CREATE TABLE im_channels(id TEXT PRIMARY KEY, tenant_id INTEGER, platform TEXT, credentials TEXT, deleted_at DATETIME)`,
			`CREATE TABLE im_channel_sessions(id TEXT PRIMARY KEY, tenant_id INTEGER, im_channel_id TEXT, platform TEXT, deleted_at DATETIME)`,
			`CREATE TABLE audit_logs(id INTEGER PRIMARY KEY, tenant_id INTEGER, action TEXT, target_type TEXT, target_id TEXT, target_user_id TEXT, outcome TEXT)`,
		} {
			require.NoError(t, db.Exec(sql).Error)
		}
		migration, err := os.ReadFile("../../migrations/sqlite/000052_im_channel_web_owners.up.sql")
		require.NoError(t, err)
		require.NoError(t, db.Exec(string(migration)).Error)
		require.NoError(t, db.Exec(`INSERT INTO im_channels(id,tenant_id,platform,credentials,deleted_at) VALUES ('channel',1,'octo',?,NULL)`, credentials).Error)
		require.NoError(t, db.Exec(`INSERT INTO im_channel_web_owners(id,tenant_id,im_channel_id,web_user_id) VALUES ('owner',1,'channel','user')`).Error)
		require.NoError(t, db.Exec(`UPDATE im_channels SET credentials=json_set(credentials,'$.allow_file_delivery',json('true'))`).Error)
		var active, audits int64
		require.NoError(t, db.Table("im_channel_web_owners").Where("deleted_at IS NULL").Count(&active).Error)
		require.NoError(t, db.Table("audit_logs").Where("action=?", "im_channel.web_owner_revoked").Count(&audits).Error)
		require.Zero(t, active)
		require.EqualValues(t, 1, audits)
	}
}
