package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/router"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

type graphEnqueueCapture struct {
	executor *router.SyncTaskExecutor
	opts     chan []asynq.Option
}

func (c *graphEnqueueCapture) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	c.opts <- append([]asynq.Option(nil), opts...)
	return c.executor.Enqueue(task, opts...)
}

func TestChunkExtractTaskPassesOptionsToLiteExecutor(t *testing.T) {
	t.Setenv("NEO4J_ENABLE", "true")

	executor := router.NewSyncTaskExecutor()
	observed := make(chan struct {
		retried  int
		maxRetry int
		ok       bool
	}, 1)
	executor.RegisterHandler(types.TypeChunkExtract, func(ctx context.Context, _ *asynq.Task) error {
		retried, maxRetry, ok := types.TaskRetryMetadataFromContext(ctx)
		observed <- struct {
			retried  int
			maxRetry int
			ok       bool
		}{retried, maxRetry, ok}
		return nil
	})
	queue := &graphEnqueueCapture{executor: executor, opts: make(chan []asynq.Option, 1)}
	enqueued, err := service.NewChunkExtractTask(
		context.Background(), queue, 7, "chunk-1", "model-1", "knowledge-1", 1, 0,
	)
	if err != nil || !enqueued {
		t.Fatalf("enqueue graph extraction: enqueued=%v err=%v", enqueued, err)
	}

	select {
	case opts := <-queue.opts:
		values := make(map[asynq.OptionType]any, len(opts))
		for _, opt := range opts {
			values[opt.Type()] = opt.Value()
		}
		if got := values[asynq.QueueOpt]; got != types.QueueGraph {
			t.Errorf("queue = %v, want %s", got, types.QueueGraph)
		}
		if got := values[asynq.MaxRetryOpt]; got != 3 {
			t.Errorf("max retries = %v, want 3", got)
		}
		if got := values[asynq.TimeoutOpt]; got != 30*time.Minute {
			t.Errorf("timeout = %v, want 30m", got)
		}
	default:
		t.Fatal("graph task did not pass options to Enqueue")
	}

	select {
	case got := <-observed:
		if !got.ok || got.retried != 0 || got.maxRetry != 3 {
			t.Fatalf("Lite retry metadata = %+v, want attempt 0/3", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Lite graph task did not run")
	}
}
