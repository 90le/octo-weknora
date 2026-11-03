package im

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/common/redislock"
	"github.com/Tencent/WeKnora/internal/types"
)

// imTurnLocks supplies the single-process fallback. A reference count removes
// idle session entries while concurrent callers inspect the same semaphore.
type imTurnLocks struct {
	mu      sync.Mutex
	entries map[string]*imTurnLockEntry
}

type imTurnLockEntry struct {
	semaphore chan struct{}
	refs      int
}

func (l *imTurnLocks) withLock(ctx context.Context, key string, fn func(context.Context) error) error {
	l.mu.Lock()
	if l.entries == nil {
		l.entries = map[string]*imTurnLockEntry{}
	}
	entry := l.entries[key]
	if entry == nil {
		entry = &imTurnLockEntry{semaphore: make(chan struct{}, 1)}
		l.entries[key] = entry
	}
	entry.refs++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.entries, key)
		}
		l.mu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case entry.semaphore <- struct{}{}:
		defer func() { <-entry.semaphore }()
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(ctx)
	default:
		return redislock.ErrLockBusy
	}
}

// Serializing sandbox-capable turns prevents two messages in one session from
// collecting each other's files. A concurrent request for the same session is
// rejected promptly, so it cannot occupy a worker or overwrite the active
// request's /stop entry. A lost Redis lease cancels the current turn.
func (s *Service) withIMSessionTurnLock(
	ctx context.Context, session *types.Session, fn func(context.Context) error,
) error {
	key := fmt.Sprintf("weknora:im:sandbox-turn:%d:%s", session.TenantID, session.ID)
	if s.redis == nil {
		return s.localTurnLocks.withLock(ctx, key, fn)
	}
	return redislock.TryWithRenewableLock(ctx, s.redis, key, 30*time.Second, 10*time.Second, fn)
}
