package octo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

var errAPIUnavailable = errors.New("Octo API unavailable")

type apiStatusError struct{ Status int }

func (e *apiStatusError) Error() string { return fmt.Sprintf("Octo API returned HTTP %d", e.Status) }

func retryRegistration(err error) bool {
	if errors.Is(err, errAPIUnavailable) {
		return true
	}
	var status *apiStatusError
	return errors.As(err, &status) && (status.Status == 408 || status.Status == 429 || status.Status >= 500)
}
func terminalTransport(err error) bool {
	var disconnect *wire.DisconnectError
	if errors.As(err, &disconnect) {
		return !disconnect.Retryable()
	}
	return errors.Is(err, wire.ErrAuthentication) || errors.Is(err, wire.ErrDisconnected) || errors.Is(err, wire.ErrProtocol)
}

func (a *Adapter) RuntimeStatus() im.ChannelRuntimeStatus {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.status.State == "" {
		return im.ChannelRuntimeStatus{State: "starting"}
	}
	return a.status
}
func (a *Adapter) setRuntimeStatus(state string, err error) {
	status := im.ChannelRuntimeStatus{State: state, UpdatedAt: time.Now()}
	if err != nil {
		status.ErrorCode, status.Message = "transport_unavailable", "连接暂时不可用，正在重连"
		var disconnect *wire.DisconnectError
		switch {
		case errors.As(err, &disconnect):
			status.ErrorCode = fmt.Sprintf("server_disconnect_%d", disconnect.Reason)
			status.Message = fmt.Sprintf("服务端断开连接（原因码 %d）", disconnect.Reason)
			if disconnect.Reason == 12 {
				status.Message += "；可能存在另一接收端，为避免互踢已停止，请核对后重新启用"
			} else if !disconnect.Retryable() {
				status.Message += "；已停止，请核对服务端诊断后重新启用"
			}
		case errors.Is(err, wire.ErrAuthentication):
			status.ErrorCode, status.Message = "authentication_rejected", "IM 鉴权失败，已停止，请检查 Bot 连接"
		case errors.Is(err, context.Canceled):
			status.ErrorCode, status.Message = "", "渠道已停止"
		case state == "stopped":
			status.ErrorCode, status.Message = "connection_rejected", "连接被拒绝或协议无效，已停止，请检查服务日志后重新启用"
		}
	}
	a.statusMu.Lock()
	a.status = status
	a.statusMu.Unlock()
}
