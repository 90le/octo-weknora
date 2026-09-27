package octo

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/logger"
)

const (
	typingRefreshInterval = 5 * time.Second
	typingRequestTimeout  = 3 * time.Second
)

var _ im.ProcessingNotifier = (*Adapter)(nil)

// StartProcessing uses Octo's ephemeral typing command as feedback while a
// request is actually running. It never creates a chat message or exposes the
// query, tool calls, or model reasoning. A failed hint cannot fail QA.
func (a *Adapter) StartProcessing(ctx context.Context, in *im.IncomingMessage) func() {
	return a.startProcessingWithInterval(ctx, in, typingRefreshInterval)
}

func (a *Adapter) startProcessingWithInterval(ctx context.Context, in *im.IncomingMessage, interval time.Duration) func() {
	target, kind, ok := a.typingTarget(in)
	if !ok || ctx.Err() != nil {
		return func() {}
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		first := true
		delay := interval
		for {
			if runCtx.Err() != nil {
				return
			}
			callCtx, callCancel := context.WithTimeout(runCtx, typingRequestTimeout)
			err := a.api.post(callCtx, "/v1/bot/typing", map[string]any{
				"channel_id": target, "channel_type": kind,
			}, nil)
			callCancel()
			if err != nil && first && runCtx.Err() == nil {
				logger.Warnf(runCtx, "[IM] Octo typing status unavailable: %v", err)
			}
			first = false
			var status *apiStatusError
			if errors.As(err, &status) && (status.Status == 401 || status.Status == 403 || status.Status == 429) {
				// This is only a UI hint. Do not keep hitting a denied or
				// rate-limited endpoint for the remainder of the QA run.
				return
			}
			if err == nil {
				delay = interval
			} else {
				delay = min(delay*2, 30*time.Second)
			}
			timer := time.NewTimer(delay)
			select {
			case <-runCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func (a *Adapter) typingTarget(in *im.IncomingMessage) (string, int, bool) {
	if a == nil || a.api == nil || in == nil || in.Platform != Platform ||
		in.UserID == "" || in.UserID == a.uid || in.Extra == nil ||
		!wire.DecimalID(in.Extra["octo_message_id"]) {
		return "", 0, false
	}
	kind, err := strconv.Atoi(in.Extra["octo_channel_type"])
	if err != nil || (kind != 1 && kind != 2 && kind != 5) {
		return "", 0, false
	}
	target := in.Extra["octo_channel_id"]
	if kind == 1 {
		target = in.UserID
	}
	if _, err = wire.ParseScope(target, byte(kind)); err != nil {
		return "", 0, false
	}
	return target, kind, true
}
