package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOctoFileDeliveryMigrationPreservesExistingDataAndRollbackEvidence(t *testing.T) {
	root := sqliteRepoRoot(t)
	db := openSQLiteDB(t, filepath.Join(t.TempDir(), "file-delivery.db"))
	_, err := db.Exec(`CREATE TABLE knowledges (id TEXT PRIMARY KEY, content TEXT NOT NULL);
		CREATE TABLE octo_message_inbox (message_id TEXT PRIMARY KEY, input TEXT NOT NULL);
		INSERT INTO knowledges VALUES ('kb', 'original synthetic knowledge');
		INSERT INTO octo_message_inbox VALUES ('msg', 'original synthetic input');`)
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(root, "migrations", "sqlite", "000051_octo_file_deliveries.up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(root, "migrations", "sqlite", "000051_octo_file_deliveries.down.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO octo_file_deliveries
		(tenant_id, channel_id, source_message_id, file_key, owner_id, state)
		VALUES (1, 'channel', 'msg', 'key', 'owner', 'sending')`)
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	_, err = db.Exec(string(up)) // re-up must retain unresolved delivery evidence
	require.NoError(t, err)
	var content, input, state string
	require.NoError(t, db.QueryRow("SELECT content FROM knowledges WHERE id = 'kb'").Scan(&content))
	require.NoError(t, db.QueryRow("SELECT input FROM octo_message_inbox WHERE message_id = 'msg'").Scan(&input))
	require.NoError(t, db.QueryRow("SELECT state FROM octo_file_deliveries WHERE file_key = 'key'").Scan(&state))
	require.Equal(t, "original synthetic knowledge", content)
	require.Equal(t, "original synthetic input", input)
	require.Equal(t, "sending", state)
}

func TestOctoFileDeliveryPostgresMigrationIsAdditive(t *testing.T) {
	root := sqliteRepoRoot(t)
	for _, name := range []string{"000133_octo_file_deliveries.up.sql", "000133_octo_file_deliveries.down.sql"} {
		data, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", name))
		require.NoError(t, err)
		var sql []string
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				sql = append(sql, strings.ToUpper(line))
			}
		}
		statement := strings.Join(sql, "\n")
		for _, destructive := range []string{"DROP TABLE", "ALTER TABLE", "DELETE FROM", "UPDATE ", "TRUNCATE "} {
			require.NotContains(t, statement, destructive, name)
		}
	}
}
