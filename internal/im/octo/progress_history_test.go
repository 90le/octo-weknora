package octo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
)

func TestProgressFinalIsCollapsedAndKeepsSafeObservedSteps(t *testing.T) {
	data := progressData(
		"fixture",
		"completed",
		[]string{
			"configuration",
			"configuration:done",
			"preview",
			"preview:done",
			"answer",
			"untrusted secret",
		},
		true,
		4*time.Second,
	)
	if data["traceExpanded"] != false || data["traceCollapsed"] != true || data["statusLabel"] != "预览待确认" {
		t.Fatalf("wrong terminal state: %#v", data)
	}
	raw, _ := json.Marshal(data)
	for _, expected := range []string{"核对当前区域配置", "准备知识变更预览", "预览已生成，变更尚未执行", "用时 4 秒"} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("missing real progress %q", expected)
		}
	}
	if strings.Contains(string(raw), "untrusted secret") {
		t.Fatal("unrecognized data leaked into progress card")
	}
	if len(data["phases"].([]any)) != 3 {
		t.Fatal("steps were repeated or unrecognized data was displayed")
	}
}

func TestProgressHistoryIsBoundedAndMarksOmittedSteps(t *testing.T) {
	p := &progressCard{updates: make(chan struct{}, 1)}
	for _, stage := range []string{
		"configuration", "configuration:done", "knowledge", "knowledge:done", "documents", "documents:done",
		"source", "contacts", "issues", "answer",
	} {
		p.Report(stage)
	}
	history := p.progressHistory()
	if len(history) != 6 || history[0] != "earlier" || history[len(history)-1] != "answer" {
		t.Fatalf("unbounded or misleading history: %v", history)
	}
}

func TestProgressRetryCoalescesOnlyTheSameFailedInvocation(t *testing.T) {
	p := &progressCard{updates: make(chan struct{}, 1)}
	for _, stage := range []string{
		"configuration", "configuration:done", "preview", "preview:failed", "preview", "preview:done", "answer",
	} {
		p.Report(stage)
	}
	history := p.progressHistory()
	if got := strings.Join(history, ","); got != "configuration:done,preview:retried,answer" {
		t.Fatalf("retry was not summarized: %s", got)
	}
	raw, _ := json.Marshal(progressData("fixture", "completed", history, true, time.Second))
	if !strings.Contains(string(raw), "重试后") || strings.Contains(string(raw), "步骤未成功") ||
		strings.Contains(string(raw), "正在整理答复") {
		t.Fatalf("successful retry has a misleading display: %s", raw)
	}
	var multiple []string
	for _, stage := range []string{"knowledge", "knowledge:done", "knowledge", "knowledge:done"} {
		multiple = compactProgressStage(multiple, stage)
	}
	if len(multiple) != 2 {
		t.Fatalf("distinct successful calls were merged: %v", multiple)
	}
}

func TestProgressCancellationPreservesQueuedDeliveryResult(t *testing.T) {
	for _, result := range []im.ProcessingResult{im.ProcessingCompleted, im.ProcessingStopped, im.ProcessingFailed} {
		p := &progressCard{finish: make(chan im.ProcessingResult, 1)}
		p.Finish(result)
		if got, want := p.resultAfterCancellation(), progressResultState(result); got != want {
			t.Fatalf("queued result %q became %q, want %q", result, got, want)
		}
	}
	p := &progressCard{finish: make(chan im.ProcessingResult, 1)}
	if got := p.resultAfterCancellation(); got != "error" {
		t.Fatalf("cancellation without a result became %q", got)
	}
}
