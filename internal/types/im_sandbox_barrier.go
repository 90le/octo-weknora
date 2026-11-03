package types

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// IMSandboxTurnBarrier is shared by all contexts in one admitted IM turn.
// Sealing denies new sandbox operations, waits for in-flight operations, then
// confirms every affected container is stopped. It is not a distributed lock:
// the lock owner must retain or quarantine its lease until SealAndStop succeeds.
type IMSandboxTurnBarrier struct {
	mu     sync.Mutex
	waitMu sync.Mutex
	sealed bool
	active int
	idle   chan struct{}
	stops  map[string]func(context.Context) error
}

// NewIMSandboxTurnBarrier creates a barrier for an admitted IM turn.
func NewIMSandboxTurnBarrier() *IMSandboxTurnBarrier {
	idle := make(chan struct{})
	close(idle)
	return &IMSandboxTurnBarrier{idle: idle, stops: make(map[string]func(context.Context) error)}
}

// Begin returns a per-operation tracker and idempotent completion callback.
func (b *IMSandboxTurnBarrier) Begin() (func(string, func(context.Context) error), func(), error) {
	if b == nil {
		return nil, nil, errors.New("IM sandbox turn barrier unavailable")
	}
	b.mu.Lock()
	if b.sealed {
		b.mu.Unlock()
		return nil, nil, errors.New("IM sandbox turn is closing")
	}
	if b.active == 0 {
		b.idle = make(chan struct{})
	}
	b.active++
	b.mu.Unlock()
	var once sync.Once
	track := func(id string, stop func(context.Context) error) {
		if id == "" || stop == nil {
			return
		}
		b.mu.Lock()
		b.stops[id] = stop
		b.mu.Unlock()
	}
	done := func() {
		once.Do(func() {
			b.mu.Lock()
			b.active--
			if b.active == 0 {
				close(b.idle)
			}
			b.mu.Unlock()
		})
	}
	return track, done, nil
}

// SealAndStop must complete successfully before a session lock is released.
// On cancellation, uncertain status or stop failure the barrier remains sealed
// and the caller must retain/quarantine the session and retry reconciliation.
func (b *IMSandboxTurnBarrier) SealAndStop(ctx context.Context) error {
	if b == nil {
		return errors.New("IM sandbox turn barrier unavailable")
	}
	b.waitMu.Lock()
	defer b.waitMu.Unlock()
	b.mu.Lock()
	b.sealed = true
	idle := b.idle
	b.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	stops := make(map[string]func(context.Context) error, len(b.stops))
	for id, fn := range b.stops {
		stops[id] = fn
	}
	b.mu.Unlock()
	for id, stop := range stops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stop(ctx); err != nil {
			return fmt.Errorf("IM sandbox container stop unconfirmed: %w", err)
		}
		b.mu.Lock()
		delete(b.stops, id)
		b.mu.Unlock()
	}
	return nil
}

// SealIMSandboxTurn is read-only with respect to the DB pin and session files.
// It may stop containers, and must use a live cleanup context carrying the grant.
func SealIMSandboxTurn(ctx context.Context) error {
	grant, ok := IMSandboxGrantFromContext(ctx)
	if !ok || (!grant.AllowShell && !grant.AllowSkills) {
		return nil
	}
	return grant.TurnBarrier.SealAndStop(ctx)
}
