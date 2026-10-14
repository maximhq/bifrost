package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/textproto"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const idempotencyKeyHeader = "Idempotency-Key"

// scopeIdempotencyKey namespaces the client's Idempotency-Key by virtual key
// before it is sent upstream. Bifrost's upstream credential is shared across
// tenants, so forwarding the key unchanged would let two tenants that pick the
// same key receive each other's saved result. The wire model is part of the
// scope, so a fallback attempt to a different model never reuses the upstream
// key of the primary with a different body. The scoped key is a deterministic
// hash, so a retry of the same attempt by the same tenant still replays.
// Requests without a virtual key ID share one namespace.
func scopeIdempotencyKey(ctx context.Context, req *fasthttp.Request, model string) {
	key := schemas.DecisionIdempotencyKey(ctx)
	if key == "" {
		return
	}
	vkID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
	req.Header.Del(textproto.CanonicalMIMEHeaderKey(idempotencyKeyHeader))
	req.Header.Set(idempotencyKeyHeader, schemas.DecisionIdempotencyScope(strings.TrimSpace(vkID), model, key))
}

// withRoutedModel returns body with only the top-level "model" value replaced
// by model, splicing that value's byte range so every other byte, key order
// and number stays exact. A body with no model key is returned unchanged. A
// body with more than one top-level key equal to "model" under case folding is
// rejected as ambiguous, since the parsed and forwarded model could differ.
func withRoutedModel(body []byte, model string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	start, end := -1, -1
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid request body: %w", err)
		}
		name, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("invalid request body: %w", err)
		}
		if !strings.EqualFold(name, "model") {
			continue
		}
		if start >= 0 {
			return nil, fmt.Errorf("request body has duplicate model keys")
		}
		if name != "model" {
			return nil, fmt.Errorf("request body has a non-canonical model key %q", name)
		}
		end = int(dec.InputOffset())
		start = end - len(raw)
	}
	if start < 0 || model == "" {
		return body, nil
	}
	value, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)+len(value))
	out = append(out, body[:start]...)
	out = append(out, value...)
	return append(out, body[end:]...), nil
}
