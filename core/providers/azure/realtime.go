package azure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// openAIEventHelper is a zero-value OpenAI provider used solely to delegate
// event conversion calls. Azure uses the exact same Realtime wire protocol as
// OpenAI, so all event parsing, serialisation, usage extraction, turn detection,
// and output extraction can be reused without modification.
var openAIEventHelper = &openaiProvider.OpenAIProvider{}

// ---------------------------------------------------------------------------
// RealtimeProvider interface
// ---------------------------------------------------------------------------

func (provider *AzureProvider) SupportsRealtimeAPI() bool {
	return true
}

func (provider *AzureProvider) RealtimeWebSocketURL(key schemas.Key, model string) string {
	endpoint := strings.TrimRight(key.AzureKeyConfig.Endpoint.GetValue(), "/")
	endpoint = strings.Replace(endpoint, "https://", "wss://", 1)
	endpoint = strings.Replace(endpoint, "http://", "ws://", 1)

	return fmt.Sprintf("%s/openai/v1/realtime?model=%s",
		endpoint, url.QueryEscape(model))
}

func (provider *AzureProvider) RealtimeHeaders(ctx *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	value := key.Value.GetValue()

	// Ephemeral tokens from /client_secrets use Bearer auth.
	if strings.HasPrefix(value, "ek_") {
		headers := map[string]string{
			"Authorization": "Bearer " + value,
		}
		for k, v := range provider.networkConfig.ExtraHeaders {
			headers[k] = v
		}
		return headers, nil
	}

	headers, authErr := provider.getAzureAuthHeaders(ctx, key, false)
	if authErr != nil {
		return nil, authErr
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		headers[k] = v
	}
	return headers, nil
}

func (provider *AzureProvider) SupportsRealtimeWebRTC() bool {
	return true
}

func (provider *AzureProvider) ExchangeRealtimeWebRTCSDP(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	model string,
	sdp string,
	session json.RawMessage,
) (string, *schemas.BifrostError) {
	endpoint := strings.TrimRight(key.AzureKeyConfig.Endpoint.GetValue(), "/")

	upstreamURL := fmt.Sprintf("%s/openai/v1/realtime?model=%s",
		endpoint, url.QueryEscape(model))

	// Build multipart body: sdp + optional session
	bodyBuf := &bytes.Buffer{}
	writer := multipart.NewWriter(bodyBuf)
	if err := writer.WriteField("sdp", sdp); err != nil {
		return "", newAzureRealtimeError(fasthttp.StatusInternalServerError, "server_error", "failed to encode upstream SDP body", err)
	}
	if session != nil {
		if err := writer.WriteField("session", string(session)); err != nil {
			return "", newAzureRealtimeError(fasthttp.StatusInternalServerError, "server_error", "failed to encode upstream session body", err)
		}
	}
	if err := writer.Close(); err != nil {
		return "", newAzureRealtimeError(fasthttp.StatusInternalServerError, "server_error", "failed to finalize upstream SDP body", err)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(upstreamURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	// Ephemeral tokens (ek_*) need Bearer auth; regular API keys use api-key header.
	value := key.Value.GetValue()
	if strings.HasPrefix(value, "ek_") {
		req.Header.Set("Authorization", "Bearer "+value)
	} else {
		authHeaders, authErr := provider.getAzureAuthHeaders(ctx, key, false)
		if authErr != nil {
			return "", authErr
		}
		for k, v := range authHeaders {
			req.Header.Set(k, v)
		}
	}

	for k, v := range provider.networkConfig.ExtraHeaders {
		req.Header.Set(k, v)
	}
	req.SetBody(bodyBuf.Bytes())

	_, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return "", bifrostErr
	}

	answerBody := resp.Body()
	if resp.StatusCode() < fasthttp.StatusOK || resp.StatusCode() >= fasthttp.StatusMultipleChoices {
		return "", provider.realtimeWebRTCUpstreamError(ctx, resp.StatusCode(), answerBody)
	}

	return string(answerBody), nil
}

// ---------------------------------------------------------------------------
// Event conversion — delegates to OpenAI (same wire protocol)
// ---------------------------------------------------------------------------

func (provider *AzureProvider) ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	return openAIEventHelper.ToBifrostRealtimeEvent(providerEvent)
}

func (provider *AzureProvider) ToProviderRealtimeEvent(bifrostEvent *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	return openAIEventHelper.ToProviderRealtimeEvent(bifrostEvent)
}

// ---------------------------------------------------------------------------
// Turn lifecycle — delegates to OpenAI
// ---------------------------------------------------------------------------

func (provider *AzureProvider) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	return openAIEventHelper.ShouldStartRealtimeTurn(event)
}

func (provider *AzureProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return openAIEventHelper.RealtimeTurnFinalEvent()
}

func (provider *AzureProvider) ShouldForwardRealtimeEvent(event *schemas.BifrostRealtimeEvent) bool {
	return true
}

func (provider *AzureProvider) ShouldAccumulateRealtimeOutput(eventType schemas.RealtimeEventType) bool {
	return openAIEventHelper.ShouldAccumulateRealtimeOutput(eventType)
}

func (provider *AzureProvider) RealtimeWebRTCDataChannelLabel() string {
	return "oai-events"
}

func (provider *AzureProvider) RealtimeWebSocketSubprotocol() string {
	return "realtime"
}

// ---------------------------------------------------------------------------
// RealtimeUsageExtractor — delegates to OpenAI
// ---------------------------------------------------------------------------

func (provider *AzureProvider) ExtractRealtimeTurnUsage(terminalEventRaw []byte) *schemas.BifrostLLMUsage {
	return openAIEventHelper.ExtractRealtimeTurnUsage(terminalEventRaw)
}

func (provider *AzureProvider) ExtractRealtimeTurnOutput(terminalEventRaw []byte) *schemas.ChatMessage {
	return openAIEventHelper.ExtractRealtimeTurnOutput(terminalEventRaw)
}

// ---------------------------------------------------------------------------
// RealtimeSessionProvider — client_secrets only (not legacy /sessions)
// ---------------------------------------------------------------------------

func (provider *AzureProvider) CreateRealtimeClientSecret(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	endpointType schemas.RealtimeSessionEndpointType,
	rawRequest json.RawMessage,
) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	// Azure does not support the legacy /sessions endpoint.
	if endpointType == schemas.RealtimeSessionEndpointSessions {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     schemas.Ptr(fasthttp.StatusBadRequest),
			Error: &schemas.ErrorField{
				Type:    schemas.Ptr("invalid_request_error"),
				Message: "Azure does not support the legacy /sessions endpoint; use /v1/realtime/client_secrets instead",
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RequestType: schemas.RealtimeRequest,
				Provider:    provider.GetProviderKey(),
			},
		}
	}

	normalizedBody, _, bifrostErr := openaiProvider.NormalizeRealtimeClientSecretRequest(rawRequest, schemas.Azure, endpointType)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	endpoint := strings.TrimRight(key.AzureKeyConfig.Endpoint.GetValue(), "/")
	upstreamURL := fmt.Sprintf("%s/openai/v1/realtime/client_secrets", endpoint)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(upstreamURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	authHeaders, authErr := provider.getAzureAuthHeaders(ctx, key, false)
	if authErr != nil {
		return nil, authErr
	}
	for k, v := range authHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		req.Header.Set(k, v)
	}
	req.SetBody(normalizedBody)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	if resp.StatusCode() < fasthttp.StatusOK || resp.StatusCode() >= fasthttp.StatusMultipleChoices {
		return nil, provider.parseRealtimeClientSecretError(ctx, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to decode response body", err)
	}

	out := &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode(),
		Headers:    headers,
		Body:       body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: headers,
		},
	}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequestIfJSON(req, &out.ExtraFields)
	}

	return out, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (provider *AzureProvider) realtimeWebRTCUpstreamError(ctx *schemas.BifrostContext, statusCode int, body []byte) *schemas.BifrostError {
	message := fmt.Sprintf("upstream realtime handshake failed for %s", provider.GetProviderKey())
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		message = parsed.Error.Message
	}

	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(statusCode),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr("upstream_error"),
			Message: message,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.RealtimeRequest,
			Provider:    provider.GetProviderKey(),
		},
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostErr.ExtraFields.RawResponse = map[string]any{
			"status": statusCode,
			"body":   string(body),
		}
	}
	return bifrostErr
}

func newAzureRealtimeError(status int, errorType, message string, err error) *schemas.BifrostError {
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     schemas.Ptr(status),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(errorType),
			Message: message,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.RealtimeRequest,
			Provider:    schemas.Azure,
		},
	}
	if err != nil {
		bifrostErr.Error.Error = err
	}
	return bifrostErr
}


func (provider *AzureProvider) parseRealtimeClientSecretError(ctx *schemas.BifrostContext, resp *fasthttp.Response) *schemas.BifrostError {
	body, _ := providerUtils.CheckAndDecodeBody(resp)
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := string(body)
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(resp.StatusCode()),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr("upstream_error"),
			Message: msg,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.RealtimeRequest,
			Provider:    provider.GetProviderKey(),
		},
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostErr.ExtraFields.RawResponse = map[string]any{
			"status": resp.StatusCode(),
			"body":   string(body),
		}
	}
	return bifrostErr
}
