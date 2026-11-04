package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// Separate slots bound the number of abandoned probes if a dependency ignores
// cancellation. Status polling never queues work behind a slow dependency.
var (
	runtimeQueueProbeSlot   = make(chan struct{}, 1)
	runtimeIMProbeSlot      = make(chan struct{}, 1)
	runtimeSandboxProbeSlot = make(chan struct{}, 1)
)

type runtimeProbeResult[T any] struct {
	value T
	err   error
}

func runtimeProbe[T any](ctx context.Context, slot chan struct{}, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	select {
	case slot <- struct{}{}:
	default:
		return zero, errRuntimeBusy
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	result := make(chan runtimeProbeResult[T], 1)
	go func() {
		defer func() { <-slot }()
		value, err := fn(probeCtx)
		result <- runtimeProbeResult[T]{value, err}
	}()
	defer cancel()
	select {
	case r := <-result:
		return r.value, r.err
	case <-probeCtx.Done():
		return zero, probeCtx.Err()
	}
}

var errRuntimeBusy = errors.New("probe busy")

// Only stable, allowlisted categories leave the server; never return error text.
func runtimeFailure(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	if errors.Is(err, errRuntimeBusy) {
		return "busy"
	}
	var remote *sandbox.RemoteError
	if errors.As(err, &remote) {
		switch remote.Kind {
		case sandbox.RemoteErrorKindTimeout, sandbox.RemoteErrorKindUnavailable, sandbox.RemoteErrorKindAuthentication,
			sandbox.RemoteErrorKindCapacity, sandbox.RemoteErrorKindUnsupported, sandbox.RemoteErrorKindInvalidRequest:
			return string(remote.Kind)
		}
	}
	return "unavailable"
}

type runtimeQueueStatus struct {
	Available bool              `json:"available"`
	Scope     string            `json:"scope"`
	Queues    []types.QueueStat `json:"queues"`
	Failure   string            `json:"failure,omitempty"`
}
type runtimeIMStatus struct {
	SandboxPreparationFailures []im.SandboxPreparationFailureCount `json:"sandbox_preparation_failures"`
	Available                  bool                                `json:"available"`
	Scope                      string                              `json:"scope"`
	Depth                      *int                                `json:"depth"`
	Active                     *int64                              `json:"active"`
	Workers                    *int                                `json:"workers"`
	QueueCapacity              *int                                `json:"queue_capacity"`
	Rejected                   *int64                              `json:"rejected"`
	TimedOut                   *int64                              `json:"timed_out"`
	Failure                    string                              `json:"failure,omitempty"`
}
type runtimeDockerStatus struct {
	Available bool   `json:"available"`
	Scope     string `json:"scope"`
	Running   *int   `json:"running"`
	Capacity  *int   `json:"capacity"`
	Failure   string `json:"failure,omitempty"`
}
type runtimeSupplement struct {
	IM     runtimeIMStatus     `json:"im"`
	Docker runtimeDockerStatus `json:"docker"`
}
type runtimeStatusResponse struct {
	Timestamp   int64              `json:"timestamp"`
	Queues      runtimeQueueStatus `json:"queues"`
	OctoRuntime runtimeSupplement  `json:"octo_runtime"`
}
type runtimeDockerCounter interface {
	RuntimeDockerCount(context.Context, uint64, string) (int, error)
}

func (h *SystemHandler) runtimeSupplement(c *gin.Context) (runtimeSupplement, bool) {
	tenant, config := c.Query("tenant_id"), c.Query("sandbox_config_id")
	var tenantID uint64
	if tenant != "" || config != "" {
		var err error
		tenantID, err = strconv.ParseUint(tenant, 10, 64)
		if err != nil || tenantID == 0 || config == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id and sandbox_config_id are required together"})
			return runtimeSupplement{}, false
		}
	}
	resp := runtimeSupplement{
		IM: runtimeIMStatus{
			Scope: "process", SandboxPreparationFailures: im.RuntimeSandboxPreparationFailures(),
		},
		Docker: runtimeDockerStatus{Scope: "tenant_config", Failure: "not_selected"},
	}
	if h.imRuntime != nil {
		type snapshot struct {
			depth              int
			active             int64
			workers, capacity  int
			rejected, timedOut int64
			available          bool
		}
		m, err := runtimeProbe(c.Request.Context(), runtimeIMProbeSlot, func(context.Context) (snapshot, error) {
			metrics, workers, capacity, ok := h.imRuntime.RuntimeQueueSnapshot()
			return snapshot{
				metrics.Depth, metrics.ActiveWorkers, workers, capacity,
				metrics.TotalRejected, metrics.TotalTimeout, ok,
			}, nil
		})
		if err != nil {
			resp.IM.Failure = runtimeFailure(err)
		} else if m.available {
			resp.IM.Available = true
			resp.IM.Depth = &m.depth
			resp.IM.Active = &m.active
			resp.IM.Workers = &m.workers
			resp.IM.QueueCapacity = &m.capacity
			resp.IM.Rejected = &m.rejected
			resp.IM.TimedOut = &m.timedOut
		} else {
			resp.IM.Failure = "unsupported"
		}
	} else {
		resp.IM.Failure = "unsupported"
	}
	if tenantID != 0 {
		counter, ok := h.sandboxConfigSvc.(runtimeDockerCounter)
		if !ok {
			resp.Docker.Failure = "unsupported"
		} else {
			count, err := runtimeProbe(
				c.Request.Context(), runtimeSandboxProbeSlot, func(ctx context.Context) (int, error) {
					return counter.RuntimeDockerCount(ctx, tenantID, config)
				},
			)
			if err != nil {
				resp.Docker.Failure = runtimeFailure(err)
			} else {
				resp.Docker.Available = true
				resp.Docker.Running = &count
				resp.Docker.Failure = ""
			}
		}
	}
	return resp, true
}

// GetRuntimeStatus godoc
// @Summary Read a bounded, aggregate-only runtime snapshot
// @Description System administrators and platform runtime-read keys only.
// @Description Optional tenant_id and sandbox_config_id select one Docker config; null capacity means unknown.
// @Tags System Admin
// @Produce json
// @Router /system/admin/runtime/status [get]
func (h *SystemHandler) GetRuntimeStatus(c *gin.Context) {
	supplement, ok := h.runtimeSupplement(c)
	if !ok {
		return
	}
	resp := runtimeStatusResponse{
		Timestamp:   time.Now().Unix(),
		Queues:      runtimeQueueStatus{Scope: "cluster", Queues: []types.QueueStat{}},
		OctoRuntime: supplement,
	}
	if h.taskInspector != nil {
		type snapshot struct {
			rows      []types.QueueStat
			supported bool
		}
		snap, err := runtimeProbe(
			c.Request.Context(), runtimeQueueProbeSlot, func(ctx context.Context) (snapshot, error) {
				rows, supported, err := h.taskInspector.QueueStats(ctx)
				return snapshot{rows, supported}, err
			},
		)
		if err != nil {
			resp.Queues.Failure = runtimeFailure(err)
		} else if !snap.supported {
			resp.Queues.Failure = "unsupported"
		} else {
			resp.Queues.Available = true
			if snap.rows != nil {
				resp.Queues.Queues = snap.rows
			}
		}
	} else {
		resp.Queues.Failure = "unsupported"
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, resp)
}
