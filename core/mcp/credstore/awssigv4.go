package credstore

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/providers/utils/awsauth"
	"github.com/maximhq/bifrost/core/schemas"
)

// awsSigV4Resolver handles MCPAuthTypeAWSSigV4. The credential is not a header: the
// client's HTTP transport signs every request (utils.NewAWSSigV4Client), so
// ConnectionHeaders is empty, like none.
type awsSigV4Resolver struct{}

func (r *awsSigV4Resolver) ConnectionHeaders(_ *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	if config == nil || config.AWSSigV4 == nil {
		return nil, fmt.Errorf("aws_sigv4 is not configured")
	}
	return http.Header{}, nil
}

func (r *awsSigV4Resolver) RequiresPerCallConnection() bool { return false }

// ForceRefresh drops the cached AWS credentials, so the next request signs with
// freshly resolved ones.
func (r *awsSigV4Resolver) ForceRefresh(_ *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	if config == nil || config.AWSSigV4 == nil {
		return fmt.Errorf("aws_sigv4 is not configured")
	}
	awsauth.Invalidate(utils.AWSSigV4Credentials(config.AWSSigV4), strings.TrimSpace(config.AWSSigV4.Region.GetValue()))
	return nil
}

// AdminConnectionHeaders: as for none, only a client running per-call has no
// persistent connection to discover through.
func (r *awsSigV4Resolver) AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error) {
	if !needsSessionStickiness(config) {
		return r.ConnectionHeaders(schemas.NewBifrostContext(ctx, schemas.NoDeadline), config)
	}
	return nil, fmt.Errorf("admin connection headers not supported for auth_type %q on a sticky connection", schemas.MCPAuthTypeAWSSigV4)
}
