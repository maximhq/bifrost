package openai

import (
	"fmt"
	"net/http"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// LiveWebSocketURL returns the WSS URL for a GPT Live primary, sideband or fork connection.
// The model travels in session.start, never in the URL.
func (provider *OpenAIProvider) LiveWebSocketURL(_ schemas.Key, kind schemas.LiveConnectionKind, sessionID string) (string, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.LiveRequest); err != nil {
		return "", err
	}
	base := provider.networkConfig.BaseURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	base += "/v1/live/sessions"

	if kind == schemas.LiveConnectionPrimary {
		return base, nil
	}
	if kind != schemas.LiveConnectionSideband && kind != schemas.LiveConnectionFork {
		return "", providerUtils.NewBifrostBadRequestError(fmt.Sprintf("unknown live connection kind %q", kind))
	}
	escapedID, err := providerUtils.EscapeResourceID(sessionID, "session_id")
	if err != nil {
		return "", err
	}
	if kind == schemas.LiveConnectionFork {
		return base + "/" + escapedID + "/fork", nil
	}
	return base + "/" + escapedID + "/attach", nil
}

// LiveHeaders returns the headers for a GPT Live WebSocket connection.
func (provider *OpenAIProvider) LiveHeaders(ctx *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	return provider.RealtimeHeaders(ctx, key)
}

// CreateLiveWebRTCSession posts a WebRTC session create to /v1/live/sessions and returns the
// session id and SDP answer. The body is sent as given.
func (provider *OpenAIProvider) CreateLiveWebRTCSession(ctx *schemas.BifrostContext, key schemas.Key, body []byte) (*schemas.LiveCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.LiveRequest); err != nil {
		return nil, err
	}
	headers, bifrostErr := provider.LiveHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/live/sessions", schemas.LiveRequest))
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
		upstreamErr := ParseOpenAIError(resp)
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
	if created.Session == nil || created.Session.ID == "" || created.Transport == nil || created.Transport.SDP == "" {
		return nil, providerUtils.NewBifrostOperationError("live session create response has no session id or SDP answer", nil)
	}
	return &created, nil
}
