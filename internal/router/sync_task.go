package router

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/dig"
)

// SyncTaskExecutor executes tasks synchronously (in a goroutine) without Redis.
// Used in Lite mode as a drop-in replacement for *asynq.Client.
type SyncTaskExecutor struct {
	mu       sync.RWMutex
	handlers map[string]func(context.Context, *asynq.Task) error
	// Lite mode has no durable queue. These semaphores intentionally bound
	// active post-process and enrichment work, where one document can fan out
	// into many graph/question tasks. Other task classes retain the historical
	// direct goroutine behaviour.
	limiters map[string]chan struct{}
}

func NewSyncTaskExecutor() *SyncTaskExecutor {
	return newSyncTaskExecutorWithLimits(map[string]int{
		types.WorkerPoolPostProcess: types.DefaultPostProcessWorkerConcurrency,
		types.WorkerPoolEnrichment:  types.DefaultEnrichmentWorkerConcurrency,
	})
}

// newSyncTaskExecutorWithLimits is kept package-private so tests can exercise
// bounded Lite execution without changing production topology. A non-positive
// value deliberately disables the limiter for that pool.
func newSyncTaskExecutorWithLimits(limits map[string]int) *SyncTaskExecutor {
	e := &SyncTaskExecutor{
		handlers: make(map[string]func(context.Context, *asynq.Task) error),
		limiters: make(map[string]chan struct{}),
	}
	for pool, limit := range limits {
		if limit > 0 {
			e.limiters[pool] = make(chan struct{}, limit)
		}
	}
	return e
}

// RegisterHandler registers a handler for a given task type pattern.
func (e *SyncTaskExecutor) RegisterHandler(pattern string, handler func(context.Context, *asynq.Task) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[pattern] = handler
}

// Enqueue satisfies interfaces.TaskEnqueuer.
// Instead of queuing to Redis, it dispatches the task to a goroutine.
// Supports ProcessIn (delay) and MaxRetry options for parity with asynq.
func (e *SyncTaskExecutor) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	e.mu.RLock()
	handler, ok := e.handlers[task.Type()]
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("sync task executor: no handler registered for type %q", task.Type())
	}

	var delay time.Duration
	var timeout time.Duration
	maxRetry := 25 // asynq default
	maxRetrySet := false
	for _, opt := range opts {
		switch opt.Type() {
		case asynq.ProcessInOpt:
			if d, ok := opt.Value().(time.Duration); ok {
				delay = d
			}
		case asynq.MaxRetryOpt:
			if n, ok := opt.Value().(int); ok {
				maxRetry = n
				maxRetrySet = true
			}
		case asynq.TimeoutOpt:
			if d, ok := opt.Value().(time.Duration); ok {
				timeout = d
			}
		}
	}
	// Callers that explicitly pass MaxRetry(0) want no retries.
	// Without the flag we can't distinguish "not set" from "set to 0".
	if maxRetrySet && maxRetry < 0 {
		maxRetry = 0
	}

	taskID := uuid.New().String()
	info := &asynq.TaskInfo{
		ID:    taskID,
		Queue: "sync",
		Type:  task.Type(),
	}

	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}

		// Tag as a background worker execution so the per-model concurrency
		// governor throttles Lite-mode ingestion/enrichment LLM calls, mirroring
		// the asynq backgroundTaskMiddleware in the Redis path.
		ctx := types.WithBackgroundTask(context.Background())
		start := time.Now()
		logger.Infof(ctx, "[SyncTask] Executing task type=%s id=%s", task.Type(), taskID)

		var lastErr error
		for attempt := 0; attempt <= maxRetry; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(attempt) * 5 * time.Second
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
				logger.Infof(ctx, "[SyncTask] Retrying task type=%s id=%s attempt=%d/%d backoff=%s",
					task.Type(), taskID, attempt, maxRetry, backoff)
				time.Sleep(backoff)
			}

			attemptCtx := types.WithTaskRetryMetadata(ctx, attempt, maxRetry)
			// Queueing behind a Lite fan-out limiter is scheduling delay, not
			// handler execution. Start the task timeout only after a slot is
			// acquired so a busy graph/postprocess pool cannot consume a task's
			// entire timeout before its handler gets CPU time.
			release, acquireErr := e.acquireLimiter(attemptCtx, task.Type())
			if acquireErr != nil {
				lastErr = acquireErr
			} else {
				handlerCtx := attemptCtx
				cancelAttempt := func() {}
				if timeout > 0 {
					handlerCtx, cancelAttempt = context.WithTimeout(attemptCtx, timeout)
				}
				lastErr = handler(handlerCtx, task)
				cancelAttempt()
				release()
			}
			if lastErr == nil {
				logger.Infof(ctx, "[SyncTask] Task completed type=%s id=%s elapsed=%v",
					task.Type(), taskID, time.Since(start))
				return
			}
			// asynq.SkipRetry is a terminal control-flow error. Retrying it in
			// Lite mode used to re-run handlers after a datasource/sync log was
			// deliberately cancelled or deleted.
			if errors.Is(lastErr, asynq.SkipRetry) {
				logger.Infof(ctx, "[SyncTask] Task stopped without retry type=%s id=%s elapsed=%v err=%v",
					task.Type(), taskID, time.Since(start), lastErr)
				return
			}
		}

		logger.Errorf(ctx, "[SyncTask] Task failed (exhausted retries) type=%s id=%s elapsed=%v err=%v",
			task.Type(), taskID, time.Since(start), lastErr)
	}()

	return info, nil
}

func (e *SyncTaskExecutor) acquireLimiter(ctx context.Context, taskType string) (func(), error) {
	pool := litePoolForTaskType(taskType)
	limiter := e.limiters[pool]
	if limiter == nil {
		return func() {}, nil
	}
	select {
	case limiter <- struct{}{}:
		return func() { <-limiter }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func litePoolForTaskType(taskType string) string {
	queue, ok := types.QueueForTaskType(taskType)
	if !ok {
		return ""
	}
	for _, definition := range types.QueueDefinitions() {
		if definition.Name == queue {
			return definition.Pool
		}
	}
	return ""
}

type SyncTaskParams struct {
	dig.In

	Executor             *SyncTaskExecutor
	OctoBusiness         *octobusiness.Service
	KnowledgeService     interfaces.KnowledgeService
	KnowledgeBaseService interfaces.KnowledgeBaseService
	TagService           interfaces.KnowledgeTagService
	DataSourceService    interfaces.DataSourceService
	ChunkExtractor       interfaces.TaskHandler `name:"chunkExtractor"`
	DataTableSummary     interfaces.TaskHandler `name:"dataTableSummary"`
	ImageMultimodal      interfaces.TaskHandler `name:"imageMultimodal"`
	KnowledgePostProcess interfaces.TaskHandler `name:"knowledgePostProcess"`
	KnowledgeAutoTag     interfaces.TaskHandler `name:"knowledgeAutoTag"`
	WikiIngest           interfaces.TaskHandler `name:"wikiIngest"`
	TemporaryDocument    interfaces.TemporaryDocumentService
	MemoryService        interfaces.MemoryService
}

// RegisterSyncHandlers registers all task handlers on the SyncTaskExecutor.
// Used in Lite mode instead of RunAsynqServer.
func RegisterSyncHandlers(params SyncTaskParams) {
	params.Executor.RegisterHandler(octobusiness.TypeReportSend, params.OctoBusiness.ProcessReportTask)
	params.Executor.RegisterHandler(types.TypeChunkExtract, params.ChunkExtractor.Handle)
	params.Executor.RegisterHandler(types.TypeDataTableSummary, params.DataTableSummary.Handle)
	params.Executor.RegisterHandler(types.TypeDocumentProcess, params.KnowledgeService.ProcessDocument)
	params.Executor.RegisterHandler(types.TypeTemporaryDocumentProcess, params.TemporaryDocument.Process)
	params.Executor.RegisterHandler(types.TypeManualProcess, params.KnowledgeService.ProcessManualUpdate)
	params.Executor.RegisterHandler(types.TypeFAQImport, params.KnowledgeService.ProcessFAQImport)
	params.Executor.RegisterHandler(types.TypeQuestionGeneration, params.KnowledgeService.ProcessQuestionGeneration)
	params.Executor.RegisterHandler(types.TypeSummaryGeneration, params.KnowledgeService.ProcessSummaryGeneration)
	params.Executor.RegisterHandler(types.TypeKBClone, params.KnowledgeService.ProcessKBClone)
	params.Executor.RegisterHandler(types.TypeKnowledgeMove, params.KnowledgeService.ProcessKnowledgeMove)
	params.Executor.RegisterHandler(types.TypeKnowledgeListDelete, params.KnowledgeService.ProcessKnowledgeListDelete)
	params.Executor.RegisterHandler(types.TypeKnowledgeListReparse, params.KnowledgeService.ProcessKnowledgeListReparse)
	params.Executor.RegisterHandler(types.TypeIndexDelete, params.TagService.ProcessIndexDelete)
	params.Executor.RegisterHandler(types.TypeKBDelete, params.KnowledgeBaseService.ProcessKBDelete)
	params.Executor.RegisterHandler(types.TypeImageMultimodal, params.ImageMultimodal.Handle)
	params.Executor.RegisterHandler(types.TypeKnowledgePostProcess, params.KnowledgePostProcess.Handle)
	params.Executor.RegisterHandler(types.TypeKnowledgeAutoTag, params.KnowledgeAutoTag.Handle)
	params.Executor.RegisterHandler(types.TypeDataSourceSync, params.DataSourceService.ProcessSync)
	params.Executor.RegisterHandler(types.TypeWikiIngest, params.WikiIngest.Handle)
	params.Executor.RegisterHandler(types.TypeWikiFinalize, params.WikiIngest.Handle)
	params.Executor.RegisterHandler(types.TypeMemoryExtract, params.MemoryService.Handle)
	logger.Infof(context.Background(), "[SyncTask] All task handlers registered (Lite mode, no Redis)")
}
