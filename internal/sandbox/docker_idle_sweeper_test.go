package sandbox

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

// newSweeperFixture wires a sweeper over a fake daemon holding the given
// containers, with the activity marker of each one set from markers.
func newSweeperFixture(
	t *testing.T,
	ttl time.Duration,
	now time.Time,
	containers []container.Summary,
) (*dockerIdleSweeper, *fakeDockerEngine) {
	t.Helper()
	engine := newFakeDockerEngine()
	engine.list = containers
	for _, item := range containers {
		engine.inspect[item.ID] = container.InspectResponse{ID: item.ID, State: &container.State{Status: item.State}}
	}
	docker := newTestDockerClient(t, engine)
	sweeper := newDockerIdleSweeper(docker, ttl)
	sweeper.now = func() time.Time { return now }
	return sweeper, engine
}

func TestDockerIdleSweeperReclaimsOnlyIdleContainers(t *testing.T) {
	now := time.Now().UTC()
	sweeper, engine := newSweeperFixture(t, 30*time.Minute, now, []container.Summary{
		{
			ID: "busy", State: "running", Created: now.Add(-4 * time.Hour).Unix(),
			Labels: map[string]string{dockerManagedLabel: "true"},
		},
		{
			ID: "idle", State: "running", Created: now.Add(-4 * time.Hour).Unix(),
			Labels: map[string]string{dockerManagedLabel: "true"},
		},
	})
	// The marker is what every exec touches; its mtime is the only record of
	// when a sandbox was last used.
	engine.statResult[dockerActivityMarker] = container.PathStat{Mtime: now.Add(-2 * time.Minute)}

	reclaimed, err := sweeper.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, reclaimed, "a recently used sandbox must survive")
	require.Empty(t, engine.stopped)

	engine.statResult[dockerActivityMarker] = container.PathStat{Mtime: now.Add(-90 * time.Minute)}
	reclaimed, err = sweeper.sweep(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, reclaimed)
	require.ElementsMatch(t, []string{"busy", "idle"}, engine.stopped)
	require.Empty(t, engine.removed, "idle workspaces must not be deleted")
}

// A sandbox that has never executed anything has no marker, so its creation
// time decides. Without this a freshly created container would look infinitely
// idle and be deleted out from under the session that just created it.
func TestDockerIdleSweeperFallsBackToCreationTime(t *testing.T) {
	now := time.Now().UTC()
	sweeper, engine := newSweeperFixture(t, 30*time.Minute, now, []container.Summary{
		{
			ID: "fresh", State: "running", Created: now.Add(-time.Minute).Unix(),
			Labels: map[string]string{dockerManagedLabel: "true"},
		},
	})

	reclaimed, err := sweeper.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, reclaimed)
	require.Empty(t, engine.stopped)
}

// The marker has to be writable by the account the execs run as, so a script
// can backdate or postdate it. Postdating is the dangerous direction: a
// single `touch -d 2099-01-01` would otherwise exempt the container from
// reclamation forever.
func TestDockerIdleSweeperRejectsFutureActivityMarker(t *testing.T) {
	now := time.Now().UTC()
	sweeper, engine := newSweeperFixture(t, 30*time.Minute, now, []container.Summary{
		{
			ID: "forged", State: "running", Created: now.Add(-4 * time.Hour).Unix(),
			Labels: map[string]string{dockerManagedLabel: "true"},
		},
	})
	engine.statResult[dockerActivityMarker] = container.PathStat{
		Mtime: now.Add(100 * 365 * 24 * time.Hour),
	}

	reclaimed, err := sweeper.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, reclaimed, "uncertain activity must not stop a live workspace")
	require.Empty(t, engine.stopped)
}

// A daemon on another host may run slightly ahead of WeKnora. That must not be
// mistaken for tampering, or every sandbox on that daemon would be judged by
// its start time alone and reclaimed mid-session.
func TestDockerIdleSweeperToleratesSmallClockSkew(t *testing.T) {
	now := time.Now().UTC()
	sweeper, engine := newSweeperFixture(t, 30*time.Minute, now, []container.Summary{
		{
			ID: "skewed", State: "running", Created: now.Add(-4 * time.Hour).Unix(),
			Labels: map[string]string{dockerManagedLabel: "true"},
		},
	})
	engine.statResult[dockerActivityMarker] = container.PathStat{
		Mtime: now.Add(dockerActivityClockSkew / 2),
	}

	reclaimed, err := sweeper.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, reclaimed)
	require.Empty(t, engine.stopped)
}

// Listing plus one stat per container is slow enough on a busy daemon that a
// session can be resumed in between. Deleting then would destroy a sandbox the
// user is working in, so the decision is re-taken right before the delete.
func TestDockerIdleSweeperRechecksActivityBeforeDeleting(t *testing.T) {
	now := time.Now().UTC()
	sweeper, engine := newSweeperFixture(t, 30*time.Minute, now, []container.Summary{
		{
			ID: "resumed", State: "running", Created: now.Add(-4 * time.Hour).Unix(),
			Labels: map[string]string{dockerManagedLabel: "true"},
		},
	})
	calls := 0
	engine.statHook = func(path string) (container.PathStat, bool) {
		if path != dockerActivityMarker {
			return container.PathStat{}, false
		}
		calls++
		if calls == 1 {
			return container.PathStat{Mtime: now.Add(-2 * time.Hour)}, true
		}
		return container.PathStat{Mtime: now.Add(-time.Second)}, true
	}

	reclaimed, err := sweeper.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, reclaimed)
	require.Empty(t, engine.stopped, "a sandbox used since the listing must survive")
	require.Equal(t, 2, calls)
}

// Each container carries the TTL it was created with, so a sweep triggered by
// one workspace config cannot apply its own TTL to another config's sandboxes.
func TestDockerIdleSweeperHonoursPerContainerTTL(t *testing.T) {
	now := time.Now().UTC()
	sweeper, engine := newSweeperFixture(t, time.Minute, now, []container.Summary{
		{
			ID: "long-ttl", State: "running", Created: now.Add(-2 * time.Hour).Unix(),
			Labels: map[string]string{
				dockerManagedLabel: "true",
				dockerIdleTTLLabel: "86400",
			},
		},
	})
	engine.statResult[dockerActivityMarker] = container.PathStat{Mtime: now.Add(-2 * time.Hour)}

	reclaimed, err := sweeper.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, reclaimed)
	require.Empty(t, engine.stopped)
}

// The sweep runs off the back of ordinary requests, so it must not turn into a
// list-plus-stat storm when a workspace is busy.
func TestDockerIdleSweeperThrottlesPerDaemon(t *testing.T) {
	now := time.Now().UTC()
	sweeper, _ := newSweeperFixture(t, time.Minute, now, nil)
	dockerSweepThrottle.mu.Lock()
	delete(dockerSweepThrottle.last, sweeper.client.settings.Endpoint.key()+"\x00")
	dockerSweepThrottle.mu.Unlock()

	require.True(t, sweeper.claimSweep(), "the first caller sweeps")
	require.False(t, sweeper.claimSweep(), "a second caller in the same minute does not")

	sweeper.now = func() time.Time { return now.Add(2 * dockerSweepMinInterval) }
	require.True(t, sweeper.claimSweep(), "the throttle expires")
}

// A container the daemon refuses to delete must not abort the pass: the rest
// of the idle set still has to be reclaimed.
func TestDockerIdleSweeperContinuesAfterStopFailure(t *testing.T) {
	now := time.Now().UTC()
	sweeper, engine := newSweeperFixture(t, time.Minute, now, []container.Summary{
		{
			ID: "wedged", State: "running", Created: now.Add(-2 * time.Hour).Unix(),
			Labels: map[string]string{dockerManagedLabel: "true"},
		},
		{
			ID: "reclaimable", State: "running", Created: now.Add(-2 * time.Hour).Unix(),
			Labels: map[string]string{dockerManagedLabel: "true"},
		},
	})
	engine.stopErr = context.DeadlineExceeded

	reclaimed, err := sweeper.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, reclaimed)
	require.Len(t, engine.stopped, 2, "every candidate is attempted")
}

func TestDockerIdleSweeperDoesNotStopActiveOrForeignSessions(t *testing.T) {
	now := time.Now().UTC()
	owned := map[string]string{dockerManagedLabel: "true", remoteMetadataTenantID: "7", remoteMetadataConfigID: "cfg"}
	foreign := map[string]string{dockerManagedLabel: "true", remoteMetadataTenantID: "8", remoteMetadataConfigID: "cfg"}
	s, engine := newSweeperFixture(t, time.Minute, now, []container.Summary{
		{ID: "in-use", State: "running", Created: now.Add(-time.Hour).Unix(), Labels: owned},
		{ID: "running-exec", State: "running", Created: now.Add(-time.Hour).Unix(), Labels: owned},
		{ID: "foreign", State: "running", Created: now.Add(-time.Hour).Unix(), Labels: foreign},
	})
	s.scope = map[string]string{remoteMetadataTenantID: "7", remoteMetadataConfigID: "cfg"}
	engine.statResult[dockerActivityMarker] = container.PathStat{Mtime: now.Add(-time.Hour)}
	busy := engine.inspect["running-exec"]
	busy.ExecIDs = []string{"exec-1"}
	engine.inspect["running-exec"] = busy
	engine.execInspectRunning.Store(1)
	release := s.client.useContainer("in-use")
	n, err := s.sweep(context.Background())
	release()
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, engine.stopped)
	require.Empty(t, engine.removed)
}

func TestDockerIdleSweeperSkipsUnreadableActivity(t *testing.T) {
	now := time.Now().UTC()
	s, engine := newSweeperFixture(t, time.Minute, now, []container.Summary{
		{ID: "unknown", State: "running", Created: now.Add(-time.Hour).Unix(), Labels: map[string]string{dockerManagedLabel: "true"}},
	})
	engine.statErr = context.DeadlineExceeded
	n, err := s.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, engine.stopped)
}

func TestDockerIdleSweeperSkipsZeroActivityTimestamp(t *testing.T) {
	now := time.Now().UTC()
	s, engine := newSweeperFixture(t, time.Minute, now, []container.Summary{
		{ID: "unknown", State: "running", Created: now.Add(-time.Hour).Unix(), Labels: map[string]string{dockerManagedLabel: "true"}},
	})
	engine.statResult[dockerActivityMarker] = container.PathStat{}
	n, err := s.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, engine.stopped)
}

func TestDockerIdleSweeperPreservesDetachedProcesses(t *testing.T) {
	now := time.Now().UTC()
	s, engine := newSweeperFixture(t, time.Minute, now, []container.Summary{
		{ID: "worker", State: "running", Created: now.Add(-time.Hour).Unix(), Labels: map[string]string{dockerManagedLabel: "true"}},
	})
	engine.statResult[dockerActivityMarker] = container.PathStat{Mtime: now.Add(-time.Hour)}
	engine.topResult.Processes = append(engine.topResult.Processes, []string{"3", "1", "python3"})
	n, err := s.sweep(context.Background())
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, engine.stopped)
}
