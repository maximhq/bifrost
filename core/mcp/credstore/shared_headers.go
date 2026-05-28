package credstore

import (
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// sharedHeadersResolver handles MCPAuthTypeHeaders — the admin-configured
// static headers on the MCP client. ConnectionHeaders here returns ONLY the
// Authorization header (if admin set one in config.Headers); other static
// headers are layered by the caller via utils.StaticConfigHeaders so they
// remain plugin-mutable.
//
// CredStore.resolverFor also normalizes empty AuthType to "headers" so this
// resolver covers the legacy DB default.
type sharedHeadersResolver struct{}

func (r *sharedHeadersResolver) ConnectionHeaders(_ *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	headers := http.Header{}
	if config == nil {
		return headers, nil
	}
	// Headers are case-insensitive on the wire but case-sensitive in Go maps;
	// match case-insensitively (consistent with utils.StaticConfigHeaders'
	// Authorization exclusion) to keep the security guarantee tight.
	for key, value := range config.Headers {
		if strings.EqualFold(key, "Authorization") {
			headers.Set("Authorization", value.GetValue())
			break
		}
	}
	return headers, nil
}

func (r *sharedHeadersResolver) RequiresPerCallConnection() bool { return false }
