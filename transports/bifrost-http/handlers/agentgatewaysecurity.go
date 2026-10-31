package handlers

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// rejectAgentSecretReferencesIfAuthBypassed prevents unauthenticated callers
// from directing gateway-owned env/vault credentials to a URL they control.
// Literal credentials and operator-provisioned config.json remain supported.
func rejectAgentSecretReferencesIfAuthBypassed(ctx *fasthttp.RequestCtx, auths ...*schemas.UpstreamAuth) bool {
	if bypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); !bypassed {
		return false
	}
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		var values []*schemas.SecretVar
		for _, value := range auth.Headers {
			value := value
			values = append(values, &value)
		}
		if auth.OAuth != nil {
			values = append(values, auth.OAuth.ClientID, auth.OAuth.ClientSecret)
		}
		if auth.TLS != nil {
			values = append(values, auth.TLS.CACertPEM, auth.TLS.ClientCertPEM, auth.TLS.ClientKeyPEM)
		}
		for _, value := range values {
			if value != nil && (value.Type() != schemas.SecretTypePlainText || value.IsMaskedPlaceholder()) {
				sendAgentGatewayError(ctx, fasthttp.StatusForbidden, "Using env./vault. or stored credentials requires dashboard authentication; provision agents through config.json when dashboard authentication is disabled")
				return true
			}
		}
	}
	return false
}
