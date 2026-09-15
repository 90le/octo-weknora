package wire

import (
	"encoding/json"
	"testing"
)

func TestSubareaIsNotConversationThread(t *testing.T) {
	s, err := ParseScope("group____2098355867442221056", 5)
	if err != nil || s.Group != "group" || s.Subarea != "2098355867442221056" {
		t.Fatal("subarea lost")
	}
	for _, id := range []string{"group____", "group____12____34", "group____1e20", "group/../../secret"} {
		if _, err = ParseScope(id, 5); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	if _, err = ParseScope("group____123", 2); err == nil {
		t.Fatal("subarea accepted as main group")
	}
}

func TestIDsAndNativeMentions(t *testing.T) {
	for _, raw := range []string{`"2098355867442221056"`, `2098355867442221056`} {
		if ReadID(json.RawMessage(raw)) != "2098355867442221056" {
			t.Fatal("precision lost")
		}
	}
	for _, raw := range []string{`2.098355867442221e18`, `null`, `-1`, `"12/34"`} {
		if ReadID(json.RawMessage(raw)) != "" {
			t.Fatal("invalid ID accepted")
		}
	}
	p := Payload{Content: json.RawMessage(`"@bot @all"`)}
	if p.AddressedTo("bot") {
		t.Fatal("text mention accepted")
	}
	p.Mention.Entities = []Entity{{UID: "bot", Offset: 0, Length: 4}}
	if !p.AddressedTo("bot") {
		t.Fatal("UID mention ignored")
	}
}
