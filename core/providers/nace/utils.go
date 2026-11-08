package nace

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const defaultBaseURL = "https://console.nace.ai"

// resolveModelAlias follows the public aliases and retired IDs served by Drex.
func resolveModelAlias(model string) string {
	switch model {
	case "drex-latest", "nacedm-latest":
		return "drex-v2.0"
	case "drex-v1.0":
		return "drex-max"
	case "drex-v1.1":
		return "drex-v1.5"
	default:
		return model
	}
}

// clientIdentity is bounded to the API's 64-byte attribution limit and keeps only
// the version characters the API accepts (letters, digits and ._@+-).
func clientIdentity(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	var clean strings.Builder
	clean.WriteString("bifrost/")
	for _, c := range version {
		if clean.Len() >= 64 {
			break
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.ContainsRune("._@+-", c) {
			clean.WriteByte(byte(c))
		}
	}
	return clean.String()
}

func (provider *NaceProvider) setHeaders(ctx *schemas.BifrostContext, req *fasthttp.Request, key schemas.Key) {
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	req.Header.Set("X-Nace-Client", provider.clientID)
	req.Header.SetUserAgent(provider.clientID)
}
