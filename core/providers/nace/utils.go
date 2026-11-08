package nace

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const defaultBaseURL = "https://console.nace.ai"

// clientIdentity is bounded to the API's 64-byte attribution limit.
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
		if c >= '!' && c <= '~' {
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
