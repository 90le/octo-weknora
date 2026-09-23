package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestPrivateGORMLoggerDoesNotWriteSQLValuesOrDriverError(t *testing.T) {
	const marker = "B074-PRIVACY-BADAGENT-0717"
	var captured bytes.Buffer
	logger.SetOutput(&captured)
	t.Cleanup(logger.ConfigureFromEnv)

	db, err := gorm.Open(sqlite.Open("file:private-gorm-logger?mode=memory&cache=shared"), &gorm.Config{
		Logger: NewPrivateGORMLogger().LogMode(gormlogger.Info),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec("CREATE TABLE privacy_checks (id TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Exec("INSERT INTO privacy_checks (id) VALUES (?)", marker).Error; err != nil {
		t.Fatalf("insert row: %v", err)
	}
	var row struct{ ID string }
	if err := db.Table("privacy_checks").Where("id = ?", marker).First(&row).Error; err != nil {
		t.Fatalf("lookup row: %v", err)
	}
	if row.ID != marker {
		t.Fatalf("query result changed: %q", row.ID)
	}
	if err := db.Table("privacy_checks").Where("id = ?", marker+"-absent").First(&row).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("record-not-found behavior changed: %v", err)
	}
	if err := db.Exec("SELECT invalid_column FROM privacy_checks WHERE id = ?", marker).Error; err == nil {
		t.Fatal("expected SQL error")
	}

	// Even SQL text or driver messages containing values must not be logged.
	NewPrivateGORMLogger().Trace(context.Background(), time.Now().Add(-time.Second),
		func() (string, int64) { return "SELECT '" + marker + "'", 1 },
		fmt.Errorf("driver echoed %s", marker))
	logs := captured.String()
	if strings.Contains(logs, marker) {
		t.Fatal("ordinary GORM logs exposed SQL values or driver error text")
	}
	for _, required := range []string{"status=ok", "status=error", "elapsed_ms=", "error_type=", "record_not_found=true"} {
		if !strings.Contains(logs, required) {
			t.Fatalf("ordinary GORM logs lost diagnostic field %q", required)
		}
	}
}
