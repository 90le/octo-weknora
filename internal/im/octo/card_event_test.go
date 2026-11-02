package octo

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCardActionParsesTrustedEnvelopeWithoutTrustingFormIdentity(t *testing.T) {
	raw := `{"event_id":42,"event_type":"card_action","event_data":{
		"message_id":"2075125733327278080","channel_id":"group____123","channel_type":5,
		"action_id":"confirm","operator_uid":"alice","inputs":{"comment":"ok","count":0,"approved":false},
		"data":{"operator_uid":"mallory"}
	}}`
	got, err := parseCardAction([]byte(raw))
	if err != nil || got.EventID != 42 || got.OperatorUID != "alice" ||
		got.Inputs["count"] != "0" || got.Inputs["approved"] != "false" {
		t.Fatalf("invalid action: %+v, %v", got, err)
	}
	binding := cardBinding{
		MessageID: "2075125733327278080", ChannelID: "group____123",
		ChannelType: 5, OperatorUID: "alice", Actions: map[string]bool{"confirm": true},
	}
	if !binding.matches(got) {
		t.Fatal("original operator and scope did not match")
	}
	for _, change := range []func(*cardBinding){
		func(b *cardBinding) { b.OperatorUID = "mallory" },
		func(b *cardBinding) { b.ChannelID = "other____123" },
		func(b *cardBinding) { b.MessageID = "99" },
		func(b *cardBinding) { b.Actions = map[string]bool{"cancel": true} },
	} {
		changed := binding
		change(&changed)
		if changed.matches(got) {
			t.Fatal("different identity, scope, message or action was accepted")
		}
	}
}

func TestCardActionDoesNotConsumeUnknownOrMalformedEvents(t *testing.T) {
	if _, err := parseCardAction([]byte(`{"event_id":42,"event_type":"bot_task"}`)); !errors.Is(
		err,
		errNotCardAction,
	) {
		t.Fatalf("unknown event should remain distinguishable: %v", err)
	}
	for _, raw := range []string{
		`{"event_id":0,"event_type":"card_action","event_data":{}}`,
		`{"event_id":1,"event_type":"card_action","event_data":` +
			`{"message_id":"../","channel_id":"g","channel_type":2,"operator_uid":"u","action_id":"confirm"}}`,
		`{"event_id":1,"event_type":"card_action","event_data":` +
			`{"message_id":"123","channel_id":"g","channel_type":9,"operator_uid":"u","action_id":"confirm"}}`,
		`{"event_id":1,"event_type":"card_action","event_data":` +
			`{"message_id":"123","channel_id":"g","channel_type":2,"operator_uid":"u",` +
			`"action_id":"confirm","inputs":{"x":{"operator_uid":"u"}}}}`,
	} {
		if _, err := parseCardAction([]byte(raw)); err == nil {
			t.Fatal("malformed action was accepted")
		}
	}
	long := strings.Repeat("a", 4097)
	raw := fmt.Sprintf(`{"event_id":1,"event_type":"card_action","event_data":`+
		`{"message_id":"123","channel_id":"g","channel_type":2,"operator_uid":"u",`+
		`"action_id":"confirm","inputs":{"note":%q}}}`, long)
	if _, err := parseCardAction([]byte(raw)); err == nil {
		t.Fatal("oversized card input was accepted")
	}
}
