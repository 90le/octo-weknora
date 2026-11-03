package octo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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
	if len(data["phases"].([]any)) != 5 {
		t.Fatal("unrecognized step was displayed")
	}
}

func TestProgressHistoryIsBoundedAndMarksOmittedSteps(t *testing.T) {
	p := &progressCard{updates: make(chan struct{}, 1)}
	for _, stage := range []string{
		"configuration", "configuration:done", "knowledge", "knowledge:done", "documents", "documents:done", "answer",
	} {
		p.Report(stage)
	}
	history := p.progressHistory()
	if len(history) != 6 || history[0] != "earlier" || history[len(history)-1] != "answer" {
		t.Fatalf("unbounded or misleading history: %v", history)
	}
}
