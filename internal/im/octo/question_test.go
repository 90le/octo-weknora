package octo

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/stretchr/testify/require"
)

func TestOctoQuestionStripsOnlyNativeCurrentBotMentions(t *testing.T) {
	a, err := NewAdapter("bf_test", "bot")
	require.NoError(t, err)
	botLabel, otherLabel := "@Octo 小丘", "@其他人"
	makeEntity := func(text, label, uid string) wire.Entity {
		index := strings.Index(text, label)
		return wire.Entity{
			UID: uid, Offset: len(utf16.Encode([]rune(text[:index]))), Length: len(utf16.Encode([]rune(label))),
		}
	}
	for _, tc := range []struct {
		name  string
		text  string
		want  string
		other bool
	}{
		{"leading", botLabel + " 请回复 OK", "请回复 OK", false},
		{"trailing", "😀模型测试，回复 OK " + botLabel, "😀模型测试，回复 OK", false},
		{
			"multiple mentions", "请 " + botLabel + " 对照 " + otherLabel + " 提的意见\nOcto 小丘这个名字保留",
			"请 对照 " + otherLabel + " 提的意见\nOcto 小丘这个名字保留", true,
		},
		{"ping only", botLabel, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entities := []wire.Entity{makeEntity(tc.text, botLabel, a.uid)}
			if tc.other {
				entities = append(entities, makeEntity(tc.text, otherLabel, "person"))
			}
			body, err := json.Marshal(map[string]any{"type": 1, "content": tc.text, "mention": wire.Mention{
				UIDs: []string{a.uid}, Entities: entities,
			}})
			require.NoError(t, err)
			msg, err := a.Normalize(&wire.Message{ID: "123", Sender: "user", Channel: "group", ChannelType: 2, Payload: body})
			require.NoError(t, err)
			require.NotNil(t, msg)
			clean, ok := msg.Extra[im.OctoQuestionTextKey]
			require.True(t, ok)
			require.Equal(t, tc.want, clean)
			require.Equal(t, "true", msg.Extra["octo_addressed"])
			require.Equal(t, tc.text, msg.Content)
			require.JSONEq(t, string(body), msg.Extra["octo_original_payload"])
		})
	}
	for _, invalid := range [][]wire.Entity{
		nil, // A UID-only mention can route the message but cannot change the text.
		{{UID: "other", Offset: 0, Length: 3}},
		{{UID: "bot", Offset: -1, Length: 3}},
		{{UID: "bot", Offset: 0, Length: 2}}, // splits the emoji's UTF-16 pair
		{{UID: "bot", Offset: 0, Length: 3}, {UID: "other", Offset: 0, Length: 3}},
	} {
		got, changed := questionWithoutBotMention("@😀 later", invalid, a.uid)
		require.False(t, changed)
		require.Equal(t, "@😀 later", got)
	}
}

func TestOctoQuestionKeepsRichTextImageWhileRemovingRoutingMention(t *testing.T) {
	a, err := NewAdapter("bf_test", "bot")
	require.NoError(t, err)
	body := json.RawMessage(`{"type":14,"plain":"[图片]@Octo 小丘","content":[
		{"type":"image","url":"https://cdn.deepminer.com.cn/image.png","name":"image.png","size":72},
		{"type":"text","text":"@Octo 小丘"}],"mention":{"uids":["bot"],
		"entities":[{"uid":"bot","offset":4,"length":8}]}}`)
	msg, err := a.Normalize(&wire.Message{ID: "123", Sender: "user", Channel: "group", ChannelType: 2, Payload: body})
	require.NoError(t, err)
	require.Equal(t, "[图片]", msg.Extra[im.OctoQuestionTextKey])
	images, err := a.InlineImages(msg)
	require.NoError(t, err)
	require.Len(t, images, 1)
	require.Equal(t, "image.png", images[0].FileName)
}
