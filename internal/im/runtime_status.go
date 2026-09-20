package im

import "time"

// Configuration being enabled does not prove an authenticated live transport.
// This optional interface leaves other platform runtimes unchanged.
type ChannelRuntimeStatus struct {
	State     string    `json:"state"`
	ErrorCode string    `json:"error_code,omitempty"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}
type RuntimeStatusProvider interface{ RuntimeStatus() ChannelRuntimeStatus }

func (s *Service) channelRuntimeStatus(id string, enabled bool) *ChannelRuntimeStatus {
	if !enabled {
		return &ChannelRuntimeStatus{State: "disabled"}
	}
	a, _, active := s.GetChannelAdapter(id)
	if !active {
		return &ChannelRuntimeStatus{State: "unavailable", Message: "此实例未运行该渠道；请检查启动日志或其他实例"}
	}
	if provider, ok := a.(RuntimeStatusProvider); ok {
		status := provider.RuntimeStatus()
		return &status
	}
	return &ChannelRuntimeStatus{State: "initialized", Message: "渠道已初始化，尚无实时连接状态"}
}
