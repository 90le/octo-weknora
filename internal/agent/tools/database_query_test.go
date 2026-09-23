package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestDatabaseQueryInjectsEnabledChunkFilter(t *testing.T) {
	tool := NewDatabaseQueryTool(nil, types.SearchTargets{{
		Type:            types.SearchTargetTypeKnowledgeBase,
		KnowledgeBaseID: "kb-1",
	}})

	securedSQL, err := tool.validateAndSecureSQL(
		"SELECT c.id, c.content FROM chunks c WHERE c.chunk_type = 'faq'",
		1,
	)
	if err != nil {
		t.Fatalf("validateAndSecureSQL() error = %v", err)
	}
	if !strings.Contains(securedSQL, "c.is_enabled = true") {
		t.Fatalf("Agent SQL must exclude disabled chunks:\n%s", securedSQL)
	}
}

func TestDatabaseQueryDoesNotLogSQLOrRowContent(t *testing.T) {
	const sqlMarker = "SECRETABC123_SQL"
	const rowMarker = "SECRETABC123_ROW"

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"content"}).AddRow(rowMarker))

	var logs bytes.Buffer
	logger.SetOutput(&logs)
	t.Cleanup(logger.ConfigureFromEnv)
	tool := NewDatabaseQueryTool(db, types.SearchTargets{{
		Type:            types.SearchTargetTypeKnowledgeBase,
		KnowledgeBaseID: "kb-1",
	}})
	query := "SELECT content FROM chunks WHERE content = '" + sqlMarker + "'"
	args, err := json.Marshal(DatabaseQueryInput{SQL: query})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	result, err := tool.Execute(ctx, args)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute() failed: result=%+v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	logged := logs.String()
	for _, marker := range []string{sqlMarker, rowMarker} {
		if strings.Contains(logged, marker) {
			t.Fatalf("tool log exposed SQL or row content: %s", logged)
		}
	}
	if !strings.Contains(logged, "sql_bytes=") {
		t.Fatalf("expected safe SQL size telemetry, got: %s", logged)
	}
}
