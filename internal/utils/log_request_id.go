package utils

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashRequestIDForLog provides a stable correlation value without writing a
// client-supplied X-Request-ID into routine logs. The original ID continues to
// be used in protocol headers, events, and request context.
func HashRequestIDForLog(requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return hex.EncodeToString(digest[:])
}
