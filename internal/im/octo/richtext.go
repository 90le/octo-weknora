package octo

import (
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"strings"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

const maxRichTextImages = 4

var _ im.InlineImageProvider = (*Adapter)(nil)

// InlineImages preserves the text question and returns only the images from
// its authenticated rich-text payload. DownloadFile applies the same trusted
// CDN, redirect and byte limits as for standalone image messages.
func (a *Adapter) InlineImages(msg *im.IncomingMessage) ([]*im.IncomingMessage, error) {
	if msg == nil || msg.Platform != Platform || msg.MessageType != im.MessageTypeText {
		return nil, nil
	}
	var payload wire.Payload
	if json.Unmarshal([]byte(msg.Extra["octo_original_payload"]), &payload) != nil || payload.Type != 14 {
		return nil, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(payload.Content, &blocks) != nil {
		// Legacy plain-string content has no image blocks.
		var text string
		if json.Unmarshal(payload.Content, &text) == nil {
			return nil, nil
		}
		return nil, errors.New("invalid Octo rich text")
	}
	var images []*im.IncomingMessage
	for _, raw := range blocks {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &kind) != nil || kind.Type != "image" {
			continue
		}
		var image struct {
			URL  string `json:"url"`
			Name string `json:"name"`
			Size int64  `json:"size"`
		}
		if json.Unmarshal(raw, &image) != nil || image.URL == "" || image.Size < 0 {
			return nil, errors.New("invalid Octo rich-text image")
		}
		if len(images) >= maxRichTextImages {
			return nil, errors.New("too many Octo rich-text images")
		}
		resource, err := url.Parse(image.URL)
		if err != nil {
			return nil, errors.New("invalid Octo rich-text image URL")
		}
		if !resource.IsAbs() && resource.Host == "" {
			base, _ := url.Parse("https://cdn.deepminer.com.cn/")
			image.URL = base.ResolveReference(resource).String()
		}
		file := *msg
		file.MessageType = im.MessageTypeImage
		file.FileKey, file.FileSize = image.URL, image.Size
		file.FileName = path.Base(strings.ReplaceAll(image.Name, "\\", "/"))
		if image.Name == "" || file.FileName == "." {
			file.FileName = "image.png"
		}
		images = append(images, &file)
	}
	return images, nil
}
