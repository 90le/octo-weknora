package sandbox

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/moby/moby/client"
)

// dockerAdmissionQueues are process-local FIFO queues per daemon. Docker's
// admission plugin remains the authority across processes; this queue avoids
// a thundering herd and gives this process's waiters a predictable order.
var dockerAdmissionQueues sync.Map // endpoint key -> *dockerAdmissionQueue

// Allow the default 30-minute idle TTL and one sweep interval to free a slot.
// A caller's own earlier deadline or cancellation still takes precedence.
const (
	dockerAdmissionWait = 35 * time.Minute
	dockerAdmissionPoll = time.Second
)

type dockerAdmissionQueue struct {
	mu      sync.Mutex
	waiters []*dockerAdmissionWaiter
}

type dockerAdmissionWaiter struct{ ready chan struct{} }

func dockerQueueFor(key string) *dockerAdmissionQueue {
	q, _ := dockerAdmissionQueues.LoadOrStore(key, &dockerAdmissionQueue{})
	return q.(*dockerAdmissionQueue)
}

// enter waits its turn and removes cancelled waiters without leaking a slot.
func (q *dockerAdmissionQueue) enter(ctx context.Context) (func(), error) {
	w := &dockerAdmissionWaiter{ready: make(chan struct{})}
	q.mu.Lock()
	q.waiters = append(q.waiters, w)
	if len(q.waiters) == 1 {
		close(w.ready)
	}
	q.mu.Unlock()
	remove := func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		for i, item := range q.waiters {
			if item != w {
				continue
			}
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			if i == 0 && len(q.waiters) > 0 {
				close(q.waiters[0].ready)
			}
			break
		}
	}
	select {
	case <-ctx.Done():
		remove()
		return nil, ctx.Err()
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			remove()
			return nil, err
		}
		return remove, nil
	}
}

func (c *DockerRemoteClient) startQueued(ctx context.Context, id, op string) error {
	start := func(ctx context.Context) error {
		_, err := c.api.ContainerStart(ctx, id, client.ContainerStartOptions{})
		return err
	}
	if c.admission == nil {
		return dockerError(op, start(ctx))
	}
	queueCtx, cancel := context.WithTimeout(ctx, dockerAdmissionWait)
	defer cancel()
	release, err := c.admission.enter(queueCtx)
	if err != nil {
		return dockerError(op, err)
	}
	defer release()
	for {
		err = start(queueCtx)
		if err == nil || !isOctoDockerCapacityError(err) {
			return dockerError(op, err)
		}
		// Trigger only the existing config's reaper; an unsaved probe must
		// never reclaim containers belonging to an existing config.
		c.sweepInBackground(queueCtx)
		timer := time.NewTimer(dockerAdmissionPoll)
		select {
		case <-queueCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return errors.Join(dockerError(op, err), queueCtx.Err())
		case <-timer.C:
		}
	}
}
