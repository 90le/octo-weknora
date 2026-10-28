package router

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/middleware/asynqdl"
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
	// onFinalFailure mirrors the asynq dead-letter callback: it runs once a
	// task exhausts its retries, so a document whose task gave up is marked
	// failed instead of spinning until the housekeeping sweep.
	onFinalFailure func(context.Context, *asynq.Task, error)
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

// SetFinalFailureHook installs the callback run after a task's last failed
// attempt. It receives the error of that attempt, including asynq.SkipRetry.
func (e *SyncTaskExecutor) SetFinalFailureHook(fn func(context.Context, *asynq.Task, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onFinalFailure = fn
}

// syncTaskOptions is the subset of asynq options the executor honours.
type syncTaskOptions struct {
	delay    time.Duration
	maxRetry int
	timeout  time.Duration
	deadline time.Time
}

// resolveSyncTaskOptions merges the options given to asynq.NewTask with the
// ones given to Enqueue, the latter winning, as asynq.Client does.
//
// Unlike asynq, a task with neither Timeout nor Deadline gets no default
// timeout: Lite has always run those unbounded (large imports on a laptop),
// and the document pipeline sets its own timeouts explicitly.
func resolveSyncTaskOptions(task *asynq.Task, opts []asynq.Option) syncTaskOptions {
	resolved := syncTaskOptions{maxRetry: 25} // asynq default
	all := append(append([]asynq.Option{}, asynqTaskOptions(task)...), opts...)
	for _, opt := range all {
		switch opt.Type() {
		case asynq.ProcessInOpt:
			if d, ok := opt.Value().(time.Duration); ok {
				resolved.delay = d
			}
		case asynq.ProcessAtOpt:
			if at, ok := opt.Value().(time.Time); ok {
				resolved.delay = time.Until(at)
			}
		case asynq.MaxRetryOpt:
			if n, ok := opt.Value().(int); ok {
				resolved.maxRetry = n
			}
		case asynq.TimeoutOpt:
			if d, ok := opt.Value().(time.Duration); ok {
				resolved.timeout = d
			}
		case asynq.DeadlineOpt:
			if at, ok := opt.Value().(time.Time); ok {
				resolved.deadline = at
			}
		}
	}
	if resolved.maxRetry < 0 {
		resolved.maxRetry = 0
	}
	return resolved
}

// asynqTaskOptions returns the options passed to asynq.NewTask. asynq keeps
// them in an unexported field and only merges them inside Client.Enqueue, so
// an executor standing in for the client has to read the field itself.
// Without this, every Lite task ran with 25 retries and no timeout, since
// callers put MaxRetry / Timeout on NewTask. TestAsynqTaskOptionsReadsNewTaskOptions
// breaks if an asynq upgrade renames the field.
func asynqTaskOptions(task *asynq.Task) []asynq.Option {
	if task == nil {
		return nil
	}
	field := reflect.ValueOf(task).Elem().FieldByName("opts")
	if !field.IsValid() || field.Type() != reflect.TypeOf([]asynq.Option(nil)) {
		return nil
	}
	return *(*[]asynq.Option)(unsafe.Pointer(field.UnsafeAddr()))
}

// attemptContext bounds one attempt the way asynq does: by the task's
// Timeout, and by its Deadline when that comes first.
func (o syncTaskOptions) attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	var deadline time.Time
	if o.timeout > 0 {
		deadline = time.Now().Add(o.timeout)
	}
	if !o.deadline.IsZero() && (deadline.IsZero() || o.deadline.Before(deadline)) {
		deadline = o.deadline
	}
	if deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}

// Enqueue satisfies interfaces.TaskEnqueuer.
// Instead of queuing to Redis, it dispatches the task to a goroutine.
// Supports ProcessIn / ProcessAt (delay), MaxRetry, Timeout and Deadline for
// parity with asynq, whether they were given here or to asynq.NewTask.
func (e *SyncTaskExecutor) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	e.mu.RLock()
	handler, ok := e.handlers[task.Type()]
	onFinalFailure := e.onFinalFailure
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("sync task executor: no handler registered for type %q", task.Type())
	}

	options := resolveSyncTaskOptions(task, opts)
	maxRetry := options.maxRetry

	taskID := uuid.New().String()
	info := &asynq.TaskInfo{
		ID:    taskID,
		Queue: "sync",
		Type:  task.Type(),
	}

	go func() {
		if options.delay > 0 {
			time.Sleep(options.delay)
		}

		// Tag as a background worker execution so the per-model concurrency
		// governor throttles Lite-mode ingestion/enrichment LLM calls, mirroring
		// the asynq backgroundTaskMiddleware in the Redis path.
		ctx := types.WithBackgroundTask(context.Background())
		start := time.Now()
		logger.Infof(ctx, "[SyncTask] Executing task type=%s id=%s", task.Type(), taskID)

		var lastErr error
		var attemptCtx context.Context
		for attempt := 0; attempt <= maxRetry; attempt++ {
			if attempt > 0 {
				// Lite has only this short in-process backoff. Explicit future
				// GitHub Retry-After hints are terminated by DataSourceService as
				// durable source cooldowns (SkipRetry), so they never sleep here
				// for 5–30 seconds and rapidly reissue the provider request.
				backoff := time.Duration(attempt) * 5 * time.Second
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
				logger.Infof(ctx, "[SyncTask] Retrying task type=%s id=%s attempt=%d/%d backoff=%s",
					task.Type(), taskID, attempt, maxRetry, backoff)
				time.Sleep(backoff)
			}

			attemptCtx = types.WithTaskRetryMetadata(ctx, attempt, maxRetry)
			// Queueing behind a Lite fan-out limiter is scheduling delay, not
			// handler execution. Start the task timeout only after a slot is
			// acquired so a busy graph/postprocess pool cannot consume a task's
			// entire timeout before its handler gets CPU time.
			release, acquireErr := e.acquireLimiter(attemptCtx, task.Type())
			if acquireErr != nil {
				lastErr = acquireErr
			} else {
				var cancel context.CancelFunc
				attemptCtx, cancel = options.attemptContext(attemptCtx)
				// An unrecovered panic here would take down the whole process.
				lastErr = asynqdl.CallRecovered(attemptCtx, task, handler)
				cancel()
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
				// Terminal, but still a failed task: the caller that owns the
				// durable status may rely on the final-failure hook to record
				// it, so fall through instead of returning early.
				break
			}
		}

		if errors.Is(lastErr, asynq.SkipRetry) {
			logger.Infof(ctx, "[SyncTask] Task failed without retry type=%s id=%s elapsed=%v err=%v",
				task.Type(), taskID, time.Since(start), lastErr)
		} else {
			logger.Errorf(ctx, "[SyncTask] Task failed (exhausted retries) type=%s id=%s elapsed=%v err=%v",
				task.Type(), taskID, time.Since(start), lastErr)
		}
		if onFinalFailure != nil {
			// The attempt's own context may be past its deadline; the
			// callback only writes status, so give it a fresh bound.
			cbCtx, cancel := context.WithTimeout(context.WithoutCancel(attemptCtx), 30*time.Second)
			defer cancel()
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Errorf(ctx, "[SyncTask] final-failure hook panicked for type=%s id=%s: %v",
							task.Type(), taskID, r)
					}
				}()
				onFinalFailure(cbCtx, task, lastErr)
			}()
		}
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
	SpanTracker          service.SpanTracker
	ChunkExtractor       interfaces.TaskHandler `name:"chunkExtractor"`
	DataTableSummary     interfaces.TaskHandler `name:"dataTableSummary"`
	ImageMultimodal      interfaces.TaskHandler `name:"imageMultimodal"`
	KnowledgePostProcess interfaces.TaskHandler `name:"knowledgePostProcess"`
	KnowledgeAutoTag     interfaces.TaskHandler `name:"knowledgeAutoTag"`
	KnowledgeBaseProfile interfaces.TaskHandler `name:"knowledgeBaseProfile"`
	WikiIngest           interfaces.TaskHandler `name:"wikiIngest"`
	TemporaryDocument    interfaces.TemporaryDocumentService
	MemoryService        interfaces.MemoryService
}

// RegisterSyncHandlers registers all task handlers on the SyncTaskExecutor.
// Used in Lite mode instead of RunAsynqServer.
func RegisterSyncHandlers(params SyncTaskParams) {
	params.Executor.RegisterHandler(octobusiness.TypeReportSend, params.OctoBusiness.ProcessReportTask)
	// Same callback the asynq dead-letter middleware runs in standard mode.
	if failer := newDeadLetterKnowledgeFailer(params.KnowledgeService, params.SpanTracker); failer != nil {
		params.Executor.SetFinalFailureHook(failer)
	}
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
	params.Executor.RegisterHandler(types.TypeKnowledgeBaseProfile, params.KnowledgeBaseProfile.Handle)
	params.Executor.RegisterHandler(types.TypeDataSourceSync, params.DataSourceService.ProcessSync)
	params.Executor.RegisterHandler(types.TypeDataSourceRestartRecovery, params.DataSourceService.ProcessRestartInterruptedRecovery)
	params.Executor.RegisterHandler(types.TypeWikiIngest, params.WikiIngest.Handle)
	params.Executor.RegisterHandler(types.TypeWikiFinalize, params.WikiIngest.Handle)
	params.Executor.RegisterHandler(types.TypeMemoryExtract, params.MemoryService.Handle)
	logger.Infof(context.Background(), "[SyncTask] All task handlers registered (Lite mode, no Redis)")
}
