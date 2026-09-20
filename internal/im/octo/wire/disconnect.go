package wire

import (
	"bytes"
	"fmt"
)

// DisconnectError retains only the numeric reason, never the server's arbitrary
// text. Wire layout and reason values follow WuKongIMGoProto disconnect.go and
// common.go. ReasonConnectKick (12) must never trigger a competing reconnect.
type DisconnectError struct{ Reason byte }

func (e *DisconnectError) Error() string {
	return fmt.Sprintf("Octo disconnected by server (reason=%d)", e.Reason)
}
func (e *DisconnectError) Unwrap() error { return ErrDisconnected }
func (e *DisconnectError) Retryable() bool {
	switch e.Reason {
	case 15, 17, 18, 22: // SystemError, NodeMatchError, NodeNotMatch, RateLimit.
		return true
	default: // Auth, kick, ban, unknown, and protocol incompatibility fail closed.
		return false
	}
}

func DecodeDisconnect(body []byte) error {
	d := decoder{r: bytes.NewReader(body)}
	reason := d.u8()
	_ = d.str()
	if d.err != nil || d.r.Len() != 0 {
		return ErrProtocol
	}
	return &DisconnectError{Reason: reason}
}
