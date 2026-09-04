package githubcopilot

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// errNotAccessibleByIntegration is the marker GitHub returns when a request carried a
// credential the endpoint does not accept. On the inference path it means the wrong layer
// of token reached the wire.
const errNotAccessibleByIntegration = "resource not accessible by integration"

// copilotErrorBody is the OpenAI-shaped envelope Copilot returns for inference errors.
// Some auth failures come back as a bare GitHub {"message": "..."} instead, so both
// shapes are read.
type copilotErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
	Message string `json:"message"`
}

// parseCopilotError converts a Copilot inference error into a BifrostError.
//
// Copilot's auth failures are almost always configuration faults rather than transient
// ones: a revoked token, a Copilot policy that excludes the model, or an installation
// without the right permission. Retrying those against a fallback provider silently
// moves paid traffic somewhere the operator did not choose, so 401 and 403 block
// fallbacks. Rate limits and server errors are left alone.
func parseCopilotError(resp *fasthttp.Response) *schemas.BifrostError {
	var bifrostErr schemas.BifrostError

	// Let the generic handler set status and base fields first.
	_ = providerUtils.HandleProviderAPIError(resp, &bifrostErr)

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	body := resp.Body()
	if len(body) > 0 {
		var parsed copilotErrorBody
		if err := sonic.Unmarshal(body, &parsed); err == nil {
			if parsed.Error.Message != "" {
				bifrostErr.Error.Message = parsed.Error.Message
			} else if parsed.Message != "" {
				bifrostErr.Error.Message = parsed.Message
			}
			if parsed.Error.Type != "" {
				bifrostErr.Error.Type = &parsed.Error.Type
			}
		}
	}

	switch resp.StatusCode() {
	case fasthttp.StatusUnauthorized:
		bifrostErr.AllowFallbacks = schemas.Ptr(false)
		bifrostErr.Error.Message = "github copilot: the Copilot API token was rejected (401). " +
			"It expired or was revoked. Upstream said: " + upstreamDetail(bifrostErr.Error.Message)
	case fasthttp.StatusForbidden:
		bifrostErr.AllowFallbacks = schemas.Ptr(false)
		if strings.Contains(strings.ToLower(bifrostErr.Error.Message), errNotAccessibleByIntegration) {
			bifrostErr.Error.Message = "github copilot: the request carried the wrong credential (403). " +
				"A GitHub installation token belongs on the Copilot token exchange only; the " +
				"Copilot API token belongs on inference requests."
			break
		}
		bifrostErr.Error.Message = "github copilot: Copilot refused the request (403). " +
			"The account may lack Copilot access, or an organization policy may exclude this " +
			"model. Upstream said: " + upstreamDetail(bifrostErr.Error.Message)
	}

	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("github copilot: provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "github copilot: provider API error"
		}
	}

	return &bifrostErr
}

// upstreamDetail keeps the operator-facing message readable when Copilot returns an empty
// or whitespace-only body, which it does for some auth failures.
func upstreamDetail(message string) string {
	if strings.TrimSpace(message) == "" {
		return "(no detail)"
	}
	return message
}

// configurationError builds a setup fault that must not drain onto another provider.
//
// providerUtils.NewConfigurationError leaves AllowFallbacks nil, and nil means allowed, so
// a bare configuration error would let a misconfigured Copilot key silently route prompts
// to whatever fallback is configured, on someone else's bill, while the operator sees a
// success. Nothing about a missing credential or a malformed private key improves by being
// retried somewhere else.
func configurationError(message string) *schemas.BifrostError {
	bErr := providerUtils.NewConfigurationError(message)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}
