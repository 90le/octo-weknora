package octo

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/logger"
)

// Run receives messages but never calls the native Agent automatically. accept
// must verify sender/scope and durably enqueue admitted input. Returning nil for
// deliberately ignored input acknowledges it; returning an error leaves it
// unacknowledged. Public channels must not pass im.Service.HandleMessage directly
// until KB scope enforcement and attachment admission are implemented there.
func (a *Adapter) Run(ctx context.Context, accept func(context.Context, *im.IncomingMessage) error) error {
	if accept == nil {
		return errors.New("Octo requires an authorized inbound dispatcher")
	}
	delay := time.Second
	for ctx.Err() == nil {
		reg, err := a.api.register(ctx)
		if err != nil {
			return err
		}
		if reg.UID != a.uid {
			return errors.New("Octo registered identity differs from configured Bot")
		}
		connectionCtx, cancel := context.WithCancel(ctx)
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-connectionCtx.Done():
					return
				case <-ticker.C:
					_ = a.api.post(connectionCtx, "/v1/bot/heartbeat", struct{}{}, nil)
				}
			}
		}()
		started := time.Now()
		err = wire.RunConnection(connectionCtx, wire.Credentials{UID: reg.UID, Token: reg.Token, URL: reg.WS}, func(callCtx context.Context, raw *wire.Message) error {
			msg, normalizeErr := a.Normalize(raw)
			if normalizeErr != nil {
				return normalizeErr
			}
			if msg == nil {
				return nil
			}
			return accept(callCtx, msg)
		})
		cancel()
		if errors.Is(err, wire.ErrAuthentication) || errors.Is(err, wire.ErrDisconnected) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warnf(ctx, "[IM] Octo transport reconnecting: %v", err)
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		wait := delay + time.Duration(rand.IntN(1000))*time.Millisecond
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
	return ctx.Err()
}
