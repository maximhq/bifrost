package lib

import (
	"strings"

	"github.com/google/uuid"
)

const zeroRequestID = "00000000000000000000000000000000"

// NormalizeRequestID preserves a usable caller-supplied request ID and creates
// a fresh one when the value is missing or is the all-zero sentinel emitted by
// some clients. Treating that sentinel as a real ID causes unrelated requests
// to overwrite/collapse into one log record.
func NormalizeRequestID(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || requestID == zeroRequestID {
		return uuid.New().String()
	}
	return requestID
}
