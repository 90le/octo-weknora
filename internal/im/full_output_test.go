package im

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type fullOutputOrder struct {
	mu    sync.Mutex
	steps []string
}

func (o *fullOutputOrder) add(step string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = append(o.steps, step)
}

func (o *fullOutputOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.steps...)
}

type fullOutputSessionService struct {
	interfaces.SessionService
	order           *fullOutputOrder
	answer          string
	fallback        bool
	hangUntilCancel bool
	started         chan struct{}
}

func (s *fullOutputSessionService) KnowledgeQA(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
	s.order.add("qa")
	if s.started != nil {
		close(s.started)
	}
	if s.hangUntilCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	return bus.Emit(ctx, event.Event{
		ID:        "answer-1",
		Type:      event.EventAgentFinalAnswer,
		SessionID: req.Session.ID,
		Data: event.AgentFinalAnswerData{
			Content:    s.answer,
			Done:       true,
			IsFallback: s.fallback,
		},
	})
}

type fullOutputMessageService struct {
	interfaces.MessageService
	mu      sync.Mutex
	updated *types.Message
}

func (s *fullOutputMessageService) CreateMessage(_ context.Context, msg *types.Message) (*types.Message, error) {
	created := *msg
	created.ID = created.Role + "-message"
	return &created, nil
}

func (s *fullOutputMessageService) UpdateMessage(_ context.Context, msg *types.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *msg
	s.updated = &copy
	return nil
}

func (s *fullOutputMessageService) latestUpdate() *types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updated == nil {
		return nil
	}
	copy := *s.updated
	return &copy
}

type fullOutputStreamManager struct {
	interfaces.StreamManager
}

func (s *fullOutputStreamManager) AppendEvent(_ context.Context, _, _ string, _ interfaces.StreamEvent) error {
	return nil
}

func (s *fullOutputStreamManager) GetEvents(
	_ context.Context, _, _ string, from int,
) ([]interfaces.StreamEvent, int, error) {
	return nil, from, nil
}

type fullOutputAdapter struct {
	Adapter
	order        *fullOutputOrder
	finalContent string
	updates      int
	plainReplies int
	plainContent string
	startErr     error
	finalizeErr  error
	sendErr      error
}

type processingFullOutputAdapter struct {
	*fullOutputAdapter
}

func (a *processingFullOutputAdapter) StartProcessing(_ context.Context, _ *IncomingMessage) func() {
	a.order.add("processing-start")
	return func() { a.order.add("processing-stop") }
}

type revokedProcessingAdapter struct {
	*processingFullOutputAdapter
}

func (a *revokedProcessingAdapter) AuthorizeExecution(context.Context, *IMChannel, *IncomingMessage) (*ExecutionScope, error) {
	return nil, ErrScopeDenied
}

type nilStopProcessingAdapter struct {
	*fullOutputAdapter
}

func (a *nilStopProcessingAdapter) StartProcessing(context.Context, *IncomingMessage) func() {
	return nil
}

func (a *fullOutputAdapter) SendReply(ctx context.Context, _ *IncomingMessage, reply *ReplyMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.sendErr != nil {
		a.order.add("plain-reply")
		return a.sendErr
	}
	a.plainReplies++
	if reply != nil {
		a.plainContent = reply.Content
	}
	a.order.add("plain-reply")
	return nil
}

func (a *fullOutputAdapter) StartStream(ctx context.Context, _ *IncomingMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.order.add("start")
	if a.startErr != nil {
		return "", a.startErr
	}
	return "full-output-stream", nil
}

func (a *fullOutputAdapter) SupportsFullOutputProgress() bool {
	return true
}

func (a *fullOutputAdapter) UpdateStreamContent(ctx context.Context, _ *IncomingMessage, _ string, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.updates++
	a.order.add("update")
	return nil
}

func (a *fullOutputAdapter) FinalizeStream(ctx context.Context, _ *IncomingMessage, _ string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.finalContent = content
	a.order.add("finalize")
	return a.finalizeErr
}

func (a *fullOutputAdapter) EndStream(ctx context.Context, _ *IncomingMessage, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.order.add("end")
	return nil
}

type fullOutputNoProgressAdapter struct {
	*fullOutputAdapter
}

func (a *fullOutputNoProgressAdapter) SupportsFullOutputProgress() bool {
	return false
}

func newFullOutputHarness(answer string) (*Service, *fullOutputAdapter, *fullOutputSessionService, *fullOutputOrder) {
	order := &fullOutputOrder{}
	adapter := &fullOutputAdapter{order: order}
	sessionSvc := &fullOutputSessionService{order: order, answer: answer}
	service := &Service{
		sessionService: sessionSvc,
		messageService: &fullOutputMessageService{},
		streamManager:  &fullOutputStreamManager{},
	}
	return service, adapter, sessionSvc, order
}

func TestIMAnswerFallbackFlagSurvivesStreamAndNonStreamPersistence(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, fallback := range []bool{false, true} {
			name := "nonstream-normal"
			if streaming {
				name = "stream-normal"
			}
			if fallback {
				name += "-fallback"
			}
			t.Run(name, func(t *testing.T) {
				service, adapter, sessionSvc, _ := newFullOutputHarness("当前资料不足")
				sessionSvc.fallback = fallback
				ctx := context.Background()
				session := &types.Session{ID: "fallback-session"}
				var err error
				if streaming {
					msg := &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"}
					err = service.handleMessageStream(ctx, msg, session, nil, nil, nil, nil, adapter, adapter, "fallback-user", nil)
				} else {
					_, err = service.runQA(ctx, session, "问题", nil, nil, nil, nil, "fallback-user", nil)
				}
				if err != nil {
					t.Fatalf("QA error: %v", err)
				}
				stored := service.messageService.(*fullOutputMessageService).latestUpdate()
				if stored == nil || !stored.IsCompleted || stored.IsFallback != fallback || stored.Content != "当前资料不足" {
					t.Fatalf("stored answer = %+v, want completed fallback=%v", stored, fallback)
				}
				if streaming && adapter.finalContent != "当前资料不足" {
					t.Fatalf("stream final content = %q", adapter.finalContent)
				}
			})
		}
	}
}

func TestHandleMessageFullOutputShowsPlaceholderWithoutIntermediateUpdates(t *testing.T) {
	service, adapter, _, order := newFullOutputHarness("最终答案")
	msg := &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"}
	session := &types.Session{ID: "session-1"}

	err := service.handleMessageFullOutput(
		context.Background(), msg, session, nil, nil, nil, nil, adapter, adapter, "user-key", nil,
	)
	if err != nil {
		t.Fatalf("handleMessageFullOutput() error = %v", err)
	}

	wantOrder := []string{"start", "qa", "finalize", "end"}
	if got := order.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("lifecycle order = %v, want %v", got, wantOrder)
	}
	if adapter.updates != 0 {
		t.Fatalf("full output sent %d intermediate updates, want 0", adapter.updates)
	}
	if adapter.finalContent != "最终答案" {
		t.Fatalf("final content = %q, want %q", adapter.finalContent, "最终答案")
	}
	if adapter.plainReplies != 0 {
		t.Fatalf("plain fallback replies = %d, want 0", adapter.plainReplies)
	}
}

func TestHandleMessageFullOutputReplacesPlaceholderAfterCancel(t *testing.T) {
	service, adapter, sessionSvc, order := newFullOutputHarness("")
	sessionSvc.hangUntilCancel = true
	sessionSvc.started = make(chan struct{})
	msg := &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"}
	session := &types.Session{ID: "session-1"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.handleMessageFullOutput(
			ctx, msg, session, nil, nil, nil, nil, adapter, adapter, "user-key", nil,
		)
	}()

	select {
	case <-sessionSvc.started:
	case <-time.After(2 * time.Second):
		t.Fatal("QA did not start")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleMessageFullOutput() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for full-output after cancel")
	}

	wantOrder := []string{"start", "qa", "finalize", "end"}
	if got := order.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("lifecycle order = %v, want %v", got, wantOrder)
	}
	if adapter.finalContent != imCancelledFallback {
		t.Fatalf("final content = %q, want cancel fallback %q", adapter.finalContent, imCancelledFallback)
	}
	if adapter.plainReplies != 0 {
		t.Fatalf("plain fallback replies = %d, want 0", adapter.plainReplies)
	}
}

func TestHandleMessageFullOutputStartStreamErrorFallsBackToPlainReply(t *testing.T) {
	service, adapter, _, order := newFullOutputHarness("最终答案")
	adapter.startErr = errors.New("create card failed")
	msg := &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"}
	session := &types.Session{ID: "session-1"}

	err := service.handleMessageFullOutput(
		context.Background(), msg, session, nil, nil, nil, nil, adapter, adapter, "user-key", nil,
	)
	if err != nil {
		t.Fatalf("handleMessageFullOutput() error = %v", err)
	}

	wantOrder := []string{"start", "qa", "plain-reply"}
	if got := order.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("lifecycle order = %v, want %v", got, wantOrder)
	}
	if adapter.plainReplies != 1 {
		t.Fatalf("plain fallback replies = %d, want 1", adapter.plainReplies)
	}
	if adapter.plainContent != "最终答案" {
		t.Fatalf("plain reply = %q, want %q", adapter.plainContent, "最终答案")
	}
	if adapter.finalContent != "" {
		t.Fatalf("FinalizeStream ran after StartStream failure, content=%q", adapter.finalContent)
	}
}

func TestHandleMessageFullOutputFinalizeFailureSendsPlainReply(t *testing.T) {
	service, adapter, _, order := newFullOutputHarness("最终答案")
	adapter.finalizeErr = errors.New("card update failed")
	msg := &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"}
	session := &types.Session{ID: "session-1"}

	err := service.handleMessageFullOutput(
		context.Background(), msg, session, nil, nil, nil, nil, adapter, adapter, "user-key", nil,
	)
	if err != nil {
		t.Fatalf("successful plain fallback should not return error, got %v", err)
	}

	wantOrder := []string{"start", "qa", "finalize", "end", "plain-reply"}
	if got := order.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("lifecycle order = %v, want %v", got, wantOrder)
	}
	if adapter.plainReplies != 1 {
		t.Fatalf("plain fallback replies = %d, want 1", adapter.plainReplies)
	}
	if adapter.plainContent != "最终答案" {
		t.Fatalf("plain reply = %q, want %q", adapter.plainContent, "最终答案")
	}
}

func TestHandleMessageFullOutputThinkOnlyReplacesWithNoAnswerFallback(t *testing.T) {
	service, adapter, _, _ := newFullOutputHarness("<think>only reasoning</think>")
	msg := &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"}
	session := &types.Session{ID: "session-1"}

	err := service.handleMessageFullOutput(
		context.Background(), msg, session, nil, nil, nil, nil, adapter, adapter, "user-key", nil,
	)
	if err != nil {
		t.Fatalf("handleMessageFullOutput() error = %v", err)
	}
	if adapter.finalContent != imNoAnswerFallback {
		t.Fatalf("final content = %q, want %q", adapter.finalContent, imNoAnswerFallback)
	}
}

func TestExecuteQARequestFullOutputDispatchesToProgressSender(t *testing.T) {
	service, adapter, _, order := newFullOutputHarness("最终答案")
	ctx, cancel := context.WithCancel(context.Background())
	service.executeQARequest(&qaRequest{
		ctx:     ctx,
		cancel:  cancel,
		msg:     &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"},
		session: &types.Session{ID: "session-1"},
		adapter: adapter,
		channel: &IMChannel{OutputMode: "full"},
		userKey: "user-key",
	})

	wantOrder := []string{"start", "qa", "finalize", "end"}
	if got := order.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("lifecycle order = %v, want %v", got, wantOrder)
	}
	if adapter.plainReplies != 0 {
		t.Fatalf("plain fallback replies = %d, want 0", adapter.plainReplies)
	}
}

func TestExecuteQARequestNativeProcessingFeedbackStopsAfterFinalReply(t *testing.T) {
	service, inner, _, order := newFullOutputHarness("最终答案")
	adapter := &processingFullOutputAdapter{fullOutputAdapter: inner}
	ctx, cancel := context.WithCancel(context.Background())
	service.executeQARequest(&qaRequest{
		ctx: ctx, cancel: cancel,
		msg:     &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"},
		session: &types.Session{ID: "session-1"}, adapter: adapter,
		channel: &IMChannel{OutputMode: "full"}, userKey: "user-key",
	})
	wantOrder := []string{"processing-start", "start", "qa", "finalize", "end", "processing-stop"}
	if got := order.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("native feedback lifecycle = %v, want %v", got, wantOrder)
	}
}

func TestExecuteQARequestRevokedScopeDoesNotShowProcessing(t *testing.T) {
	service, inner, _, order := newFullOutputHarness("最终答案")
	adapter := &revokedProcessingAdapter{processingFullOutputAdapter: &processingFullOutputAdapter{fullOutputAdapter: inner}}
	ctx, cancel := context.WithCancel(context.Background())
	service.executeQARequest(&qaRequest{
		ctx: ctx, cancel: cancel,
		msg:     &IncomingMessage{Platform: "octo", UserID: "user-1", Content: "问题"},
		session: &types.Session{ID: "session-1"}, adapter: adapter,
		channel: &IMChannel{Platform: "octo", OutputMode: "full"},
		scope:   &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "before"},
		userKey: "user-key",
	})
	if got := order.snapshot(); len(got) != 0 {
		t.Fatalf("revoked request produced visible feedback: %v", got)
	}
}

func TestExecuteQARequestAllowsNotifierWithoutStopFunction(t *testing.T) {
	service, inner, _, order := newFullOutputHarness("最终答案")
	adapter := &nilStopProcessingAdapter{fullOutputAdapter: inner}
	ctx, cancel := context.WithCancel(context.Background())
	service.executeQARequest(&qaRequest{
		ctx: ctx, cancel: cancel,
		msg:     &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"},
		session: &types.Session{ID: "session-1"}, adapter: adapter,
		channel: &IMChannel{OutputMode: "full"}, userKey: "user-key",
	})
	wantOrder := []string{"start", "qa", "finalize", "end"}
	if got := order.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("nil notifier stop changed QA = %v, want %v", got, wantOrder)
	}
}

func TestExecuteQARequestFullOutputSkipsProgressWhenNotSupported(t *testing.T) {
	service, inner, _, order := newFullOutputHarness("最终答案")
	adapter := &fullOutputNoProgressAdapter{fullOutputAdapter: inner}
	ctx, cancel := context.WithCancel(context.Background())
	service.executeQARequest(&qaRequest{
		ctx:     ctx,
		cancel:  cancel,
		msg:     &IncomingMessage{Platform: PlatformFeishu, UserID: "user-1", Content: "问题"},
		session: &types.Session{ID: "session-1"},
		adapter: adapter,
		channel: &IMChannel{OutputMode: "full"},
		userKey: "user-key",
	})

	wantOrder := []string{"qa", "plain-reply"}
	if got := order.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("lifecycle order = %v, want %v", got, wantOrder)
	}
	if inner.plainContent != "最终答案" {
		t.Fatalf("plain reply = %q, want %q", inner.plainContent, "最终答案")
	}
}

func TestFormatIMOutboundAnswerOrFallbackAfterCleanup(t *testing.T) {
	got := formatIMOutboundAnswerOrFallback(
		context.Background(), "<think>only reasoning</think>", nil, nil,
	)
	if got != imNoAnswerFallback {
		t.Fatalf("think-only final content = %q, want fallback %q", got, imNoAnswerFallback)
	}
}

func TestIMQAFailureReply(t *testing.T) {
	if got := imQAFailureReply(context.Canceled); got != imCancelledFallback {
		t.Fatalf("canceled = %q, want %q", got, imCancelledFallback)
	}
	if got := imQAFailureReply(context.DeadlineExceeded); got != imCancelledFallback {
		t.Fatalf("deadline = %q, want %q", got, imCancelledFallback)
	}
	if got := imQAFailureReply(errors.New("boom")); got != imErrorFallback {
		t.Fatalf("other error = %q, want %q", got, imErrorFallback)
	}
}
