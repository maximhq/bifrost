package vertex

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var (
	_ schemas.RealtimeProvider              = (*VertexProvider)(nil)
	_ schemas.RealtimeTranslatorProvider    = (*VertexProvider)(nil)
	_ schemas.RealtimeNativeProvider        = (*VertexProvider)(nil)
	_ schemas.RealtimeUsageExtractor        = (*VertexProvider)(nil)
	_ schemas.RealtimeInterimUsageExtractor = (*VertexProvider)(nil)
)

// vertexLivePath is the Live API (BidiGenerateContent) WebSocket endpoint under a Vertex AI host. It is the
// v1beta1 service, which Google's SDKs use and which carries the newest Live settings.
const vertexLivePath = "/ws/google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent"

// SupportsRealtimeAPI reports that Vertex AI serves the Gemini Live API. A client that speaks OpenAI Realtime is
// served by the Gemini translator; a client that speaks Live itself, such as the Google GenAI SDK, is relayed.
func (provider *VertexProvider) SupportsRealtimeAPI() bool {
	return true
}

// vertexLiveLocation returns the project and location a Live session runs in. The host, and the model path
// the setup frame carries, both follow from them, so the URL and the translator read them in one place. The
// location becomes part of the host, so anything but a location name is refused.
func vertexLiveLocation(ctx *schemas.BifrostContext, key schemas.Key) (project, region string, bifrostErr *schemas.BifrostError) {
	project = resolveVertexProjectID(ctx, key)
	if project == "" {
		return "", "", providerUtils.NewConfigurationError("project_id is not set")
	}
	region = resolveVertexRegion(ctx, key)
	if region == "" {
		return "", "", providerUtils.NewConfigurationError("region is not set")
	}
	if !vertexRegionRe.MatchString(region) {
		return "", "", providerUtils.NewConfigurationError("region is not a valid vertex location")
	}
	return project, region, nil
}

// RealtimeWebSocketURL returns the Live endpoint on the host of the key's location: the multi-region pools
// "us" and "eu" use their rep.googleapis.com host, as the other Vertex endpoints do. The model is not part of
// the URL, because Live learns it from the setup frame.
func (provider *VertexProvider) RealtimeWebSocketURL(ctx *schemas.BifrostContext, key schemas.Key, model, intent string) (string, *schemas.BifrostError) {
	if intent == "transcription" {
		return "", providerUtils.NewUnsupportedOperationError(schemas.RealtimeRequest, provider.GetProviderKey())
	}
	_, region, bifrostErr := vertexLiveLocation(ctx, key)
	if bifrostErr != nil {
		return "", bifrostErr
	}
	host := getVertexModelAwareAPIHost(region, model, resolveVertexForceSingleRegion(ctx, key), provider.logger)
	return "wss://" + host + vertexLivePath, nil
}

// RealtimeHeaders authenticates the WebSocket handshake with an OAuth bearer token from the key's credentials.
func (provider *VertexProvider) RealtimeHeaders(_ *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	if key.VertexKeyConfig == nil || key.Value.GetValue() != "" {
		return nil, providerUtils.NewConfigurationError("a vertex live session needs service account or workload identity credentials (api key auth not supported for realtime)")
	}
	tokenSource, err := provider.getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		provider.removeVertexClient(key)
		return nil, providerUtils.NewBifrostOperationError("error getting auth token", err)
	}
	headers := make(map[string]string, len(provider.networkConfig.ExtraHeaders)+1)
	for name, value := range provider.networkConfig.ExtraHeaders {
		if !strings.EqualFold(name, "Authorization") {
			headers[name] = value.GetValue()
		}
	}
	headers["Authorization"] = "Bearer " + token.AccessToken
	return headers, nil
}

// NewRealtimeTranslator returns the Gemini Live translator, addressed to the model by the path
// Vertex AI expects in the setup frame: projects/<project>/locations/<location>/publishers/google/models/<model>.
// The project and location are the ones the connection's model alias resolves to, as they are for the URL.
func (provider *VertexProvider) NewRealtimeTranslator(ctx *schemas.BifrostContext, key schemas.Key, model string) (schemas.RealtimeTranslator, *schemas.BifrostError) {
	if strings.TrimSpace(model) == "" {
		return nil, providerUtils.NewBifrostOperationError("model is required for a vertex live session", nil)
	}
	project, region, bifrostErr := vertexLiveLocation(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	translator, err := gemini.NewLiveTranslatorForModelPath(model, expandVertexModelPath(model, project, region))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create the vertex live translator", err)
	}
	return translator, nil
}

// liveProtocol serves the frames of a client that speaks Live itself, such as the Google GenAI SDK on /genai.
// Vertex AI speaks the Gemini Live protocol unchanged and these methods keep no state, so Gemini's are Vertex's.
var liveProtocol = &gemini.GeminiProvider{}

// NativeRealtimeSetup addresses a Live client's setup frame to the model Bifrost resolved, by the path Vertex AI
// expects. The project and location are the key's, or its alias's, as they are for the URL; the client's own are replaced.
func (provider *VertexProvider) NativeRealtimeSetup(ctx *schemas.BifrostContext, key schemas.Key, setup []byte, model string) ([]byte, *schemas.BifrostError) {
	project, region, bifrostErr := vertexLiveLocation(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	rewritten, err := gemini.LiveSetupWithModelPath(setup, expandVertexModelPath(model, project, region))
	if err != nil {
		return nil, providerUtils.NewBifrostBadRequestError("invalid setup message: " + err.Error())
	}
	return rewritten, nil
}

func (provider *VertexProvider) ExtractRealtimeTurnUsage(terminalFrame []byte) *schemas.BifrostLLMUsage {
	return liveProtocol.ExtractRealtimeTurnUsage(terminalFrame)
}

func (provider *VertexProvider) ExtractRealtimeInterimUsage(event *schemas.BifrostRealtimeEvent) *schemas.BifrostLLMUsage {
	return liveProtocol.ExtractRealtimeInterimUsage(event)
}

func (provider *VertexProvider) ExtractRealtimeTurnOutput(terminalFrame []byte) *schemas.ChatMessage {
	return liveProtocol.ExtractRealtimeTurnOutput(terminalFrame)
}

// SupportsRealtimeWebRTC reports that Live has no WebRTC signaling.
func (provider *VertexProvider) SupportsRealtimeWebRTC() bool {
	return false
}

// ExchangeRealtimeWebRTCSDP is not supported by Live.
func (provider *VertexProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", providerUtils.NewUnsupportedOperationError(schemas.RealtimeRequest, provider.GetProviderKey())
}

func (provider *VertexProvider) ToBifrostRealtimeEvent(frame json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	return liveProtocol.ToBifrostRealtimeEvent(frame)
}

func (provider *VertexProvider) ToProviderRealtimeEvent(event *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	return liveProtocol.ToProviderRealtimeEvent(event)
}

func (provider *VertexProvider) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	return liveProtocol.ShouldStartRealtimeTurn(event)
}

func (provider *VertexProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventResponseDone
}

func (provider *VertexProvider) RealtimeWebRTCDataChannelLabel() string {
	return ""
}

func (provider *VertexProvider) RealtimeWebSocketSubprotocol() string {
	return ""
}

func (provider *VertexProvider) ShouldForwardRealtimeEvent(*schemas.BifrostRealtimeEvent) bool {
	return true
}

func (provider *VertexProvider) ShouldAccumulateRealtimeOutput(eventType schemas.RealtimeEventType) bool {
	return liveProtocol.ShouldAccumulateRealtimeOutput(eventType)
}
