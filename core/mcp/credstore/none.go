package credstore

import (
	"net/http"

	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// noneResolver handles MCPAuthTypeNone — no auth credentials, but the
// transport still gets any static config headers + filtered context-extras
// the caller passes in via BifrostContext.
type noneResolver struct{}

func (r *noneResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	return utils.GetHeadersForToolExecution(ctx, config), nil
}

func (r *noneResolver) RequiresPerCallConnection() bool { return false }
