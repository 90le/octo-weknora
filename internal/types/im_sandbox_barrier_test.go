package types

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestIMSandboxBarrierWaitsAndRetriesUnconfirmedStop(t *testing.T) {
	b := NewIMSandboxTurnBarrier()
	track, done, err := b.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	track("container", func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("stop unconfirmed")
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = b.SealAndStop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active operation must block sealing: %v", err)
	}
	if _, _, err = b.Begin(); err == nil {
		t.Fatal("sealed barrier accepted a new operation")
	}
	done()
	done()
	if err = b.SealAndStop(context.Background()); err == nil {
		t.Fatal("unconfirmed stop was accepted")
	}
	if err = b.SealAndStop(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatalf("stop was not retried: calls=%d err=%v", calls.Load(), err)
	}
}
