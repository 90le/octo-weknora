package github

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedGitLockWaitCancelsAndThenAcquires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror.lock")
	unlockRead, err := mirrorLock(context.Background(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_, err = mirrorLock(ctx, path, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive waiter must time out behind a reader: %v", err)
	}
	unlockRead()
	unlockWrite, err := mirrorLock(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	unlockWrite()
}
