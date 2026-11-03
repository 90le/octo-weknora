package octo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octobusiness"
)

type journalNetwork struct {
	mu                                      sync.Mutex
	presigns, uploads, sends                int
	clients                                 []string
	presignStatus, uploadStatus, sendStatus int
	ack                                     bool
}

func (n *journalNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		n.presigns++
		if n.presignStatus != 0 {
			return fileResponse(n.presignStatus, nil), nil
		}
		size, _ := strconv.Atoi(r.URL.Query().Get("fileSize"))
		return fileResponse(200, filePresignFixture(size)), nil
	case http.MethodPut:
		n.uploads++
		if n.uploadStatus != 0 {
			return fileResponse(n.uploadStatus, nil), nil
		}
		return fileResponse(200, nil), nil
	case http.MethodPost:
		n.sends++
		var payload struct {
			Client string `json:"client_msg_no"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Client == "" {
			return nil, errors.New("invalid mock send")
		}
		n.clients = append(n.clients, payload.Client)
		if n.sendStatus != 0 {
			return fileResponse(n.sendStatus, nil), nil
		}
		if !n.ack {
			return fileResponse(200, map[string]string{}), nil
		}
		return fileResponse(200, map[string]string{"message_id": "123"}), nil
	default:
		return nil, errors.New("unexpected mock request")
	}
}

func journalFixture(t *testing.T) (*Adapter, *im.IncomingMessage, context.Context, *journalNetwork) {
	t.Helper()
	a, msg := durableAdapter(t)
	msg.Extra[fileDeliveryGuardKey] = "1" // already admitted by the new dispatcher
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.db.Create(&Inbox{TenantID: a.tenantID, ChannelID: a.channelID, MessageID: msg.MessageID,
		Input: string(raw), Authority: encodeAuthority(&im.ExecutionScope{KnowledgeBaseIDs: []string{"kb"}}), State: "processing"}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, err := a.startFileDeliveryTurn(context.Background(), msg.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	network := &journalNetwork{ack: true}
	a.api.http.Transport = network
	return a, msg, ctx, network
}

func fileRecords(t *testing.T, a *Adapter, source string) []fileDeliveryEntry {
	t.Helper()
	var rows []fileDeliveryEntry
	if err := a.db.Where("tenant_id = ? AND channel_id = ? AND source_message_id = ? AND file_key <> ?",
		a.tenantID, a.channelID, source, fileDeliveryTurnKey).Order("created_at, file_key").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestFileDeliveryRequiresDurableJournalInRuntime(t *testing.T) {
	a, msg := generatedFileFixture(t)
	a.fileJournal = nil // NewAdapter with no database is a real fail-closed path.
	requests := 0
	a.api.http.Transport = fileRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		return fileResponse(500, nil), nil
	})
	err := a.SendGeneratedFile(context.Background(), msg, "report.pdf", []byte("data"), "application/pdf")
	var failure *fileFailure
	if !errors.As(err, &failure) || failure.code != "journal_unavailable" || requests != 0 {
		t.Fatalf("no database did not fail closed: err=%v requests=%d", err, requests)
	}
}

func TestFileDeliveryPersistsMultipleFilesAndDoesNotResendSameBytes(t *testing.T) {
	a, msg, ctx, network := journalFixture(t)
	for _, file := range []struct {
		name string
		data []byte
	}{
		{"same.xlsx", []byte{0, 1, 2, 3}},
		{"same.xlsx", []byte{0, 1, 2, 3}}, // exact retry is served from the journal
		{"same.xlsx", []byte{0, 1, 2, 4}}, // distinct bytes in the same execution
		{"other.pdf", []byte{0, 1, 2, 3}},
	} {
		if err := a.SendGeneratedFile(ctx, msg, file.name, file.data, "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}
	rows := fileRecords(t, a, msg.MessageID)
	if len(rows) != 3 || network.presigns != 3 || network.uploads != 3 || network.sends != 3 ||
		len(network.clients) != 3 || network.clients[0] == network.clients[1] || network.clients[0] == network.clients[2] {
		t.Fatalf("unexpected multi-file sends: rows=%d network=%+v", len(rows), network)
	}
	for _, row := range rows {
		if row.State != "sent" || row.PlatformMessageID != "123" || row.ClientMsgNo == "" || row.ContentHash == "" {
			t.Fatalf("delivery result not persisted: %+v", row)
		}
	}
}

func TestFileDeliveryIdentityIsIsolatedByTenantAndChannel(t *testing.T) {
	a, msg, ctx, network := journalFixture(t)
	if err := a.SendGeneratedFile(ctx, msg, "same.pdf", []byte("same"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	other := restartAdapterForTest(t, a)
	other.tenantID, other.channelID = 2, "other-channel"
	otherCtx, err := other.startFileDeliveryTurn(context.Background(), msg.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.SendGeneratedFile(otherCtx, msg, "same.pdf", []byte("same"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	if network.sends != 2 || len(network.clients) != 2 || network.clients[0] == network.clients[1] {
		t.Fatalf("cross-tenant delivery identities collided: sends=%d ids=%v", network.sends, network.clients)
	}
}

func TestConcurrentFileAttemptsSendAtMostOnce(t *testing.T) {
	a, msg, ctx, network := journalFixture(t)
	sqlDB, err := a.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Serialize SQLite transactions, while callers still race to claim the
	// same file. Production PostgreSQL uses the turn row lock instead.
	sqlDB.SetMaxOpenConns(1)
	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			results <- a.SendGeneratedFile(ctx, msg, "report.pdf", []byte("same"), "application/pdf")
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		err := <-results
		var failure *fileFailure
		if err != nil && (!errors.As(err, &failure) || failure.DeliveryOutcome() != "unknown") {
			t.Fatalf("unexpected concurrent outcome: %v", err)
		}
	}
	if network.sends != 1 || len(fileRecords(t, a, msg.MessageID)) != 1 {
		t.Fatalf("concurrent requests duplicated a file: sends=%d", network.sends)
	}
}

// Panics model a process disappearing after a committed journal boundary;
// recovery uses a fresh adapter and the same isolated on-disk SQLite database.
type crashFileJournal struct {
	fileDeliveryJournal
	stage string
}

func (j crashFileJournal) reserve(ctx context.Context, turn fileTurn, owner string, entry fileDeliveryEntry) (bool, error) {
	sent, err := j.fileDeliveryJournal.reserve(ctx, turn, owner, entry)
	if err == nil && j.stage == "after_reserve" {
		panic("simulated crash after reserve")
	}
	return sent, err
}

func (j crashFileJournal) markUploading(ctx context.Context, turn fileTurn, owner, key string) error {
	err := j.fileDeliveryJournal.markUploading(ctx, turn, owner, key)
	if err == nil && j.stage == "after_upload_marker" {
		panic("simulated crash before upload")
	}
	return err
}

func (j crashFileJournal) markSending(ctx context.Context, turn fileTurn, owner, key string) error {
	if j.stage == "before_send_marker" {
		panic("simulated crash after upload")
	}
	err := j.fileDeliveryJournal.markSending(ctx, turn, owner, key)
	if err == nil && j.stage == "after_send_marker" {
		panic("simulated crash before send")
	}
	return err
}

func (j crashFileJournal) finish(ctx context.Context, turn fileTurn, owner, key, state, platformID, code string) error {
	if state == "sent" && j.stage == "before_result" {
		panic("simulated crash after send")
	}
	if state == "sent" && j.stage == "result_write_failure" {
		return errors.New("simulated database failure")
	}
	err := j.fileDeliveryJournal.finish(ctx, turn, owner, key, state, platformID, code)
	if err == nil && state == "sent" && j.stage == "after_result" {
		panic("simulated crash after result")
	}
	return err
}

func invokeSimulatedCrash(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected simulated crash")
		}
	}()
	fn()
}

func TestFileDeliveryCrashWindowsAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		stage       string
		wantState   string
		allowReplay bool
		posts       int
	}{
		{"after_reserve", "preparing", true, 0},
		{"after_upload_marker", "uploading", false, 0},
		{"before_send_marker", "uploading", false, 0},
		{"after_send_marker", "sending", false, 0},
		{"before_result", "sending", false, 1},
		{"after_result", "sent", false, 1},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			a, msg, ctx, network := journalFixture(t)
			a.fileJournal = crashFileJournal{fileDeliveryJournal: &dbFileDeliveryJournal{db: a.db}, stage: tc.stage}
			invokeSimulatedCrash(t, func() { _ = a.SendGeneratedFile(ctx, msg, "report.xlsx", []byte{0, 1, 2, 3}, "") })
			rows := fileRecords(t, a, msg.MessageID)
			if len(rows) != 1 || rows[0].State != tc.wantState || network.sends != tc.posts {
				t.Fatalf("incorrect crash boundary: rows=%+v sends=%d", rows, network.sends)
			}
			restarted := restartAdapterForTest(t, a)
			calls := 0
			restarted.recoverInbox(context.Background(), func(c context.Context, recovered *im.IncomingMessage) error {
				calls++
				// A rerun may produce different bytes, which must not defeat the
				// pre-model recovery barrier once any send became uncertain.
				return restarted.SendGeneratedFile(c, recovered, "report.xlsx", []byte{0, 9, 8, 7}, "")
			})
			if tc.allowReplay {
				if calls != 1 || network.sends != 1 {
					t.Fatalf("safe pre-send recovery failed: calls=%d sends=%d", calls, network.sends)
				}
			} else if calls != 0 || network.sends != tc.posts || inboxRow(t, a, msg.MessageID).ErrorCode != "file_delivery_recovery_blocked" {
				t.Fatalf("unsafe replay ran: calls=%d sends=%d inbox=%+v", calls, network.sends, inboxRow(t, a, msg.MessageID))
			}
		})
	}
}

func TestFileDeliveryPartialMultiFileCrashDoesNotRegenerate(t *testing.T) {
	a, msg, ctx, network := journalFixture(t)
	if err := a.SendGeneratedFile(ctx, msg, "first.pdf", []byte("first"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	a.fileJournal = crashFileJournal{fileDeliveryJournal: &dbFileDeliveryJournal{db: a.db}, stage: "before_send_marker"}
	invokeSimulatedCrash(t, func() { _ = a.SendGeneratedFile(ctx, msg, "second.pdf", []byte("second"), "application/pdf") })
	rows := fileRecords(t, a, msg.MessageID)
	states := map[string]string{}
	for _, row := range rows {
		states[row.FileName] = row.State
	}
	if states["first.pdf"] != "sent" || states["second.pdf"] != "uploading" || network.sends != 1 {
		t.Fatalf("partial delivery state lost: states=%v sends=%d", states, network.sends)
	}
	restarted := restartAdapterForTest(t, a)
	restarted.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error {
		t.Fatal("partial multi-file delivery regenerated after restart")
		return nil
	})
	if network.sends != 1 || inboxRow(t, a, msg.MessageID).ErrorCode != "file_delivery_recovery_blocked" {
		t.Fatal("partial multi-file recovery produced another send")
	}
}

func TestFileDeliveryKnownFailureCanRecoverButUnknownCannot(t *testing.T) {
	for _, tc := range []struct {
		name, wantState string
		presign, send   int
		ack             bool
		replay          bool
	}{
		{"presign rejected", "failed", 400, 0, true, true},
		{"send HTTP error", "unknown", 0, 503, true, false},
		{"missing acknowledgement", "unknown", 0, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, msg, ctx, network := journalFixture(t)
			network.presignStatus, network.sendStatus, network.ack = tc.presign, tc.send, tc.ack
			err := a.SendGeneratedFile(ctx, msg, "report.pdf", []byte("data"), "application/pdf")
			var failure *fileFailure
			if !errors.As(err, &failure) || (failure.DeliveryOutcome() == "failed") != tc.replay {
				t.Fatalf("wrong classified failure: %v", err)
			}
			rows := fileRecords(t, a, msg.MessageID)
			if len(rows) != 1 || rows[0].State != tc.wantState {
				t.Fatalf("wrong durable result: %+v", rows)
			}
			if !tc.replay {
				beforePresigns := network.presigns
				if err := a.SendGeneratedFile(ctx, msg, "report.pdf", []byte("new!"), "application/pdf"); err == nil || network.presigns != beforePresigns {
					t.Fatal("changed bytes bypassed an ambiguous send in the same execution")
				}
			}
			network.presignStatus, network.sendStatus, network.ack = 0, 0, true
			before := network.sends
			restarted := restartAdapterForTest(t, a)
			calls := 0
			restarted.recoverInbox(context.Background(), func(c context.Context, m *im.IncomingMessage) error {
				calls++
				return restarted.SendGeneratedFile(c, m, "report.pdf", []byte("new!"), "application/pdf")
			})
			if tc.replay && (calls != 1 || network.sends != before+1) {
				t.Fatalf("known failure did not recover: calls=%d sends=%d", calls, network.sends)
			}
			if !tc.replay && (calls != 0 || network.sends != before) {
				t.Fatalf("unknown outcome was replayed: calls=%d sends=%d", calls, network.sends)
			}
		})
	}
}

func TestFileDeliveryUploadFailureIsUnknownAndNotReplayed(t *testing.T) {
	a, msg, ctx, network := journalFixture(t)
	network.uploadStatus = http.StatusServiceUnavailable
	err := a.SendGeneratedFile(ctx, msg, "report.pdf", []byte("data"), "application/pdf")
	var failure *fileFailure
	rows := fileRecords(t, a, msg.MessageID)
	if !errors.As(err, &failure) || failure.DeliveryOutcome() != "unknown" || network.uploads != 1 || network.sends != 0 || len(rows) != 1 || rows[0].State != "unknown" {
		t.Fatalf("upload uncertainty not recorded: err=%v uploads=%d sends=%d rows=%+v", err, network.uploads, network.sends, rows)
	}
	restarted := restartAdapterForTest(t, a)
	restarted.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error {
		t.Fatal("uncertain upload was regenerated")
		return nil
	})
}

func TestFileDeliveryRevocationAfterUploadIsNotSentOrReplayed(t *testing.T) {
	a, msg, ctx, network := journalFixture(t)
	checks := 0
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		checks++
		if checks == 4 {
			return nil, im.ErrScopeDenied
		}
		return &im.ExecutionScope{KnowledgeBaseIDs: []string{"kb"}}, nil
	}
	err := a.SendGeneratedFile(ctx, msg, "report.pdf", []byte("data"), "application/pdf")
	rows := fileRecords(t, a, msg.MessageID)
	if !errors.Is(err, im.ErrScopeDenied) || network.sends != 0 || len(rows) != 1 || rows[0].State != "not_sent" {
		t.Fatalf("pre-send revocation was not a known failure: err=%v sends=%d rows=%+v", err, network.sends, rows)
	}
	restarted := restartAdapterForTest(t, a)
	restarted.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error {
		t.Fatal("already uploaded bytes were regenerated after revocation")
		return nil
	})
}

func TestFileDeliveryResultWriteFailureIsUnknownAndNotReplayed(t *testing.T) {
	a, msg, ctx, network := journalFixture(t)
	a.fileJournal = crashFileJournal{fileDeliveryJournal: &dbFileDeliveryJournal{db: a.db}, stage: "result_write_failure"}
	err := a.SendGeneratedFile(ctx, msg, "report.pdf", []byte("data"), "application/pdf")
	var failure *fileFailure
	if !errors.As(err, &failure) || failure.DeliveryOutcome() != "unknown" || network.sends != 1 {
		t.Fatalf("unrecorded acknowledgement reported as safe: %v", err)
	}
	rows := fileRecords(t, a, msg.MessageID)
	if len(rows) != 1 || rows[0].State != "sending" {
		t.Fatalf("uncertain marker lost: %+v", rows)
	}
	restarted := restartAdapterForTest(t, a)
	restarted.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error {
		t.Fatal("recovery reran after an unrecorded send")
		return nil
	})
}

func TestFileDeliveryRecoveryBlocksLegacyUntrackedProcessing(t *testing.T) {
	a, msg := durableAdapter(t)
	raw, _ := json.Marshal(msg)
	if err := a.db.Create(&Inbox{TenantID: a.tenantID, ChannelID: a.channelID, MessageID: msg.MessageID, Input: string(raw), State: "processing"}).Error; err != nil {
		t.Fatal(err)
	}
	a.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error {
		t.Fatal("untracked legacy processing reran")
		return nil
	})
	if row := inboxRow(t, a, msg.MessageID); row.State != "failed" || row.ErrorCode != "file_delivery_history_unavailable" {
		t.Fatalf("legacy ambiguity was not recorded: %+v", row)
	}
}

type takeoverFileJournal struct {
	fileDeliveryJournal
	takeover func()
}

func (j takeoverFileJournal) markUploading(ctx context.Context, turn fileTurn, owner, key string) error {
	j.takeover()
	return j.fileDeliveryJournal.markUploading(ctx, turn, owner, key)
}

func TestFileDeliveryFencesAnOlderExecution(t *testing.T) {
	a, msg, oldCtx, network := journalFixture(t)
	var newCtx context.Context
	a.fileJournal = takeoverFileJournal{fileDeliveryJournal: &dbFileDeliveryJournal{db: a.db}, takeover: func() {
		var err error
		newCtx, err = a.startFileDeliveryTurn(context.Background(), msg.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		owner, _ := newCtx.Value(fileDeliveryOwnerKey{}).(string)
		turn := fileTurn{tenant: a.tenantID, channel: a.channelID, source: msg.MessageID}
		if err := (&dbFileDeliveryJournal{db: a.db}).start(newCtx, turn, owner); err != nil {
			t.Fatal(err)
		}
	}}
	err := a.SendGeneratedFile(oldCtx, msg, "old.pdf", []byte("old"), "application/pdf")
	if err == nil || network.uploads != 0 || network.sends != 0 || newCtx == nil {
		t.Fatalf("superseded execution reached send: err=%v sends=%d", err, network.sends)
	}
	// Avoid stealing the execution again when the new request uploads.
	a.fileJournal = nil
	if err = a.SendGeneratedFile(newCtx, msg, "new.pdf", []byte("new"), "application/pdf"); err != nil || network.sends != 1 {
		t.Fatalf("current execution could not send: err=%v sends=%d", err, network.sends)
	}
}

func TestFileDeliveryLegacyTextAndMissingJournalRecovery(t *testing.T) {
	a, msg := durableAdapter(t)
	raw, _ := json.Marshal(msg)
	if err := a.db.Create(&Inbox{TenantID: a.tenantID, ChannelID: a.channelID, MessageID: msg.MessageID, Input: string(raw), State: "queued"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.db.Exec("DROP TABLE octo_file_deliveries").Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error { calls++; return nil })
	if calls != 1 {
		t.Fatal("queued ordinary text was blocked by a missing file journal")
	}
	a.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error {
		t.Fatal("untracked processing resumed while the journal was missing")
		return nil
	})
	if row := inboxRow(t, a, msg.MessageID); row.State != "processing" {
		t.Fatalf("missing journal should leave recovery pending: %s", fmt.Sprint(row.State))
	}
}

func TestTextOnlyTurnDoesNotCreateFileJournalAndCanRecover(t *testing.T) {
	a, msg := durableAdapter(t)
	calls := 0
	if err := a.accept(context.Background(), msg, func(context.Context, *im.IncomingMessage) error {
		calls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Progress persistence rewrites the input; the guard must survive it.
	card := &progressCard{adapter: a, message: msg}
	if err := card.saveProgress(context.Background(), "123", false); err != nil {
		t.Fatal(err)
	}
	var stored im.IncomingMessage
	if err := json.Unmarshal([]byte(inboxRow(t, a, msg.MessageID).Input), &stored); err != nil ||
		stored.Extra[fileDeliveryGuardKey] != "1" || stored.Content != msg.Content || stored.Extra["octo_progress_message_id"] != "123" {
		t.Fatalf("inbox input or recovery guard changed: %+v, %v", stored, err)
	}
	a.recoverInbox(context.Background(), func(context.Context, *im.IncomingMessage) error {
		calls++
		return nil
	})
	var count int64
	if err := a.db.Model(&fileDeliveryEntry{}).Count(&count).Error; err != nil || count != 0 || calls != 2 {
		t.Fatalf("text-only recovery created file ledger rows: calls=%d rows=%d err=%v", calls, count, err)
	}
}

func TestScheduledReportFileUsesDurableJournal(t *testing.T) {
	a, schedule := scheduledAdapter(t)
	if err := a.db.AutoMigrate(&fileDeliveryEntry{}); err != nil {
		t.Fatal(err)
	}
	network := &journalNetwork{ack: true}
	a.api.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/bot/groups/group/threads/2098355867442221056" {
			return fileResponse(200, map[string]any{"group_no": "group", "short_id": "2098355867442221056", "status": 1}), nil
		}
		return network.RoundTrip(r)
	})
	file := &octobusiness.ReportArtifact{Name: "report.csv", Content: "data", ContentType: "text/csv"}
	if err := a.DeliverReport(context.Background(), schedule, &octobusiness.Report{}, file); err != nil {
		t.Fatal(err)
	}
	file.Content = "changed"
	err := a.DeliverReport(context.Background(), schedule, &octobusiness.Report{}, file)
	var failure *fileFailure
	if !errors.As(err, &failure) || failure.DeliveryOutcome() != "unknown" || network.sends != 1 {
		t.Fatalf("scheduled report repeated for same run: err=%v sends=%d", err, network.sends)
	}
}
