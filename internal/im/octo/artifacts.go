package octo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/google/uuid"
)

var octoCOSHost = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-[0-9]+\.cos\.[a-z0-9-]+\.myqcloud\.com$`)

// Bound one file in line with the sandbox's planned collection limit. The Octo
// server may enforce a lower limit. Callers must keep data unchanged until
// delivery returns; the transport does not duplicate the file.
const maxGeneratedFileBytes = 20 << 20

type fileFailure struct {
	code  string
	cause error
}

func (e *fileFailure) Error() string { return "Octo generated file failed: " + e.code }
func (e *fileFailure) Unwrap() error { return e.cause }

// DeliveryOutcome distinguishes a known pre-send failure from an
// unacknowledged send, where the server may already have accepted the message.
func (e *fileFailure) DeliveryOutcome() string {
	if e.code == "send_failed" || e.code == "send_not_acknowledged" || e.code == "delivery_uncertain" ||
		e.code == "journal_result_unavailable" ||
		e.code == "upload_unavailable" ||
		strings.HasPrefix(e.code, "upload_http_") {
		return "unknown"
	}
	return "failed"
}

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

func validGeneratedFilename(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) ||
		utf8.RuneCountInString(name) > 255 || name != path.Base(name) ||
		strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") ||
		strings.ContainsAny(name, `\/:*?"<>|`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// The server remains authoritative about extensions and size. This is only a
// MIME hint for servers that use it; the signed response is authoritative for
// the PUT. Supply portable Office defaults when the OS MIME database is sparse.
func generatedFileContentType(name, supplied string) string {
	if len(supplied) <= 512 {
		if mediaType, params, err := mime.ParseMediaType(supplied); err == nil &&
			mediaType != "application/octet-stream" {
			if formatted := mime.FormatMediaType(mediaType, params); formatted != "" {
				return formatted
			}
		}
	}
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".doc":
		return "application/msword"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pdf":
		return "application/pdf"
	}
	if inferred := mime.TypeByExtension(ext); inferred != "" {
		return inferred
	}
	return "application/octet-stream"
}

func validSignedFileHeader(value string) bool {
	for i := 0; i < len(value); i++ {
		if (value[i] < 0x20 && value[i] != '\t') || value[i] == 0x7f {
			return false
		}
	}
	return true
}

// SendGeneratedFile accepts bounded attachment bytes, never a local filesystem path
// or a caller-selected recipient. It uses Octo's presigned PUT flow and sends
// the returned CDN reference afterward.
func (a *Adapter) SendGeneratedFile(ctx context.Context, in *im.IncomingMessage, name string, data []byte, contentType string) error {
	return a.sendGeneratedFile(ctx, in, name, data, contentType, func() error { _, err := a.AuthorizeExecution(ctx, nil, in); return err })
}

func (a *Adapter) sendGeneratedFile(
	ctx context.Context,
	in *im.IncomingMessage,
	name string,
	data []byte,
	contentType string,
	authorize func() error,
) (resultErr error) {
	if in == nil || len(data) == 0 || len(data) > maxGeneratedFileBytes || !validGeneratedFilename(name) {
		return a.fileFailure(ctx, in, "invalid_file", nil)
	}
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return a.fileFailure(ctx, in, "report_authority_changed", err)
	}
	if err := octobusiness.ValidateFileDeliveryAuthority(ctx); err != nil {
		return a.fileFailure(ctx, in, "file_authority_changed", err)
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

	contentHash := sha256.Sum256(data)
	identity, _ := json.Marshal(
		[]string{
			strconv.FormatUint(a.tenantID, 10),
			a.channelID,
			strconv.Itoa(kind),
			target,
			in.MessageID,
			name,
			hex.EncodeToString(contentHash[:]),
		},
	)
	clientMessageID := uuid.NewSHA1(uuid.NameSpaceOID, identity).String()
	fileKeyHash := sha256.Sum256(identity)
	fileKey := hex.EncodeToString(fileKeyHash[:])
	turn := fileTurn{tenant: a.tenantID, channel: a.channelID, source: in.MessageID}
	journal := a.fileDeliveryStore()
	if journal == nil {
		return a.fileFailure(ctx, in, "journal_unavailable", nil)
	}
	owner, _ := ctx.Value(fileDeliveryOwnerKey{}).(string)
	if err := journal.start(ctx, turn, owner); err != nil {
		if errors.Is(err, errFileDeliveryUncertain) || errors.Is(err, errFileDeliveryFenced) {
			return a.fileFailure(ctx, in, "delivery_uncertain", nil)
		}
		return a.fileFailure(ctx, in, "journal_unavailable", nil)
	}
	entry := fileDeliveryEntry{
		FileKey:     fileKey,
		FileName:    name,
		ContentHash: hex.EncodeToString(contentHash[:]),
		ClientMsgNo: clientMessageID,
	}
	alreadySent, err := journal.reserve(ctx, turn, owner, entry)
	if err != nil {
		if errors.Is(err, errFileDeliveryUncertain) || errors.Is(err, errFileDeliveryFenced) {
			return a.fileFailure(ctx, in, "delivery_uncertain", nil)
		}
		return a.fileFailure(ctx, in, "journal_unavailable", nil)
	}
	if alreadySent {
		return nil
	}
	// A detached, bounded write preserves the outcome even if the request's
	// context expired. A failed write leaves the conservative prior state.
	finish := func(state, platformID, code string) error {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return journal.finish(writeCtx, turn, owner, fileKey, state, platformID, code)
	}
	phase := "preparing"
	defer func() {
		if resultErr != nil && (phase == "preparing" || phase == "uploading") {
			code := "before_send_failed"
			var failure *fileFailure
			if errors.As(resultErr, &failure) {
				code = failure.code
			}
			state := "failed"
			if phase == "uploading" {
				state = "unknown"
			}
			_ = finish(state, "", code)
		}
	}()

	query := url.Values{
		"filename":    []string{name},
		"fileSize":    []string{strconv.Itoa(len(data))},
		"contentType": []string{generatedFileContentType(name, contentType)},
	}
	var signed filePresign
	if err = a.api.request(ctx, http.MethodGet, "/v1/bot/upload/presigned?"+query.Encode(), nil, &signed); err != nil {
		return a.fileFailure(ctx, in, "presign_failed", nil)
	}
	if signed.Method != http.MethodPut || signed.MaxFileSize != int64(len(data)) ||
		!validFileURL(signed.UploadURL, true) ||
		!validFileURL(signed.DownloadURL, false) ||
		signed.ContentType == "" ||
		len(signed.ContentType) > 512 ||
		len(signed.ContentDisposition) > 4096 ||
		!validSignedFileHeader(signed.ContentType) ||
		!validSignedFileHeader(signed.ContentDisposition) {
		return a.fileFailure(ctx, in, "presign_invalid", nil)
	}
	// A slow presign request must not allow data to leave after access changes.
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return a.fileFailure(ctx, in, "report_authority_changed", err)
	}
	if err := octobusiness.ValidateFileDeliveryAuthority(ctx); err != nil {
		return a.fileFailure(ctx, in, "file_authority_changed", err)
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
	storageClient.Timeout = 5 * time.Minute
	storageClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Once a PUT begins, even a timeout can leave bytes in external storage.
	if err = journal.markUploading(ctx, turn, owner, fileKey); err != nil {
		if errors.Is(err, errFileDeliveryFenced) {
			return a.fileFailure(ctx, in, "delivery_uncertain", nil)
		}
		return a.fileFailure(ctx, in, "journal_unavailable", nil)
	}
	phase = "uploading"
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
	if err := octobusiness.ValidateFileDeliveryAuthority(ctx); err != nil {
		return a.fileFailure(ctx, in, "file_authority_changed", err)
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
	// Commit the uncertainty boundary before the external message can be sent.
	if err = journal.markSending(ctx, turn, owner, fileKey); err != nil {
		if errors.Is(err, errFileDeliveryFenced) {
			return a.fileFailure(ctx, in, "delivery_uncertain", nil)
		}
		return a.fileFailure(ctx, in, "journal_unavailable", nil)
	}
	phase = "sending"
	abortBeforeSend := func(code string, cause error) error {
		if err := finish("not_sent", "", code); err != nil {
			return a.fileFailure(ctx, in, "journal_result_unavailable", nil)
		}
		return a.fileFailure(ctx, in, code, cause)
	}
	// The journal write can be slow; recheck authorization after it, too.
	if err := octobusiness.ValidateReportAuthority(ctx); err != nil {
		return abortBeforeSend("report_authority_changed", err)
	}
	if err := octobusiness.ValidateFileDeliveryAuthority(ctx); err != nil {
		return abortBeforeSend("file_authority_changed", err)
	}
	if err = authorize(); err != nil {
		return abortBeforeSend("scope_denied", err)
	}
	err = a.api.post(
		ctx,
		"/v1/bot/sendMessage",
		map[string]any{
			"channel_id":    target,
			"channel_type":  kind,
			"client_msg_no": clientMessageID,
			"payload":       payload,
		},
		&result,
	)
	if err != nil {
		_ = finish("unknown", "", "send_failed")
		return a.fileFailure(ctx, in, "send_failed", nil)
	}
	platformID := wire.ReadID(result.ID)
	if platformID == "" {
		_ = finish("unknown", "", "send_not_acknowledged")
		return a.fileFailure(ctx, in, "send_not_acknowledged", nil)
	}
	if err = finish("sent", platformID, ""); err != nil {
		return a.fileFailure(ctx, in, "journal_result_unavailable", nil)
	}
	return nil
}
