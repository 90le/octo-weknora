package octo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/google/uuid"
)

// SendGeneratedFile accepts only in-memory bounded report bytes. It cannot read
// host paths, publish a document, or select another recipient.
func (a *Adapter) SendGeneratedFile(ctx context.Context, in *im.IncomingMessage, name string, data []byte, contentType string) error {
	return a.sendGeneratedFile(ctx, in, name, data, contentType, func() error { _, err := a.AuthorizeExecution(ctx, nil, in); return err })
}

func (a *Adapter) sendGeneratedFile(ctx context.Context, in *im.IncomingMessage, name string, data []byte, contentType string, authorize func() error) error {
	if in == nil || len(data) == 0 || len(data) > 2<<20 || name != path.Base(name) || strings.ContainsAny(name, "\\\r\n\x00") {
		return errors.New("invalid generated file")
	}
	if contentType != "text/html; charset=utf-8" && contentType != "text/markdown; charset=utf-8" && contentType != "text/csv; charset=utf-8" && contentType != "text/html" && contentType != "text/markdown" && contentType != "text/csv" {
		return errors.New("unsupported generated file type")
	}
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return err
	}
	if err := authorize(); err != nil {
		return err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	if err = writer.WriteField("type", "chat"); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.api.base+"/v1/bot/file/upload", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.api.token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := a.api.http.Do(req)
	if err != nil {
		return errors.New("Octo file upload unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("Octo file upload failed")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return errors.New("invalid file upload response")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return errors.New("invalid file upload response")
	}
	if success, ok := envelope["success"]; ok && string(success) != "true" {
		return errors.New("Octo file upload rejected")
	}
	if d, ok := envelope["data"]; ok {
		raw = d
	}
	var uploaded struct {
		URL  string `json:"url"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if json.Unmarshal(raw, &uploaded) != nil {
		return errors.New("invalid file upload response")
	}
	u, err := url.Parse(uploaded.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() != "cdn.deepminer.com.cn" || (u.Port() != "" && u.Port() != "443") || u.User != nil {
		return errors.New("untrusted uploaded file URL")
	}
	kind, err := strconv.Atoi(in.Extra["octo_channel_type"])
	if err != nil {
		return err
	}
	target := in.Extra["octo_channel_id"]
	if kind == 1 {
		target = in.UserID
	}
	if _, err = wire.ParseScope(target, byte(kind)); err != nil {
		return err
	}
	// Recheck both current access and the exact scope used to generate the file.
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return err
	}
	if err = authorize(); err != nil {
		return err
	}
	var result struct {
		ID json.RawMessage `json:"message_id"`
	}
	err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{"channel_id": target, "channel_type": kind, "client_msg_no": uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.MessageID+":artifact:"+name)).String(), "payload": map[string]any{"type": 8, "url": uploaded.URL, "name": name, "size": len(data)}}, &result)
	if err != nil {
		return err
	}
	if wire.ReadID(result.ID) == "" {
		return errors.New("file delivery not acknowledged")
	}
	return nil
}
