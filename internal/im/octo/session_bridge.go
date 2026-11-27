package octo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/google/uuid"
)

const octoMaxSegmentRunes = 3500

// SplitOctoMessage splits long Markdown/text content into tidy segments that
// stay under Octo's single-message size limit, preferring paragraph breaks and
// preserving fenced code blocks across segment boundaries.
func SplitOctoMessage(text string, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxRunes <= 200 {
		maxRunes = octoMaxSegmentRunes
	}
	if len([]rune(text)) <= maxRunes {
		return []string{text}
	}

	paragraphs := strings.Split(text, "\n\n")
	segments := []string{}
	var cur strings.Builder
	curRunes := 0
	inFence := false
	fenceHeader := "```"

	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s == "" {
			cur.Reset()
			curRunes = 0
			return
		}
		if inFence {
			s += "\n```"
		}
		segments = append(segments, s)
		cur.Reset()
		curRunes = 0
		if inFence {
			cur.WriteString(fenceHeader)
			cur.WriteString("\n")
			curRunes = len([]rune(fenceHeader)) + 1
		}
	}

	trackFence := func(line string) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				inFence = true
				fenceHeader = trimmed
			} else {
				inFence = false
				fenceHeader = "```"
			}
		}
	}

	appendLine := func(line string) {
		lineRunes := len([]rune(line))
		// Hard-wrap single ultra-long line if needed
		if lineRunes > maxRunes-32 {
			rs := []rune(line)
			step := maxRunes - 64
			for len(rs) > 0 {
				if curRunes > 0 {
					flush()
				}
				take := min(len(rs), step)
				cur.WriteString(string(rs[:take]))
				curRunes += take
				rs = rs[take:]
				if len(rs) > 0 {
					flush()
				}
			}
			return
		}
		if curRunes > 0 && curRunes+1+lineRunes > maxRunes {
			flush()
		}
		if curRunes > 0 {
			cur.WriteString("\n")
			curRunes++
		}
		cur.WriteString(line)
		curRunes += lineRunes
		trackFence(line)
	}

	for idx, para := range paragraphs {
		paraRunes := len([]rune(para))
		sep := 0
		if curRunes > 0 {
			sep = 2
		}
		if curRunes > 0 && curRunes+sep+paraRunes <= maxRunes {
			cur.WriteString("\n\n")
			cur.WriteString(para)
			curRunes += 2 + paraRunes
			for _, ln := range strings.Split(para, "\n") {
				trackFence(ln)
			}
			continue
		}
		if curRunes > 0 && paraRunes <= maxRunes {
			flush()
			cur.WriteString(para)
			curRunes += paraRunes
			for _, ln := range strings.Split(para, "\n") {
				trackFence(ln)
			}
			continue
		}
		if idx > 0 && curRunes > 0 {
			cur.WriteString("\n\n")
			curRunes += 2
		}
		for _, ln := range strings.Split(para, "\n") {
			appendLine(ln)
		}
	}
	if curRunes > 0 {
		s := strings.TrimSpace(cur.String())
		if s != "" && s != fenceHeader {
			segments = append(segments, s)
		}
	}
	return segments
}

// SendSessionBridgeReply sends a Bot text message back along the exact return
// path of an Octo session (DM / group / subarea). For group/subarea targets,
// segment 1 ALWAYS carries a native Octo @mention (UID + UTF-16 entity) and
// optionally quotes the latest user message. Long content is cleanly split into
// multiple ordered segments.
func (a *Adapter) SendSessionBridgeReply(
	ctx context.Context,
	tenant uint64,
	targetChannelID string,
	channelType int,
	body string,
	quoteInboxInput string,
	mentionUID string,
	mentionName string,
) (string, error) {
	if a.db == nil || a.tenantID != tenant {
		return "", errors.New("octo channel mismatch")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", errors.New("empty reply body")
	}
	if targetChannelID == "" || (channelType != 1 && channelType != 2 && channelType != 5) {
		return "", errors.New("invalid octo return target")
	}

	var quoteIn *im.IncomingMessage
	if strings.TrimSpace(quoteInboxInput) != "" {
		var parsed im.IncomingMessage
		if json.Unmarshal([]byte(quoteInboxInput), &parsed) == nil && parsed.UserID != "" {
			if mentionName != "" && (parsed.UserName == "" || parsed.UserName == parsed.UserID) {
				parsed.UserName = mentionName
			}
			quoteIn = &parsed
			if mentionUID == "" {
				mentionUID = parsed.UserID
			}
			if mentionName == "" {
				mentionName = parsed.UserName
			}
		}
	}

	segments := SplitOctoMessage(body, octoMaxSegmentRunes)
	if len(segments) == 0 {
		return "", errors.New("empty reply body")
	}

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var firstReceipt string
	for i, seg := range segments {
		if i > 0 {
			select {
			case <-ctx.Done():
				return firstReceipt, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}

		var payload map[string]any
		if i == 0 {
			if quoteIn != nil {
				p, _ := nativeReplyPayload(quoteIn, channelType, seg)
				payload = p
			} else {
				payload = buildBridgeTextPayload(channelType, seg, mentionUID, mentionName)
			}
		} else {
			payload = map[string]any{"type": 1, "content": seg}
		}

		var response struct {
			ID json.RawMessage `json:"message_id"`
		}
		clientNo := "web:" + uuid.NewString()
		if err := a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{
			"channel_id":    targetChannelID,
			"channel_type":  channelType,
			"client_msg_no": clientNo,
			"payload":       payload,
		}, &response); err != nil {
			return firstReceipt, err
		}
		rcpt := wire.ReadID(response.ID)
		if firstReceipt == "" {
			firstReceipt = rcpt
		}
	}
	return firstReceipt, nil
}

func buildBridgeTextPayload(channelType int, text, mentionUID, mentionName string) map[string]any {
	payload := map[string]any{"type": 1}
	if channelType != 1 && strings.TrimSpace(mentionUID) != "" {
		name := strings.TrimSpace(mentionName)
		if name == "" {
			name = mentionUID
		}
		label := "@" + name
		text = label + " " + text
		entity := wire.Entity{UID: mentionUID, Offset: 0, Length: len(utf16.Encode([]rune(label)))}
		payload["mention"] = map[string]any{
			"uids":     []string{mentionUID},
			"entities": []wire.Entity{entity},
		}
	}
	payload["content"] = text
	return payload
}

// SendSessionBridgeFile uploads a file to Octo COS via /v1/bot/upload/presigned
// and sends an Octo file message (type 8) to the target DM/group/subarea.
// If Octo rejects the file extension as unsupported (and it is not a blocked
// executable), it automatically packages the file into a .zip archive and retries.
func (a *Adapter) SendSessionBridgeFile(
	ctx context.Context,
	tenant uint64,
	targetChannelID string,
	channelType int,
	fileName string,
	data []byte,
	contentType string,
	quoteInboxInput string,
) (string, string, error) {
	if a.db == nil || a.tenantID != tenant {
		return "", "", errors.New("octo channel mismatch")
	}
	if targetChannelID == "" || (channelType != 1 && channelType != 2 && channelType != 5) {
		return "", "", errors.New("invalid octo return target")
	}
	if len(data) == 0 || len(data) > maxGeneratedFileBytes {
		return "", "", errors.New("file empty or exceeds 20MB limit")
	}
	cleanName := sanitizeBridgeFilename(fileName)
	ext := strings.ToLower(path.Ext(cleanName))

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	sendName := cleanName
	sendBytes := data
	sendCT := generatedFileContentType(sendName, contentType)

	presign := func(name string, size int, ct string) (*filePresign, error) {
		query := url.Values{
			"filename":    []string{name},
			"fileSize":    []string{strconv.Itoa(size)},
			"contentType": []string{ct},
		}
		var signed filePresign
		if err := a.api.request(ctx, http.MethodGet, "/v1/bot/upload/presigned?"+query.Encode(), nil, &signed); err != nil {
			return nil, err
		}
		return &signed, nil
	}

	signed, err := presign(sendName, len(sendBytes), sendCT)
	if err != nil {
		if errors.Is(err, errUnsupportedFileType) && ext != ".zip" {
			zipName, zipBytes, zErr := zipGeneratedFile(cleanName, data)
			if zErr != nil {
				return "", "", zErr
			}
			sendName = zipName
			sendBytes = zipBytes
			sendCT = "application/zip"
			signed, err = presign(sendName, len(sendBytes), sendCT)
		}
		if err != nil {
			return "", "", err
		}
	}

	if signed.Method != http.MethodPut || !validFileURL(signed.UploadURL, true) || !validFileURL(signed.DownloadURL, false) {
		return "", "", errors.New("invalid Octo presigned upload URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, signed.UploadURL, bytes.NewReader(sendBytes))
	if err != nil {
		return "", "", err
	}
	req.ContentLength = int64(len(sendBytes))
	req.Header.Set("Content-Type", signed.ContentType)
	if signed.ContentDisposition != "" {
		req.Header.Set("Content-Disposition", signed.ContentDisposition)
	}
	storageClient := *a.api.http
	storageClient.Jar = nil
	storageClient.Timeout = 2 * time.Minute
	storageClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := storageClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", errors.New("octo file upload failed with HTTP " + strconv.Itoa(resp.StatusCode))
	}

	payload := map[string]any{
		"type": 8,
		"url":  signed.DownloadURL,
		"name": sendName,
		"size": len(sendBytes),
	}
	if strings.TrimSpace(quoteInboxInput) != "" {
		var in im.IncomingMessage
		if json.Unmarshal([]byte(quoteInboxInput), &in) == nil && wire.DecimalID(in.Extra["octo_message_id"]) && in.UserID != "" {
			var original map[string]json.RawMessage
			if json.Unmarshal([]byte(in.Extra["octo_original_payload"]), &original) == nil && len(original) > 0 {
				payload["reply"] = map[string]any{
					"message_id": in.Extra["octo_message_id"],
					"from_uid":   in.UserID,
					"from_name":  in.UserName,
					"payload":    original,
				}
			}
		}
	}

	var result struct {
		ID json.RawMessage `json:"message_id"`
	}
	if err := a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{
		"channel_id":    targetChannelID,
		"channel_type":  channelType,
		"client_msg_no": "webfile:" + uuid.NewString(),
		"payload":       payload,
	}, &result); err != nil {
		return "", sendName, err
	}
	return wire.ReadID(result.ID), sendName, nil
}

func sanitizeBridgeFilename(name string) string {
	base := strings.TrimSpace(path.Base(strings.ReplaceAll(name, "\\", "/")))
	base = strings.NewReplacer("\r", "_", "\n", "_", "\t", "_", "/", "_", "\\", "_").Replace(base)
	if base == "" || base == "." || base == ".." {
		return "attachment.bin"
	}
	if len([]rune(base)) > 120 {
		ext := path.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		rs := []rune(stem)
		if len(rs) > 100 {
			rs = rs[:100]
		}
		base = string(rs) + ext
	}
	return base
}
