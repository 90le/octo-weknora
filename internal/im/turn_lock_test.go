package im

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestIMSessionTurnLockSerializesAndCancelsWaiters(t *testing.T) {
	for _, mode := range []string{"local", "redis"} {
		t.Run(mode, func(t *testing.T) {
			service := &Service{}
			if mode == "redis" {
				server := miniredis.RunT(t)
				service.redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
				defer service.redis.Close()
			}
			session := &types.Session{ID: "one", TenantID: 7}
			entered := make(chan struct{})
			release := make(chan struct{})
			firstDone := make(chan error, 1)
			go func() {
				firstDone <- service.withIMSessionTurnLock(context.Background(), session, func(context.Context) error {
					close(entered)
					<-release
					return nil
				})
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first turn did not start")
			}
			other := &types.Session{ID: "two", TenantID: 7}
			require.NoError(t, service.withIMSessionTurnLock(context.Background(), other, func(context.Context) error { return nil }))
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := service.withIMSessionTurnLock(ctx, session, func(context.Context) error {
				t.Error("overlapping turns entered the same session")
				return nil
			})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			close(release)
			require.NoError(t, <-firstDone)
			require.NoError(t, service.withIMSessionTurnLock(context.Background(), session, func(context.Context) error { return nil }))
			if mode == "local" {
				service.localTurnLocks.mu.Lock()
				remaining := len(service.localTurnLocks.entries)
				service.localTurnLocks.mu.Unlock()
				require.Zero(t, remaining)
			}
		})
	}
}

func TestIMLocalTurnLockHonorsAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var locks imTurnLocks
	err := locks.withLock(ctx, "session", func(context.Context) error {
		return errors.New("canceled turn entered")
	})
	require.ErrorIs(t, err, context.Canceled)
}
