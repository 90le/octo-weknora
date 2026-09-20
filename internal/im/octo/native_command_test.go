package octo

import (
	"encoding/json"
	"testing"
	"unicode/utf16"

	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

func TestNativeLeadingMentionNormalizesCommandAfterBotRename(t *testing.T) {
	a, _ := NewAdapter("bf_test", "stable-bot-uid")
	label := "@Renamed 😀 helper"
	command := "确认 OP-00000000-0000-0000-0000-000000000001"
	body, _ := json.Marshal(map[string]any{"type": 1, "content": label + " " + command, "mention": map[string]any{"entities": []wire.Entity{{UID: a.uid, Offset: 0, Length: len(utf16.Encode([]rune(label)))}}}})
	msg, err := a.Normalize(&wire.Message{ID: "2098355867442221056", Sender: "user", Channel: "group", ChannelType: 2, Payload: body})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != label+" "+command || msg.Extra["octo_command_text"] != command || msg.Extra["octo_addressed"] != "true" {
		t.Fatalf("native command or original query lost: %+v", msg)
	}
}

func TestNativeCommandDoesNotStripOtherUserOrBrokenUTF16Entity(t *testing.T) {
	for _, tc := range []struct {
		text   string
		entity wire.Entity
	}{{"@Other 确认 OP-x", wire.Entity{UID: "other", Offset: 0, Length: 6}}, {"前文 @Bot 确认 OP-x", wire.Entity{UID: "bot", Offset: 3, Length: 4}}, {"@😀 确认 OP-x", wire.Entity{UID: "bot", Offset: 0, Length: 2}}, {"@Botattached", wire.Entity{UID: "bot", Offset: 0, Length: 4}}} {
		if _, ok := leadingNativeCommandText(tc.text, []wire.Entity{tc.entity}, "bot"); ok {
			t.Fatalf("non-leading or invalid entity stripped: %+v", tc)
		}
	}
}
