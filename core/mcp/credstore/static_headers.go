package credstore

import (
	"net/http"

	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// staticHeadersResolver handles MCPAuthTypeHeaders — admin-configured static
// headers, merged with any context-extras present in the BifrostContext. Also
// serves as the fallback for empty/unrecognized AuthType (matches the
// existing "empty AuthType means headers" normalization in UpdateClient).
type staticHeadersResolver struct{}

func (r *staticHeadersResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	return utils.GetHeadersForToolExecution(ctx, config), nil
}

func (r *staticHeadersResolver) RequiresPerCallConnection() bool { return false }
