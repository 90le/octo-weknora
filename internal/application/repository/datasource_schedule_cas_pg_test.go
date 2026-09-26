package repository

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Run against a disposable database named weknora_test_* by setting
// WEKNORA_TEST_POSTGRES_DSN. The name guard prevents accidental migration or
// writes to a production database. This checks the actual PostgreSQL column
// precision and JSON timestamp round trip used by the browser preview/apply.
func TestGitHubScheduleCASPostgresTimestampRoundTrip(t *testing.T) {
	dsn := os.Getenv("WEKNORA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL DSN not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	var databaseName string
	require.NoError(t, db.Raw("SELECT current_database()").Scan(&databaseName).Error)
	require.True(t, strings.HasPrefix(databaseName, "weknora_test_"), "refusing to mutate non-test PostgreSQL database %q", databaseName)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}, &types.SyncLog{}))
	repo := NewDataSourceRepository(db).(*DataSourceRepository)
	id := uuid.NewString()
	ds := &types.DataSource{
		ID: id, TenantID: 17, KnowledgeBaseID: "kb-" + id, Type: types.ConnectorTypeGitHub,
		Status: types.DataSourceStatusActive, SyncSchedule: "0 0 */6 * * *",
	}
	require.NoError(t, repo.Create(context.Background(), ds))
	t.Cleanup(func() { _ = db.Unscoped().Where("id = ?", id).Delete(&types.DataSource{}).Error })
	stored, err := repo.FindByID(context.Background(), id)
	require.NoError(t, err)
	require.False(t, stored.UpdatedAt.IsZero())
	require.Equal(t, 0, stored.UpdatedAt.Nanosecond()%1000, "PostgreSQL timestamp is microsecond precision")
	encoded, err := json.Marshal(stored.UpdatedAt)
	require.NoError(t, err)
	var browserRoundTrip time.Time
	require.NoError(t, json.Unmarshal(encoded, &browserRoundTrip))
	require.True(t, stored.UpdatedAt.Equal(browserRoundTrip))
	applied, err := repo.UpdateGitHubScheduleIfUnchanged(context.Background(), 17, ds.KnowledgeBaseID, id,
		browserRoundTrip, "0 17 2,8,14,20 * * *")
	require.NoError(t, err)
	require.True(t, applied, "preview timestamp must match the real PostgreSQL row")
	applied, err = repo.UpdateGitHubScheduleIfUnchanged(context.Background(), 17, ds.KnowledgeBaseID, id,
		browserRoundTrip, "0 17 2,8,14,20 * * *")
	require.NoError(t, err)
	require.False(t, applied, "stale preview must not write again")
}
