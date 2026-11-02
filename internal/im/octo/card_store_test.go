package octo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func cardStoreFixture(t *testing.T) (*Adapter, string) {
	t.Helper()
	a, msg := durableAdapter(t)
	if err := a.db.Create(&Inbox{
		ChannelID: a.channelID, TenantID: a.tenantID, MessageID: msg.MessageID,
		Input: "existing non-sensitive inbox", Authority: "original", State: "delivered",
	}).Error; err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "sqlite",
		"000050_octo_card_confirmations.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Exec(string(script)).Error; err != nil {
		t.Fatal(err)
	}
	if got := inboxRow(t, a, msg.MessageID); got.Input != "existing non-sensitive inbox" ||
		got.Authority != "original" || got.State != "delivered" {
		t.Fatal("existing inbox changed while applying additive migration")
	}
	return a, msg.MessageID
}

func TestCardEventStoreIsIdempotentAndCursorNeverMovesBackwards(t *testing.T) {
	a, _ := cardStoreFixture(t)
	ctx := context.Background()
	action := cardAction{
		EventID: 42, MessageID: "123", ChannelID: "group", ChannelType: 2,
		OperatorUID: "alice", ActionID: "confirm",
	}
	if err := a.recordCardEvent(ctx, action); err != nil {
		t.Fatal(err)
	}
	otherAction := action
	otherAction.MessageID = "124"
	if err := a.recordCardEvent(ctx, otherAction); err != nil {
		t.Fatal(err)
	}
	var collisions int64
	err := a.db.Model(&storedCardEvent{}).Where("channel_id = ? AND event_id = ?",
		a.channelID, 42).Count(&collisions).Error
	if err != nil || collisions != 2 {
		t.Fatalf("colliding event identities lost: %d %v", collisions, err)
	}
	if err := a.db.Model(&storedCardEvent{}).Where("channel_id = ? AND event_id = ?", a.channelID, 42).
		Where("event_key = ?", cardEventKey(action)).Update("state", "completed").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.recordCardEvent(ctx, action); err != nil {
		t.Fatal(err)
	}
	var event storedCardEvent
	err = a.db.Where("channel_id = ? AND event_key = ?", a.channelID, cardEventKey(action)).First(&event).Error
	if err != nil || event.State != "completed" {
		t.Fatal("redelivery replaced a completed event")
	}
	for _, id := range []uint64{42, 10, 43} {
		if err := a.advanceCardCursor(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	var cursor cardEventCursor
	err = a.db.Where("channel_id = ? AND tenant_id = ?", a.channelID, a.tenantID).First(&cursor).Error
	if err != nil || cursor.EventID != 43 {
		t.Fatal("card event cursor did not advance monotonically")
	}
	otherTenant := &Adapter{db: a.db, channelID: a.channelID, tenantID: a.tenantID + 1}
	if err := otherTenant.advanceCardCursor(ctx, 50); err == nil {
		t.Fatal("another tenant changed the cursor")
	}
}

func TestCardEventStoreRejectsFormInputsForConfirmation(t *testing.T) {
	a, _ := cardStoreFixture(t)
	action := cardAction{EventID: 42, Inputs: map[string]string{"unexpected": "value"}}
	if err := a.recordCardEvent(context.Background(), action); err == nil {
		t.Fatal("confirmation event with unexpected form inputs was stored")
	}
}
