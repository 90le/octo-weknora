package octo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/google/uuid"
)

var octoCOSHost = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-[0-9]+\.cos\.[a-z0-9-]+\.myqcloud\.com$`)

type fileFailure struct {
	code  string
	cause error
}

func (e *fileFailure) Error() string { return "Octo generated file failed: " + e.code }
func (e *fileFailure) Unwrap() error { return e.cause }

// Only stable codes and local identifiers may enter logs. In particular, a
// storage *url.Error can contain the complete signed URL and must not escape.
func (a *Adapter) fileFailure(ctx context.Context, in *im.IncomingMessage, code string, cause error) error {
	messageID := ""
	if in != nil {
		messageID = in.MessageID
	}
	logger.Warnf(ctx, "[IM] Octo generated file failed: code=%s channel=%s message=%s", code, a.channelID, messageID)
	return &fileFailure{code: code, cause: cause}
}

type filePresign struct {
	Method             string `json:"method"`
	UploadURL          string `json:"uploadUrl"`
	DownloadURL        string `json:"downloadUrl"`
	ContentType        string `json:"contentType"`
	ContentDisposition string `json:"contentDisposition"`
	MaxFileSize        int64  `json:"maxFileSize"`
}

func validFileURL(raw string, storage bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if storage {
		return octoCOSHost.MatchString(host)
	}
	return host == "cdn.deepminer.com.cn"
}

// SendGeneratedFile accepts bounded report bytes, never a local filesystem path
// or a caller-selected recipient. Octo's multipart endpoint is retired; use its
// official presigned PUT flow and send the returned CDN reference afterward.
func (a *Adapter) SendGeneratedFile(ctx context.Context, in *im.IncomingMessage, name string, data []byte, contentType string) error {
	return a.sendGeneratedFile(ctx, in, name, data, contentType, func() error { _, err := a.AuthorizeExecution(ctx, nil, in); return err })
}

func (a *Adapter) sendGeneratedFile(ctx context.Context, in *im.IncomingMessage, name string, data []byte, contentType string, authorize func() error) error {
	if in == nil || len(data) == 0 || len(data) > 2<<20 || name == "" || name == "." || name != path.Base(name) || strings.ContainsAny(name, "\\\r\n\x00") {
		return a.fileFailure(ctx, in, "invalid_file", nil)
	}
	if contentType != "text/html; charset=utf-8" && contentType != "text/markdown; charset=utf-8" && contentType != "text/csv; charset=utf-8" && contentType != "text/html" && contentType != "text/markdown" && contentType != "text/csv" {
		return a.fileFailure(ctx, in, "unsupported_type", nil)
	}
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return a.fileFailure(ctx, in, "report_authority_changed", err)
	}
	if err := authorize(); err != nil {
		return a.fileFailure(ctx, in, "scope_denied", err)
	}
	kind, err := strconv.Atoi(in.Extra["octo_channel_type"])
	if err != nil || (kind != 1 && kind != 2 && kind != 5) {
		return a.fileFailure(ctx, in, "invalid_target", nil)
	}
	target := in.Extra["octo_channel_id"]
	if kind == 1 {
		target = in.UserID
	}
	if _, err = wire.ParseScope(target, byte(kind)); err != nil {
		return a.fileFailure(ctx, in, "invalid_target", nil)
	}

	query := url.Values{"filename": []string{name}, "fileSize": []string{strconv.Itoa(len(data))}}
	var signed filePresign
	if err = a.api.request(ctx, http.MethodGet, "/v1/bot/upload/presigned?"+query.Encode(), nil, &signed); err != nil {
		return a.fileFailure(ctx, in, "presign_failed", nil)
	}
	if signed.Method != http.MethodPut || signed.MaxFileSize != int64(len(data)) || !validFileURL(signed.UploadURL, true) || !validFileURL(signed.DownloadURL, false) || signed.ContentType == "" || len(signed.ContentType) > 512 || len(signed.ContentDisposition) > 4096 || strings.ContainsAny(signed.ContentType+signed.ContentDisposition, "\r\n\x00") {
		return a.fileFailure(ctx, in, "presign_invalid", nil)
	}
	// A slow presign request must not allow data to leave after access changes.
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return a.fileFailure(ctx, in, "report_authority_changed", err)
	}
	if err = authorize(); err != nil {
		return a.fileFailure(ctx, in, "scope_denied", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, signed.UploadURL, bytes.NewReader(data))
	if err != nil {
		return a.fileFailure(ctx, in, "upload_request_invalid", nil)
	}
	request.ContentLength = int64(len(data))
	request.Header.Set("Content-Type", signed.ContentType)
	if signed.ContentDisposition != "" {
		request.Header.Set("Content-Disposition", signed.ContentDisposition)
	}
	// The signed URL carries storage authorization. Never copy the Bot bearer or
	// an API cookie jar onto this separate request, and never follow redirects.
	storageClient := *a.api.http
	storageClient.Jar = nil
	storageClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := storageClient.Do(request)
	if err != nil {
		return a.fileFailure(ctx, in, "upload_unavailable", nil)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return a.fileFailure(ctx, in, "upload_http_"+strconv.Itoa(response.StatusCode), nil)
	}
	// Recheck current access and the exact scope used while generating this file
	// before disclosing its CDN reference to the original conversation.
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return a.fileFailure(ctx, in, "report_authority_changed", err)
	}
	if err = authorize(); err != nil {
		return a.fileFailure(ctx, in, "scope_denied", err)
	}
	payload := map[string]any{"type": 8, "url": signed.DownloadURL, "name": name, "size": len(data)}
	if id := in.Extra["octo_message_id"]; wire.DecimalID(id) && in.UserID != "" {
		var original map[string]json.RawMessage
		if json.Unmarshal([]byte(in.Extra["octo_original_payload"]), &original) == nil && len(original) > 0 {
			payload["reply"] = map[string]any{"message_id": id, "from_uid": in.UserID, "from_name": in.UserName, "payload": original}
		}
	}
	var result struct {
		ID json.RawMessage `json:"message_id"`
	}
	err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{"channel_id": target, "channel_type": kind, "client_msg_no": uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.MessageID+":artifact:"+name)).String(), "payload": payload}, &result)
	if err != nil {
		return a.fileFailure(ctx, in, "send_failed", nil)
	}
	if wire.ReadID(result.ID) == "" {
		return a.fileFailure(ctx, in, "send_not_acknowledged", nil)
	}
	return nil
}
