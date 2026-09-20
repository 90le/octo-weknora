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
	return a.run(ctx, accept, wire.RunConnection, waitReconnect)
}

type connectionRunner func(context.Context, wire.Credentials, func(context.Context, *wire.Message) error, ...func()) error

func waitReconnect(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay + time.Duration(rand.IntN(1000))*time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *Adapter) run(ctx context.Context, accept func(context.Context, *im.IncomingMessage) error, connect connectionRunner, wait func(context.Context, time.Duration) error) (runErr error) {
	a.setRuntimeStatus("starting", nil)
	defer func() { a.setRuntimeStatus("stopped", runErr) }()
	if accept == nil {
		return errors.New("Octo requires an authorized inbound dispatcher")
	}
	delay := time.Second
	for ctx.Err() == nil {
		reg, err := a.api.register(ctx)
		if err != nil {
			if !retryRegistration(err) || ctx.Err() != nil {
				return err
			}
			a.setRuntimeStatus("reconnecting", err)
			logger.Warnf(ctx, "[IM] Octo registration retrying: %v", err)
			if err = wait(ctx, delay); err != nil {
				return err
			}
			delay = min(delay*2, 30*time.Second)
			continue
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
		err = connect(connectionCtx, wire.Credentials{UID: reg.UID, Token: reg.Token, URL: reg.WS}, func(callCtx context.Context, raw *wire.Message) error {
			msg, normalizeErr := a.Normalize(raw)
			if normalizeErr != nil {
				return normalizeErr
			}
			if msg == nil {
				return nil
			}
			return accept(callCtx, msg)
		}, func() { a.setRuntimeStatus("online", nil) })
		cancel()
		if terminalTransport(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warnf(ctx, "[IM] Octo transport reconnecting: %v", err)
		a.setRuntimeStatus("reconnecting", err)
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		if err = wait(ctx, delay); err != nil {
			return err
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
