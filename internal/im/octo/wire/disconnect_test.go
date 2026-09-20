package wire

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestServerDisconnectReasonClassificationAndSanitization(t *testing.T) {
	for _, reason := range []byte{0, 1, 2, 12, 14, 15, 17, 18, 19, 22, 255} {
		b := bytes.NewBuffer([]byte{reason})
		_ = putString(b, "secret-server-detail")
		err := DecodeDisconnect(b.Bytes())
		var detail *DisconnectError
		if !errors.As(err, &detail) || !errors.Is(err, ErrDisconnected) || detail.Reason != reason {
			t.Fatalf("reason %d: %v", reason, err)
		}
		wantRetry := reason == 15 || reason == 17 || reason == 18 || reason == 22
		if detail.Retryable() != wantRetry || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe classification %d: %v", reason, err)
		}
	}
	for _, body := range [][]byte{nil, {12}, {12, 0, 2, 1}, {12, 0, 0, 99}} {
		if !errors.Is(DecodeDisconnect(body), ErrProtocol) {
			t.Fatal("malformed disconnect accepted")
		}
	}
}
