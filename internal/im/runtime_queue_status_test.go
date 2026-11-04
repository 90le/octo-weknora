package im

import "testing"

func TestRuntimeQueueSnapshotUsesLiveQueue(t *testing.T) {
	q := newQAQueue(2, 8, 3, 0, nil, nil)
	q.queue = append(q.queue, &qaRequest{})
	q.activeWorkers.Store(1)
	s := &Service{qaQueue: q}
	m, workers, capacity, ok := s.RuntimeQueueSnapshot()
	if !ok || m.Depth != 1 || m.ActiveWorkers != 1 || workers != 2 || capacity != 8 {
		t.Fatalf("unexpected snapshot: %+v %d %d %v", m, workers, capacity, ok)
	}
}
