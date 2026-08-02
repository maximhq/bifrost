package credstore

import (
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// noneResolver handles MCPAuthTypeNone — no credentials, no auth header.
// Static config headers are layered separately by the caller via
// utils.StaticConfigHeaders, and per-request extras flow through
// CredStore.RequestHeaders. ConnectionHeaders here is empty by design.
type noneResolver struct{}

func (r *noneResolver) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (r *noneResolver) RequiresPerCallConnection() bool { return false }
