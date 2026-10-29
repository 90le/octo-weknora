// Idle reclamation for the docker backend.
//
// Every other backend gets this for free. Cube and E2B are told an idle
// timeout at creation and the provider pauses or kills the sandbox when it
// elapses, which is why SessionBoundManager never reaps anything itself. The
// Docker daemon has no such concept: a container runs until something stops
// it. Without a sweep, one abandoned session pins its memory and CPU share on
// the host forever.
//
// The sweep needs to know when a sandbox was last used, and Docker exposes no
// exec timestamps. Labels cannot help either — they are immutable after
// create. So each exec touches an activity marker file inside the container
// (folded into the exec wrapper, costing no extra round-trip), and the sweep
// reads its mtime with a single HEAD /archive call per container.
//
// Idle containers are stopped rather than deleted: their files and session
// binding survive, and Connect starts the same container on the next use.

package sandbox

import (
	"context"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// dockerIdleTTLLabel records the TTL each container was created with, so a
// sweep triggered by one workspace config never applies its own TTL to a
// container belonging to another.
const dockerIdleTTLLabel = "com.weknora.sandbox.idle-ttl-seconds"

// dockerSweepMinInterval bounds how often one daemon is swept, no matter how
// many requests trigger it. Sweeping is a list plus one stat per container;
// cheap, but not free enough to run on every execution.
const dockerSweepMinInterval = time.Minute

// dockerSweepBudget bounds one sweep so a wedged daemon cannot leave a
// goroutine hanging around forever.
const dockerSweepBudget = 2 * time.Minute

// dockerSweepThrottle records the last sweep per daemon endpoint. It is
// process-wide because the adapter is rebuilt per request; per-instance state
// would throttle nothing.
var dockerSweepThrottle = struct {
	mu   sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

// dockerIdleSweeper reclaims containers that have not executed anything for
// longer than their TTL.
type dockerIdleSweeper struct {
	client   *DockerRemoteClient
	ttl      time.Duration
	scope    map[string]string
	scopeKey string

	// now is injected by tests.
	now func() time.Time
}

func newDockerIdleSweeper(cli *DockerRemoteClient, ttl time.Duration) *dockerIdleSweeper {
	return &dockerIdleSweeper{client: cli, ttl: ttl, now: time.Now}
}

// trigger runs a sweep in the background unless one ran recently. It detaches
// from the caller's context so finishing the request does not abort the sweep,
// and it never reports failures upward: reclaiming storage must not turn a
// working execution into an error.
func (s *dockerIdleSweeper) trigger(ctx context.Context) {
	if s == nil || s.client == nil {
		return
	}
	if !s.claimSweep() {
		return
	}
	go func() {
		// Drop the chat-trace parent: Engine API list/stat/delete from a
		// sweep must not appear as siblings of agent.round after the tool
		// that triggered the sweep has already finished its span.
		sweepCtx, cancel := context.WithTimeout(
			logger.CloneContextWithoutTrace(ctx), dockerSweepBudget,
		)
		defer cancel()
		if reclaimed, err := s.sweep(sweepCtx); err != nil {
			log.Printf("[sandbox] docker idle sweep failed: %v", err)
		} else if reclaimed > 0 {
			log.Printf("[sandbox] docker idle sweep reclaimed %d container(s)", reclaimed)
		}
	}()
}

// claimSweep reports whether this caller won the right to sweep now.
func (s *dockerIdleSweeper) claimSweep() bool {
	key := s.client.settings.Endpoint.key() + "\x00" + s.scopeKey
	now := s.now()
	dockerSweepThrottle.mu.Lock()
	defer dockerSweepThrottle.mu.Unlock()
	if last, ok := dockerSweepThrottle.last[key]; ok &&
		now.Sub(last) < dockerSweepMinInterval {
		return false
	}
	dockerSweepThrottle.last[key] = now
	return true
}

// sweep stops idle managed containers without removing their files.
func (s *dockerIdleSweeper) sweep(ctx context.Context) (int, error) {
	summaries, err := s.client.List(ctx, RemoteListFilter{Metadata: s.scope})
	if err != nil {
		return 0, err
	}
	reclaimed := 0
	for _, summary := range summaries {
		select {
		case <-ctx.Done():
			return reclaimed, ctx.Err()
		default:
		}
		if summary.State != RemoteStateRunning || !metadataMatches(summary.Metadata, s.scope) {
			continue
		}
		release, ok := s.client.tryReapContainer(summary.ID)
		if !ok {
			continue
		}
		stopped, err := s.stopIfIdle(ctx, summary)
		release()
		if err != nil {
			log.Printf("[sandbox] docker idle sweep: stop %s: %v", summary.ID, err)
			continue
		}
		if stopped {
			reclaimed++
		}
	}
	return reclaimed, nil
}

// stopIfIdle is called with the exclusive usage guard. Unknown state and any
// running Docker exec fail safe, including a PTY opened by another client.
func (s *dockerIdleSweeper) stopIfIdle(ctx context.Context, summary RemoteSandboxSummary) (bool, error) {
	if !s.isIdle(ctx, summary) {
		return false, nil
	}
	inspected, err := s.client.api.ContainerInspect(ctx, summary.ID, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	if inspected.Container.State == nil || inspected.Container.State.Status != "running" {
		return false, nil
	}
	for _, execID := range inspected.Container.ExecIDs {
		result, err := s.client.api.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return false, err
		}
		if result.Running {
			return false, nil
		}
	}
	// Docker execs can leave detached child processes behind. Only stop a
	// container whose process tree is exactly the idle init + sleep loop.
	top, err := s.client.api.ContainerTop(ctx, summary.ID, client.ContainerTopOptions{Arguments: []string{"-eo", "pid,ppid,comm"}})
	if err != nil {
		return false, err
	}
	if !dockerOnlyIdleProcesses(top) {
		return false, nil
	}
	if !s.isIdle(ctx, summary) {
		return false, nil
	}
	_, err = s.client.api.ContainerStop(ctx, summary.ID, client.ContainerStopOptions{})
	return err == nil, err
}

func dockerOnlyIdleProcesses(top client.ContainerTopResult) bool {
	pid, ppid, command := -1, -1, -1
	for i, title := range top.Titles {
		switch strings.ToUpper(strings.TrimSpace(title)) {
		case "PID":
			pid = i
		case "PPID":
			ppid = i
		case "COMMAND", "COMM", "CMD":
			command = i
		}
	}
	if pid < 0 || ppid < 0 || command < 0 || len(top.Processes) == 0 || len(top.Processes) > 2 {
		return false
	}
	var initPID, sleepPPID string
	for _, row := range top.Processes {
		if pid >= len(row) || ppid >= len(row) || command >= len(row) {
			return false
		}
		switch filepath.Base(row[command]) {
		case "docker-init", "tini":
			if initPID != "" {
				return false
			}
			initPID = row[pid]
		case "sleep":
			if sleepPPID != "" {
				return false
			}
			sleepPPID = row[ppid]
		default:
			return false
		}
	}
	if sleepPPID == "" {
		return false
	}
	return len(top.Processes) == 1 || (initPID != "" && sleepPPID == initPID)
}

// isIdle decides whether one container has gone unused for longer than the
// TTL it was created with.
func (s *dockerIdleSweeper) isIdle(ctx context.Context, summary RemoteSandboxSummary) bool {
	ttl := s.ttlFor(summary)
	if ttl <= 0 {
		return false
	}
	lastUsed := s.lastActivity(ctx, summary)
	if lastUsed.IsZero() {
		// Neither a marker nor a creation timestamp: treating that as idle
		// would risk deleting a sandbox that is merely unreadable right now.
		return false
	}
	return s.now().UTC().Sub(lastUsed) > ttl
}

// ttlFor prefers the TTL stamped on the container at creation over this
// client's own configuration.
func (s *dockerIdleSweeper) ttlFor(summary RemoteSandboxSummary) time.Duration {
	if raw, ok := summary.Metadata[dockerIdleTTLLabel]; ok {
		if seconds, err := strconv.Atoi(raw); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	return s.ttl
}

// lastActivity returns when the container last ran a command, falling back to
// when it started for a sandbox that has not executed anything yet.
//
// The marker lives inside the container and has to be writable by the account
// the execs run as, so its mtime is attacker-influenced: a script can
// `touch -d` it. A marker too far in the future or an unreadable marker is
// treated as unknown activity, so a possibly live session is not stopped.
func (s *dockerIdleSweeper) lastActivity(
	ctx context.Context,
	summary RemoteSandboxSummary,
) time.Time {
	stat, err := s.client.api.ContainerStatPath(ctx, summary.ID,
		client.ContainerStatPathOptions{Path: dockerActivityMarker})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return time.Time{}
	}
	if err == nil {
		if stat.Stat.Mtime.IsZero() {
			return time.Time{}
		}
		marker := stat.Stat.Mtime.UTC()
		if marker.After(s.now().UTC().Add(dockerActivityClockSkew)) {
			return time.Time{}
		} else {
			return marker
		}
	}
	return summary.StartedAt.UTC()
}

// dockerActivityClockSkew is how far ahead of WeKnora the daemon's clock may
// run before a marker is treated as forged. The marker is stamped by the
// daemon host while the comparison happens here, and the two are not
// necessarily the same machine.
const dockerActivityClockSkew = 2 * time.Minute
