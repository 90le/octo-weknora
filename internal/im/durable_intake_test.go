package im

import (
	"context"
	"errors"
	"testing"
	"time"
)

type durableDedupProbe struct {
	Adapter
	durable        bool
	authorizations int
}

func (a *durableDedupProbe) DurableIntake() bool { return a.durable }
func (a *durableDedupProbe) AuthorizeExecution(context.Context, *IMChannel, *IncomingMessage) (*ExecutionScope, error) {
	a.authorizations++
	return nil, ErrScopeDenied
}

func TestDurableIntakeRecoveryBypassesOnlyVolatileDedup(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary receiver", true: "durable receiver"}[durable], func(t *testing.T) {
			a := &durableDedupProbe{durable: durable}
			s := &Service{channels: map[string]*channelState{"channel": {Adapter: a, Channel: &IMChannel{ID: "channel", Platform: "octo"}}}}
			s.processedMsgs.Store("already-cached", time.Now())
			err := s.HandleMessage(context.Background(), &IncomingMessage{Platform: "octo", MessageID: "already-cached", Content: "recover persisted input"}, "channel")
			if durable {
				if !errors.Is(err, ErrScopeDenied) || a.authorizations != 1 {
					t.Fatalf("durable recovery swallowed or authorization skipped: err=%v calls=%d", err, a.authorizations)
				}
			} else if err != nil || a.authorizations != 0 {
				t.Fatalf("ordinary receiver lost volatile dedup: err=%v calls=%d", err, a.authorizations)
			}
		})
	}
}
