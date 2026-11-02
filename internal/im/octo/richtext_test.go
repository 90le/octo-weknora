package octo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

func TestRichTextImagesPreserveQuestionAndOrder(t *testing.T) {
	a, _ := NewAdapter("bf_test", "bot")
	raw := &wire.Message{
		ID: "123", Sender: "user", Channel: "bot", ChannelType: 1,
		Payload: json.RawMessage(`{"type":14,"content":[
			{"type":"text","text":"比较"},
			{"type":"image","url":"https://cdn.deepminer.com.cn/a","name":"a.jpg","size":12},
			{"type":"text","text":"和"},
			{"type":"image","url":"files/b","name":"b.png","size":13}
		]}`),
	}
	msg, err := a.Normalize(raw)
	if err != nil || msg == nil || msg.Content != "比较[图片]和[图片]" {
		t.Fatalf("normalized rich-text question = %+v, %v", msg, err)
	}
	images, err := a.InlineImages(msg)
	if err != nil || len(images) != 2 {
		t.Fatalf("images = %d, err = %v", len(images), err)
	}
	if images[0].FileKey != "https://cdn.deepminer.com.cn/a" ||
		images[1].FileKey != "https://cdn.deepminer.com.cn/files/b" ||
		images[0].FileName != "a.jpg" || images[1].FileName != "b.png" {
		t.Fatal("image references or order changed")
	}
	if msg.MessageType != im.MessageTypeText {
		t.Fatal("rich text lost its original question")
	}
}

func TestRichTextRejectsTooManyOrMalformedImages(t *testing.T) {
	a, _ := NewAdapter("bf_test", "bot")
	for _, content := range []string{
		`[{"type":"image","url":"a"},{"type":"image","url":"b"},{"type":"image","url":"c"},` +
			`{"type":"image","url":"d"},{"type":"image","url":"e"}]`,
		`[{"type":"image","url":{}}]`,
	} {
		msg := &im.IncomingMessage{
			Platform: Platform, MessageType: im.MessageTypeText,
			Extra: map[string]string{"octo_original_payload": `{"type":14,"content":` + content + `}`},
		}
		if _, err := a.InlineImages(msg); err == nil {
			t.Fatal("unsupported image payload accepted")
		}
	}
}

func TestRichTextImageStillRequiresTrustedCDN(t *testing.T) {
	a, _ := NewAdapter("bf_test", "bot")
	msg := &im.IncomingMessage{
		Platform: Platform, MessageType: im.MessageTypeText,
		Extra: map[string]string{
			"octo_original_payload": `{"type":14,"content":[{"type":"image","url":"https://example.org/a.png"}]}`,
		},
	}
	images, err := a.InlineImages(msg)
	if err != nil || len(images) != 1 {
		t.Fatalf("parse image failed: %v", err)
	}
	_, _, err = a.DownloadFile(context.Background(), images[0])
	if err == nil || !strings.Contains(err.Error(), "untrusted") {
		t.Fatal("untrusted rich-text image was allowed")
	}
}
