package im

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type octoSessionService struct{ interfaces.SessionService }

func (s *octoSessionService) CreateSession(_ context.Context, session *types.Session) (*types.Session, error) {
	out := *session
	out.ID = uuid.NewString()
	return &out, nil
}

func TestOctoSessionSeparatesBotsAndChangedKnowledge(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sessions.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	data, err := os.ReadFile("../../migrations/sqlite/000000_init.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "CREATE TABLE IF NOT EXISTS im_channel_sessions (")
	if start < 0 {
		t.Fatal("missing native table")
	}
	end := strings.Index(text[start:], ");")
	if end < 0 {
		t.Fatal("missing DDL end")
	}
	if err = db.Exec(text[start : start+end+2]).Error; err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sqlite/000019_octo_session_scope.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(string(migration)).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{db: db, sessionService: &octoSessionService{}}
	m := &IncomingMessage{Platform: "octo", UserID: "native-user", ChatID: "group____123", executionScope: &ExecutionScope{KnowledgeBaseIDs: []string{"kb-a"}, Revision: "v1"}}
	a, err := svc.resolveUserSession(context.Background(), m, 1, "shared-agent", "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.resolveUserSession(context.Background(), m, 1, "shared-agent", "bot-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID == b.SessionID {
		t.Fatal("Bots shared conversation")
	}
	again, err := svc.resolveUserSession(context.Background(), m, 1, "shared-agent", "bot-a")
	if err != nil || again.SessionID != a.SessionID {
		t.Fatal("stable scope lost continuity")
	}
	m.executionScope = &ExecutionScope{KnowledgeBaseIDs: []string{"kb-b"}, Revision: "v2"}
	changed, err := svc.resolveUserSession(context.Background(), m, 1, "shared-agent", "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if changed.SessionID == a.SessionID {
		t.Fatal("old knowledge history reused")
	}
	var archived ChannelSession
	if err = db.Unscoped().First(&archived, "id = ?", a.ID).Error; err != nil || !archived.DeletedAt.Valid {
		t.Fatal("old mapping not retained as archived")
	}
	m.ChatID = ""
	m.ChatType = ChatTypeDirect
	dmA, err := svc.resolveUserSession(context.Background(), m, 1, "shared-agent", "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	dmB, err := svc.resolveUserSession(context.Background(), m, 1, "shared-agent", "bot-b")
	if err != nil || dmA.SessionID == dmB.SessionID {
		t.Fatal("DM cross-Bot collision")
	}
}
