package typesafe

import (
	"context"
	"net/textproto"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const idempotencyKeyHeader = "Idempotency-Key"

// scopeIdempotencyKey namespaces the client's Idempotency-Key by virtual key
// before it is sent upstream. Bifrost's upstream credential is shared across
// tenants, so forwarding the key unchanged would let two tenants that pick the
// same key receive each other's saved result. The scoped key is a
// deterministic hash, so a retry by the same tenant still replays.
func scopeIdempotencyKey(ctx context.Context, req *fasthttp.Request) {
	key := schemas.DecisionIdempotencyKey(ctx)
	if key == "" {
		return
	}
	vkID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
	req.Header.Del(textproto.CanonicalMIMEHeaderKey(idempotencyKeyHeader))
	req.Header.Set(idempotencyKeyHeader, schemas.DecisionIdempotencyScope(strings.TrimSpace(vkID), key))
}
