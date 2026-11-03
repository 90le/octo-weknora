package octo

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

const fileDeliveryTurnKey = "@turn"

var (
	errFileJournalUnavailable = errors.New("file delivery journal unavailable")
	errFileDeliveryUncertain  = errors.New("file delivery outcome uncertain")
	errFileDeliveryFenced     = errors.New("file delivery execution superseded")
)

// fileDeliveryEntry stores metadata only. In particular it must never contain
// file bytes, signed storage URLs, or credentials. The special turn row fences
// executions; all other rows describe individual file attempts.
type fileDeliveryEntry struct {
	TenantID          uint64 `gorm:"primaryKey"`
	ChannelID         string `gorm:"primaryKey"`
	SourceMessageID   string `gorm:"primaryKey"`
	FileKey           string `gorm:"primaryKey"`
	OwnerID           string
	State             string
	FileName          string
	ContentHash       string
	ClientMsgNo       string
	PlatformMessageID string
	ErrorCode         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (fileDeliveryEntry) TableName() string { return "octo_file_deliveries" }

type fileTurn struct {
	tenant  uint64
	channel string
	source  string
}

func (t fileTurn) valid() bool {
	return t.tenant != 0 && t.channel != "" && t.source != "" && len(t.channel) <= 36 && len(t.source) <= 255
}

func (t fileTurn) query(db *gorm.DB) *gorm.DB {
	return db.Where("tenant_id = ? AND channel_id = ? AND source_message_id = ?", t.tenant, t.channel, t.source)
}

type fileDeliveryJournal interface {
	start(context.Context, fileTurn, string) error
	recovery(context.Context, fileTurn) (tracked, unsafe bool, err error)
	reserve(context.Context, fileTurn, string, fileDeliveryEntry) (alreadySent bool, err error)
	markUploading(context.Context, fileTurn, string, string) error
	markSending(context.Context, fileTurn, string, string) error
	finish(context.Context, fileTurn, string, string, string, string, string) error
}

type dbFileDeliveryJournal struct{ db *gorm.DB }

// GORM's default error logger may interpolate private filenames and hashes into
// SQL logs. Callers log stable failure codes instead.
func (j *dbFileDeliveryJournal) withContext(ctx context.Context) *gorm.DB {
	return j.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
}

func (a *Adapter) fileDeliveryStore() fileDeliveryJournal {
	if a.fileJournal != nil {
		return a.fileJournal
	}
	if a.db == nil {
		return nil
	}
	return &dbFileDeliveryJournal{db: a.db}
}

type fileDeliveryOwnerKey struct{}

func (a *Adapter) startFileDeliveryTurn(ctx context.Context, source string) (context.Context, error) {
	journal := a.fileDeliveryStore()
	turn := fileTurn{tenant: a.tenantID, channel: a.channelID, source: source}
	if journal == nil || !turn.valid() {
		return ctx, errFileJournalUnavailable
	}
	// The owner is shared by this execution's context. The turn row is created
	// lazily by the first file attempt, not by ordinary text messages.
	return context.WithValue(ctx, fileDeliveryOwnerKey{}, uuid.NewString()), nil
}

func (j *dbFileDeliveryJournal) lockTurn(tx *gorm.DB, t fileTurn) (fileDeliveryEntry, error) {
	var turn fileDeliveryEntry
	err := t.query(tx.Clauses(clause.Locking{Strength: "UPDATE"})).Where("file_key = ?", fileDeliveryTurnKey).First(&turn).Error
	return turn, err
}

func (j *dbFileDeliveryJournal) start(ctx context.Context, t fileTurn, owner string) error {
	if !t.valid() || owner == "" {
		return errFileJournalUnavailable
	}
	return j.withContext(ctx).Transaction(func(tx *gorm.DB) error {
		turn := fileDeliveryEntry{TenantID: t.tenant, ChannelID: t.channel, SourceMessageID: t.source, FileKey: fileDeliveryTurnKey, OwnerID: owner, State: "turn"}
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&turn)
		if insert.Error != nil {
			return insert.Error
		}
		stored, err := j.lockTurn(tx, t)
		if err != nil {
			return err
		}
		if stored.State != "turn" {
			return errFileJournalUnavailable
		}
		if insert.RowsAffected == 1 {
			// A missing turn alongside file rows is an inconsistent ledger, not
			// evidence that a prior message was never sent.
			var files int64
			if err := t.query(tx.Model(&fileDeliveryEntry{})).Where("file_key <> ?", fileDeliveryTurnKey).Count(&files).Error; err != nil {
				return err
			}
			if files != 0 {
				return errFileDeliveryUncertain
			}
		}
		if stored.OwnerID == owner {
			return nil
		}
		var count int64
		if err := t.query(tx.Model(&fileDeliveryEntry{})).Where("file_key <> ? AND state NOT IN ?", fileDeliveryTurnKey, []string{"preparing", "failed"}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errFileDeliveryUncertain
		}
		result := t.query(tx.Model(&fileDeliveryEntry{})).Where("file_key = ? AND owner_id = ?", fileDeliveryTurnKey, stored.OwnerID).
			Updates(map[string]any{"owner_id": owner, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errFileDeliveryFenced
		}
		return nil
	})
}

func (j *dbFileDeliveryJournal) recovery(ctx context.Context, t fileTurn) (bool, bool, error) {
	if !t.valid() {
		return false, false, errFileJournalUnavailable
	}
	var turns, files, unsafe int64
	if err := t.query(j.withContext(ctx).Model(&fileDeliveryEntry{})).Where("file_key = ? AND state = ?", fileDeliveryTurnKey, "turn").Count(&turns).Error; err != nil {
		return false, false, err
	}
	if err := t.query(j.withContext(ctx).Model(&fileDeliveryEntry{})).Where("file_key <> ? AND state NOT IN ?", fileDeliveryTurnKey, []string{"preparing", "failed"}).Count(&unsafe).Error; err != nil {
		return false, false, err
	}
	if turns == 0 {
		if err := t.query(j.withContext(ctx).Model(&fileDeliveryEntry{})).Where("file_key <> ?", fileDeliveryTurnKey).Count(&files).Error; err != nil {
			return false, false, err
		}
	}
	return turns == 1, unsafe != 0 || (turns == 0 && files != 0), nil
}

func (j *dbFileDeliveryJournal) reserve(ctx context.Context, t fileTurn, owner string, entry fileDeliveryEntry) (bool, error) {
	if !t.valid() || owner == "" || entry.FileKey == "" || entry.FileKey == fileDeliveryTurnKey {
		return false, errFileJournalUnavailable
	}
	alreadySent := false
	err := j.withContext(ctx).Transaction(func(tx *gorm.DB) error {
		turn, err := j.lockTurn(tx, t)
		if err != nil {
			return err
		}
		if turn.State != "turn" || turn.OwnerID != owner {
			return errFileDeliveryFenced
		}
		var existing fileDeliveryEntry
		lookup := t.query(tx).Where("file_key = ?", entry.FileKey).Limit(1).Find(&existing)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 1 {
			switch existing.State {
			case "sent":
				if existing.PlatformMessageID == "" || existing.ClientMsgNo != entry.ClientMsgNo || existing.ContentHash != entry.ContentHash || existing.FileName != entry.FileName {
					return errFileDeliveryUncertain
				}
				alreadySent = true
				return nil
			case "uploading", "sending", "unknown", "not_sent":
				return errFileDeliveryUncertain
			case "preparing":
				if existing.OwnerID == owner {
					return errFileDeliveryUncertain
				}
			case "failed":
			default:
				return errFileJournalUnavailable
			}
		}
		// A changed payload with the same name cannot bypass an ambiguous send.
		var ambiguous int64
		if err := t.query(tx.Model(&fileDeliveryEntry{})).Where("file_key <> ? AND file_name = ? AND state IN ?", fileDeliveryTurnKey, entry.FileName, []string{"uploading", "sending", "unknown", "not_sent"}).Count(&ambiguous).Error; err != nil {
			return err
		}
		if ambiguous != 0 {
			return errFileDeliveryUncertain
		}
		entry.TenantID, entry.ChannelID, entry.SourceMessageID = t.tenant, t.channel, t.source
		entry.OwnerID, entry.State = owner, "preparing"
		if lookup.RowsAffected == 0 {
			return tx.Create(&entry).Error
		}
		result := t.query(tx.Model(&fileDeliveryEntry{})).Where("file_key = ? AND state IN ?", entry.FileKey, []string{"failed", "preparing"}).
			Updates(map[string]any{"owner_id": owner, "state": "preparing", "error_code": "", "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errFileDeliveryFenced
		}
		return nil
	})
	return alreadySent, err
}

func (j *dbFileDeliveryJournal) markUploading(ctx context.Context, t fileTurn, owner, key string) error {
	return j.transition(ctx, t, owner, key, "preparing", "uploading")
}

func (j *dbFileDeliveryJournal) markSending(ctx context.Context, t fileTurn, owner, key string) error {
	return j.transition(ctx, t, owner, key, "uploading", "sending")
}

func (j *dbFileDeliveryJournal) transition(ctx context.Context, t fileTurn, owner, key, from, to string) error {
	return j.withContext(ctx).Transaction(func(tx *gorm.DB) error {
		turn, err := j.lockTurn(tx, t)
		if err != nil {
			return err
		}
		if turn.State != "turn" || turn.OwnerID != owner {
			return errFileDeliveryFenced
		}
		result := t.query(tx.Model(&fileDeliveryEntry{})).Where("file_key = ? AND owner_id = ? AND state = ?", key, owner, from).
			Updates(map[string]any{"state": to, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errFileDeliveryFenced
		}
		return nil
	})
}

func (j *dbFileDeliveryJournal) finish(ctx context.Context, t fileTurn, owner, key, state, platformID, code string) error {
	previous := []string{"sending"}
	if state == "failed" {
		previous = []string{"preparing"}
	} else if state == "not_sent" {
		// The message was not sent, but its bytes already left for storage.
		// Preserve that fact as a recovery barrier.
	} else if state == "unknown" {
		previous = []string{"uploading", "sending"}
	} else if state != "sent" && state != "unknown" {
		return errFileJournalUnavailable
	}
	result := t.query(j.withContext(ctx).Model(&fileDeliveryEntry{})).Where("file_key = ? AND owner_id = ? AND state IN ?", key, owner, previous).
		Updates(map[string]any{"state": state, "platform_message_id": platformID, "error_code": code, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errFileDeliveryFenced
	}
	return nil
}
