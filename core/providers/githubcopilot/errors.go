package githubcopilot

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// errFeatureNotAuthorized is Copilot's 403 for a PAT when the Copilot CLI policy is off.
const errFeatureNotAuthorized = "not authorized to use this copilot feature"

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
// ones: a revoked token or a Copilot policy that excludes the model. Retrying those against a fallback provider silently
// moves paid traffic somewhere the operator did not choose, so 401 and 403 block
// fallbacks. Rate limits and server errors are left alone.
func parseCopilotError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp schemas.BifrostError

	// Let the generic handler set status, the raw response and the retry hint first. Only
	// what Copilot itself sent feeds the message, since the handler's wording for an empty
	// or non-JSON body is not upstream detail.
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	bifrostErr.EventID = errorResp.EventID
	bifrostErr.Error = errorResp.Error

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
		} else {
			// Copilot sends some rejections as plain text ("Bad Request", or "unauthorized: not
			// authorized to use this Copilot feature" for a PAT); keep them as the detail.
			bifrostErr.Error.Message = truncateDetail(strings.TrimSpace(string(body)))
		}
	}

	classifyAuthError(bifrostErr, resp.StatusCode())

	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("github copilot: provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "github copilot: provider API error"
		}
	}

	return bifrostErr
}

// classifyAuthError blocks fallbacks on 401 and 403 and rewrites the message to name the
// likely cause. Shared by the Copilot-shaped and Anthropic-shaped (/v1/messages) error paths.
func classifyAuthError(bifrostErr *schemas.BifrostError, status int) {
	switch status {
	case fasthttp.StatusUnauthorized:
		bifrostErr.AllowFallbacks = schemas.Ptr(false)
		bifrostErr.Error.Message = "github copilot: the Copilot API token was rejected (401). " +
			"It expired or was revoked. Upstream said: " + upstreamDetail(bifrostErr.Error.Message)
	case fasthttp.StatusForbidden:
		bifrostErr.AllowFallbacks = schemas.Ptr(false)
		// A PAT on a seat whose organization has not enabled the Copilot CLI policy.
		if strings.Contains(strings.ToLower(bifrostErr.Error.Message), errFeatureNotAuthorized) {
			bifrostErr.Error.Message = "github copilot: Copilot refused the personal access token (403). The " +
				"organization that assigns this Copilot seat must enable the Copilot CLI policy (Settings > " +
				"Copilot > Policies). Upstream said: " + bifrostErr.Error.Message
			return
		}
		bifrostErr.Error.Message = "github copilot: Copilot refused the request (403). " +
			"The account may lack Copilot access, or an organization policy may exclude this " +
			"model. Upstream said: " + upstreamDetail(bifrostErr.Error.Message)
	}
}

// blockAuthFallbacks applies classifyAuthError to an error from the shared Anthropic
// handlers, which parse errors themselves and take no custom converter.
func blockAuthFallbacks(bifrostErr *schemas.BifrostError) *schemas.BifrostError {
	if bifrostErr == nil || bifrostErr.StatusCode == nil {
		return bifrostErr
	}
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	classifyAuthError(bifrostErr, *bifrostErr.StatusCode)
	return bifrostErr
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
// success. Nothing about a missing credential improves by being retried somewhere else.
func configurationError(message string) *schemas.BifrostError {
	bErr := providerUtils.NewConfigurationError(message)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}

// truncateDetail bounds a plain-text upstream body before it becomes an error message.
func truncateDetail(detail string) string {
	const maxDetail = 512
	if len(detail) > maxDetail {
		return detail[:maxDetail] + "..."
	}
	return detail
}
