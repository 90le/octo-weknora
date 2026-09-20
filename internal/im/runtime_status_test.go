package im

import (
	"encoding/json"
	"strings"
	"testing"
)

type runtimeStatusTestAdapter struct {
	lifecycleTestAdapter
	state ChannelRuntimeStatus
}

func (a *runtimeStatusTestAdapter) RuntimeStatus() ChannelRuntimeStatus { return a.state }

func TestRuntimeStatusDoesNotConfuseEnabledOrInitializedWithOnline(t *testing.T) {
	a := &runtimeStatusTestAdapter{state: ChannelRuntimeStatus{State: "stopped", ErrorCode: "server_disconnect_12"}}
	s := &Service{channels: map[string]*channelState{"test": {Channel: &IMChannel{ID: "test", Enabled: true}, Adapter: a}}}
	if s.channelRuntimeStatus("test", true).State != "stopped" {
		t.Fatal("enabled stopped transport reported online")
	}
	if s.channelRuntimeStatus("test", false).State != "disabled" {
		t.Fatal("disabled configuration ignored")
	}
	if s.channelRuntimeStatus("absent", true).State != "unavailable" {
		t.Fatal("uninitialized transport reported online")
	}
	s.channels["test"].Adapter = &lifecycleTestAdapter{}
	if s.channelRuntimeStatus("test", true).State != "initialized" {
		t.Fatal("other adapter readiness invented")
	}
	ch := IMChannel{Enabled: true, RuntimeStatus: &a.state}
	b, _ := json.Marshal(SummarizeIMChannel(ch))
	if !strings.Contains(string(b), `"runtime_status":{"state":"stopped","error_code":"server_disconnect_12"`) {
		t.Fatalf("summary lost runtime diagnosis: %s", b)
	}
}
