package azure

import (
	"fmt"
	"net/http"
	"strings"

	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// liveSessionsURL returns the key's /openai/v1/live/sessions URL. Azure speaks OpenAI's GPT Live
// wire protocol; session models are deployment names, resolved through the key's aliases upstream
// of here. The key-level endpoint is used, as GPT Live runs both deployments on one resource.
func liveSessionsURL(key schemas.Key) (string, *schemas.BifrostError) {
	endpoint := ""
	if key.AzureKeyConfig != nil {
		endpoint = strings.TrimRight(strings.TrimSpace(key.AzureKeyConfig.Endpoint.GetValue()), "/")
	}
	if endpoint == "" {
		return "", providerUtils.NewBifrostBadRequestError("azure live sessions require a configured endpoint")
	}
	return endpoint + "/openai/v1/live/sessions", nil
}

// LiveWebSocketURL returns the WSS URL for a GPT Live primary or sideband connection. Azure has
// no fork route. The deployment travels in session.start, never in the URL.
func (provider *AzureProvider) LiveWebSocketURL(key schemas.Key, kind schemas.LiveConnectionKind, sessionID string) (string, *schemas.BifrostError) {
	base, bifrostErr := liveSessionsURL(key)
	if bifrostErr != nil {
		return "", bifrostErr
	}
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)

	switch kind {
	case schemas.LiveConnectionPrimary:
		return base, nil
	case schemas.LiveConnectionSideband:
		escapedID, err := providerUtils.EscapeResourceID(sessionID, "session_id")
		if err != nil {
			return "", err
		}
		return base + "/" + escapedID + "/attach", nil
	case schemas.LiveConnectionFork:
		return "", providerUtils.NewBifrostBadRequestError("azure does not support forking live sessions")
	}
	return "", providerUtils.NewBifrostBadRequestError(fmt.Sprintf("unknown live connection kind %q", kind))
}

// LiveHeaders returns the headers for a GPT Live connection: the api-key or an Entra bearer
// token. Azure issues no ephemeral client keys for GPT Live.
func (provider *AzureProvider) LiveHeaders(ctx *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	headers, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		headers[k] = v.GetValue()
	}
	return headers, nil
}

// CreateLiveWebRTCSession posts a WebRTC session create to /openai/v1/live/sessions and returns
// the session id and SDP answer. Azure rejects session.client, the frontend's data-channel
// permissions, as an unknown parameter, so it is dropped; the gateway, which terminates the
// browser's data channel, enforces those permissions itself. The rest of the body is sent as given.
func (provider *AzureProvider) CreateLiveWebRTCSession(ctx *schemas.BifrostContext, key schemas.Key, body []byte) (*schemas.LiveCreateResponse, *schemas.BifrostError) {
	url, bifrostErr := liveSessionsURL(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	body, err := providerUtils.DeleteJSONField(body, "session.client")
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to prepare live session create request", err)
	}
	headers, bifrostErr := provider.LiveHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.SetBody(body)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() < fasthttp.StatusOK || resp.StatusCode() >= fasthttp.StatusMultipleChoices {
		upstreamErr := openaiProvider.ParseOpenAIError(resp)
		upstreamErr.ExtraFields.RequestType = schemas.LiveRequest
		upstreamErr.ExtraFields.RoutingInfo.Provider = provider.GetProviderKey()
		upstreamErr.ExtraFields.Provider = provider.GetProviderKey()
		if !providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			upstreamErr.ExtraFields.RawResponse = nil
		}
		return nil, providerUtils.SetErrorLatency(upstreamErr, latency)
	}

	var created schemas.LiveCreateResponse
	if err := schemas.Unmarshal(resp.Body(), &created); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to parse live session create response", err)
	}
	// A 2xx with a session id is a created, billable session even without a usable answer; the
	// caller settles it, so only the id is required here.
	if created.Session == nil || created.Session.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("live session create response has no session id", nil)
	}
	return &created, nil
}

// LiveSessionContent is not supported: Azure does not store GPT Live sessions yet.
func (provider *AzureProvider) LiveSessionContent(_ *schemas.BifrostContext, _ schemas.Key, _ string) (*schemas.LiveContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.LiveContentRequest, provider.GetProviderKey())
}
