package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type statusCounter struct {
	*fakeSandboxConfigService
	tenant uint64
	config string
	err    error
}

func (s *statusCounter) RuntimeDockerCount(_ context.Context, tenant uint64, config string) (int, error) {
	s.tenant = tenant
	s.config = config
	return 3, s.err
}

type failedStatusInspector struct{ runtimeTestInspector }

func (failedStatusInspector) QueueStats(context.Context) ([]types.QueueStat, bool, error) {
	return nil, true, errors.New("secret /home/user/token")
}

func TestRuntimeStatusScopedAndRedacted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	counter := &statusCounter{fakeSandboxConfigService: &fakeSandboxConfigService{}}
	h := &SystemHandler{taskInspector: failedStatusInspector{}, sandboxConfigSvc: counter}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(
		http.MethodGet, "/system/admin/runtime/status?tenant_id=7&sandbox_config_id=cfg", nil,
	)
	h.GetRuntimeStatus(c)
	body := w.Body.String()
	if w.Code != 200 || counter.tenant != 7 || counter.config != "cfg" ||
		!strings.Contains(body, `"running":3`) || !strings.Contains(body, `"capacity":null`) ||
		!strings.Contains(body, `"failure":"unavailable"`) {
		t.Fatalf("status=%d body=%s counter=%+v", w.Code, body, counter)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "/home/user") {
		t.Fatal("leaked error text")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing cache control")
	}
}

func TestRuntimeStatusRejectsIncompleteSelector(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/system/admin/runtime/status?tenant_id=7", nil)
	(&SystemHandler{}).GetRuntimeStatus(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestRuntimeFailureOnlyUsesAllowlistedKind(t *testing.T) {
	err := sandbox.NewRemoteError(
		sandbox.SandboxTypeDocker, "List", sandbox.RemoteErrorKindAuthentication, "/secret/user", errors.New("token"),
	)
	if got := runtimeFailure(err); got != "authentication" {
		t.Fatal(got)
	}
	err.Kind = sandbox.RemoteErrorKind("secret")
	if got := runtimeFailure(err); got != "unavailable" {
		t.Fatal(got)
	}
}

func TestRuntimeProbeDoesNotQueueBehindBlockedProbe(t *testing.T) {
	slot := make(chan struct{}, 1)
	slot <- struct{}{}
	_, err := runtimeProbe(context.Background(), slot, func(context.Context) (int, error) {
		t.Error("unexpected invocation")
		return 0, nil
	})
	if !errors.Is(err, errRuntimeBusy) {
		t.Fatal(err)
	}
}

func TestRuntimeQueuesIncludesSupplementAndFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	counter := &statusCounter{fakeSandboxConfigService: &fakeSandboxConfigService{}}
	h := &SystemHandler{taskInspector: failedStatusInspector{}, sandboxConfigSvc: counter}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(
		http.MethodGet, "/system/admin/runtime/queues?tenant_id=7&sandbox_config_id=cfg", nil,
	)
	h.GetRuntimeQueues(c)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"octo_runtime"`) || !strings.Contains(body, `"running":3`) ||
		!strings.Contains(body, `"queue_failure":"unavailable"`) || strings.Contains(body, "/home/user") {
		t.Fatalf("status=%d body=%s", w.Code, body)
	}
}

func TestRuntimeProbeHonorsCancellation(t *testing.T) {
	slot := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runtimeProbe(ctx, slot, func(ctx context.Context) (int, error) { <-ctx.Done(); return 0, ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
