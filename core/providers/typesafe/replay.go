package typesafe

import (
	"strings"

	"github.com/valyala/fasthttp"
)

// idempotencyReplayedHeader marks a response that a System One endpoint served
// from a saved result for a repeated Idempotency-Key, without evaluating or
// charging again.
const idempotencyReplayedHeader = "X-Idempotency-Replayed"

// isIdempotentReplay reports whether resp is a free same-key replay.
func isIdempotentReplay(resp *fasthttp.Response) bool {
	if resp == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(resp.Header.Peek(idempotencyReplayedHeader))), "true")
}
