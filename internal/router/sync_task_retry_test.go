package router

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

func TestSyncTaskExecutorInjectsRetryMetadata(t *testing.T) {
	executor := NewSyncTaskExecutor()
	observed := make(chan [2]int, 1)
	executor.RegisterHandler("test:retry-metadata", func(ctx context.Context, _ *asynq.Task) error {
		retried, maxRetry, ok := types.TaskRetryMetadataFromContext(ctx)
		if !ok {
			observed <- [2]int{-1, -1}
			return nil
		}
		observed <- [2]int{retried, maxRetry}
		return nil
	})

	task := asynq.NewTask("test:retry-metadata", nil)
	if _, err := executor.Enqueue(task, asynq.MaxRetry(3)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case got := <-observed:
		if got != [2]int{0, 3} {
			t.Fatalf("retry metadata = %v, want [0 3]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sync task")
	}
}

func TestSyncTaskExecutorStopsOnSkipRetry(t *testing.T) {
	executor := NewSyncTaskExecutor()
	var calls atomic.Int32
	done := make(chan struct{}, 1)
	executor.RegisterHandler("test:skip-retry", func(context.Context, *asynq.Task) error {
		calls.Add(1)
		done <- struct{}{}
		return fmt.Errorf("source was cancelled: %w", asynq.SkipRetry)
	})

	_, err := executor.Enqueue(asynq.NewTask("test:skip-retry", nil), asynq.MaxRetry(5))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first handler invocation")
	}
	// The old Lite executor retried this five times. Give a small window for an
	// accidental retry without making the test depend on the normal 5s backoff.
	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("SkipRetry handler calls = %d, want 1", got)
	}
}

func TestSyncTaskExecutorBoundsEnrichmentFanout(t *testing.T) {
	executor := newSyncTaskExecutorWithLimits(map[string]int{
		types.WorkerPoolEnrichment: 2,
	})
	started := make(chan struct{}, 4)
	done := make(chan struct{}, 4)
	release := make(chan struct{})
	var running atomic.Int32
	var maxRunning atomic.Int32
	executor.RegisterHandler(types.TypeChunkExtract, func(context.Context, *asynq.Task) error {
		current := running.Add(1)
		for {
			max := maxRunning.Load()
			if current <= max || maxRunning.CompareAndSwap(max, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		running.Add(-1)
		done <- struct{}{}
		return nil
	})

	for i := 0; i < 4; i++ {
		if _, err := executor.Enqueue(asynq.NewTask(types.TypeChunkExtract, nil), asynq.MaxRetry(0)); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for bounded enrichment tasks")
		}
	}
	select {
	case <-started:
		t.Fatal("third enrichment task started before a limiter slot was released")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	for i := 0; i < 4; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for enrichment fanout to drain")
		}
	}
	if got := maxRunning.Load(); got != 2 {
		t.Fatalf("maximum active enrichment tasks = %d, want 2", got)
	}
}

func TestSyncTaskExecutorHonorsTaskTimeout(t *testing.T) {
	executor := NewSyncTaskExecutor()
	observed := make(chan error, 1)
	executor.RegisterHandler("test:timeout", func(ctx context.Context, _ *asynq.Task) error {
		<-ctx.Done()
		observed <- ctx.Err()
		return nil
	})
	if _, err := executor.Enqueue(asynq.NewTask("test:timeout", nil), asynq.MaxRetry(0), asynq.Timeout(20*time.Millisecond)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case err := <-observed:
		if err != context.DeadlineExceeded {
			t.Fatalf("handler context error = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task deadline")
	}
}

func TestSyncTaskExecutorStartsTimeoutAfterLimiterAdmission(t *testing.T) {
	executor := newSyncTaskExecutorWithLimits(map[string]int{
		types.WorkerPoolEnrichment: 1,
	})
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondObserved := make(chan time.Duration, 1)
	var calls atomic.Int32
	executor.RegisterHandler(types.TypeChunkExtract, func(ctx context.Context, _ *asynq.Task) error {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return nil
		}
		started := time.Now()
		<-ctx.Done()
		secondObserved <- time.Since(started)
		return nil
	})

	if _, err := executor.Enqueue(asynq.NewTask(types.TypeChunkExtract, nil), asynq.MaxRetry(0)); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first task did not acquire the limiter")
	}
	if _, err := executor.Enqueue(asynq.NewTask(types.TypeChunkExtract, nil), asynq.MaxRetry(0), asynq.Timeout(40*time.Millisecond)); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	// Keep the second task queued well past its eventual handler timeout.
	time.Sleep(80 * time.Millisecond)
	close(releaseFirst)
	select {
	case elapsed := <-secondObserved:
		if elapsed < 25*time.Millisecond {
			t.Fatalf("second handler timeout elapsed after %s; want a fresh timeout after admission", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second task did not receive a fresh handler timeout")
	}
}
