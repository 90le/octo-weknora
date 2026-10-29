package sandbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/stretchr/testify/require"
)

func TestDockerAdmissionQueueCancellationAndFIFO(t *testing.T) {
	q := &dockerAdmissionQueue{}
	release, err := q.enter(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := q.enter(ctx); done <- err }()
	require.Eventually(t, func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.waiters) == 2
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	release()
	last, err := q.enter(context.Background())
	require.NoError(t, err)
	last()
	q.mu.Lock()
	defer q.mu.Unlock()
	require.Empty(t, q.waiters)
}

func TestDockerAdmissionQueueServesWaitersInOrder(t *testing.T) {
	q := &dockerAdmissionQueue{}
	first, err := q.enter(context.Background())
	require.NoError(t, err)
	ready := make(chan int, 2)
	releaseOne := make(chan struct{})
	for i := 1; i <= 2; i++ {
		id := i
		go func() {
			release, err := q.enter(context.Background())
			if err != nil {
				return
			}
			ready <- id
			if id == 1 {
				<-releaseOne
			}
			release()
		}()
		require.Eventually(t, func() bool {
			q.mu.Lock()
			defer q.mu.Unlock()
			return len(q.waiters) == id+1
		}, time.Second, time.Millisecond)
	}
	first()
	require.Equal(t, 1, <-ready)
	select {
	case v := <-ready:
		t.Fatalf("waiter %d ran before its turn", v)
	default:
	}
	close(releaseOne)
	require.Equal(t, 2, <-ready)
}

func TestDockerAdmissionWaitsForCapacityAndRejectsOtherErrors(t *testing.T) {
	engine := newFakeDockerEngine()
	var attempts atomic.Int32
	engine.startHook = func(string) error {
		if attempts.Add(1) == 1 {
			return cerrdefs.ErrPermissionDenied.WithMessage(
				"authorization denied by plugin octo-sandbox-limit: Octo sandbox capacity reached: maximum 2 running containers")
		}
		return nil
	}
	c := newTestDockerClient(t, engine)
	c.admission = &dockerAdmissionQueue{}
	require.NoError(t, c.startQueued(context.Background(), "queued", "Create"))
	require.EqualValues(t, 2, attempts.Load())
	engine.startHook = func(string) error { return errors.New("unrelated failure") }
	require.ErrorContains(t, c.startQueued(context.Background(), "other", "Create"), "unrelated failure")
}

func TestDockerAdmissionCancellationDoesNotKeepQueueSlot(t *testing.T) {
	engine := newFakeDockerEngine()
	engine.startHook = func(string) error {
		return cerrdefs.ErrPermissionDenied.WithMessage(
			"authorization denied by plugin octo-sandbox-limit: Octo sandbox capacity reached: full")
	}
	c := newTestDockerClient(t, engine)
	c.admission = &dockerAdmissionQueue{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.startQueued(ctx, "waiting", "Create") }()
	require.Eventually(t, func() bool {
		c.admission.mu.Lock()
		defer c.admission.mu.Unlock()
		return len(c.admission.waiters) == 1
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	c.admission.mu.Lock()
	defer c.admission.mu.Unlock()
	require.Empty(t, c.admission.waiters)
}

func TestDockerQueuedCreateCancellationCleansItsUnstartedContainer(t *testing.T) {
	engine := newFakeDockerEngine()
	engine.imagePresent["weknora/sandbox:test"] = true
	tried := make(chan struct{}, 1)
	engine.startHook = func(string) error {
		select {
		case tried <- struct{}{}:
		default:
		}
		return cerrdefs.ErrPermissionDenied.WithMessage(
			"authorization denied by plugin octo-sandbox-limit: Octo sandbox capacity reached: full")
	}
	c := newTestDockerClient(t, engine)
	c.admission = &dockerAdmissionQueue{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Create(ctx, RemoteCreateRequest{TemplateID: "weknora/sandbox:test"}); done <- err }()
	<-tried
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, []string{"container-1"}, engine.removed)
}
