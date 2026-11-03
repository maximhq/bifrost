package azure

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"maps"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Azure AI Speech serves keys and aliases with use_speech_endpoints. For text to speech the model
// is "neural" (the voice is sent by name) or a voice suffix such as "DragonHDLatestNeural" or
// "MAI-Voice-2.1-Flash". For speech to text it is "fast-transcription", "llm-speech", a
// MAI-Transcribe model such as "MAI-Transcribe-2", or "short-audio" for the short audio endpoint.
const (
	azureSpeechNeuralModel         = "neural"
	azureSpeechFastModel           = "fast-transcription"
	azureSpeechLLMModel            = "llm-speech"
	azureSpeechSynthesisPath       = "/tts/cognitiveservices/v1"
	azureSpeechVoicesPath          = "/tts/cognitiveservices/voices/list"
	azureSpeechTranscriptionPath   = "/speechtotext/transcriptions:transcribe"
	azureSpeechAPIVersion          = "2025-10-15"
	azureSpeechDefaultOutputFormat = "audio-24khz-48kbitrate-mono-mp3"
	azureSpeechDefaultLanguage     = "en-US"
)

// The short audio endpoint takes the recognition mode as a path segment and reports offsets in 100ns ticks.
const (
	azureSpeechShortAudioModel        = "short-audio"
	azureSpeechDefaultRecognitionMode = "conversation"
	azureSpeechTicksPerSecond         = 10_000_000
)

// azureSpeechRecognitionModes are the short audio endpoints. The mode is a path segment, so no other value is sent.
var azureSpeechRecognitionModes = map[string]bool{"conversation": true, "interactive": true, "dictation": true}

// azureSpeechTranscriptionModels are the speech to text models listed for a speech key; Azure has no endpoint that lists them.
var azureSpeechTranscriptionModels = []string{azureSpeechFastModel, azureSpeechLLMModel, azureSpeechShortAudioModel, "MAI-Transcribe-2"}

// azureSpeechVoicesDeadlineMargin is how long before a list models deadline the voices list gives up.
const azureSpeechVoicesDeadlineMargin = time.Second

// azureSpeechOutputFormats maps OpenAI response formats to Azure output formats. aac and flac
// have no Azure equivalent and fall back to mp3; any other value is sent as an Azure format name.
var azureSpeechOutputFormats = map[string]string{
	"":     azureSpeechDefaultOutputFormat,
	"mp3":  azureSpeechDefaultOutputFormat,
	"aac":  azureSpeechDefaultOutputFormat,
	"flac": azureSpeechDefaultOutputFormat,
	"opus": "ogg-24khz-16bit-mono-opus",
	"wav":  "riff-24khz-16bit-mono-pcm",
	"pcm":  "raw-24khz-16bit-mono-pcm",
}

var (
	azureSSMLTextEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	azureSSMLAttrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

// ToAzureSpeechSSML builds the SSML body for a text to speech request; an input that is already
// SSML is sent as is.
func ToAzureSpeechSSML(model string, request *schemas.BifrostSpeechRequest) (string, error) {
	if request == nil || request.Input == nil {
		return "", errors.New("speech input is not provided")
	}
	input := request.Input.Input
	if isAzureSSML(input) {
		return input, nil
	}
	params := request.Params
	if params == nil || params.VoiceConfig == nil || params.VoiceConfig.Voice == nil || *params.VoiceConfig.Voice == "" {
		return "", errors.New("voice is required")
	}
	voice := *params.VoiceConfig.Voice
	if model != azureSpeechNeuralModel && !strings.Contains(voice, ":") {
		voice += ":" + model
	}
	language := azureSpeechDefaultLanguage
	if params.LanguageCode != nil && *params.LanguageCode != "" {
		language = *params.LanguageCode
	}

	var ssml strings.Builder
	ssml.WriteString(`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xmlns:mstts="https://www.w3.org/2001/mstts" xml:lang="`)
	ssml.WriteString(azureSSMLAttrEscaper.Replace(language))
	ssml.WriteString(`"><voice name="`)
	ssml.WriteString(azureSSMLAttrEscaper.Replace(voice))
	ssml.WriteString(`">`)

	style, hasStyle := schemas.SafeExtractString(params.ExtraParams["style"])
	role, hasRole := schemas.SafeExtractString(params.ExtraParams["role"])
	expressAs := hasStyle || hasRole
	if expressAs {
		ssml.WriteString("<mstts:express-as")
		if hasStyle {
			ssml.WriteString(` style="` + azureSSMLAttrEscaper.Replace(style) + `"`)
		}
		if degree, ok := schemas.SafeExtractFloat64(params.ExtraParams["styledegree"]); ok {
			ssml.WriteString(` styledegree="` + strconv.FormatFloat(degree, 'f', -1, 64) + `"`)
		}
		if hasRole {
			ssml.WriteString(` role="` + azureSSMLAttrEscaper.Replace(role) + `"`)
		}
		ssml.WriteString(">")
	}
	if params.Speed != nil {
		ssml.WriteString(`<prosody rate="` + strconv.FormatFloat(*params.Speed, 'f', -1, 64) + `">`)
	}
	ssml.WriteString(azureSSMLTextEscaper.Replace(input))
	if params.Speed != nil {
		ssml.WriteString("</prosody>")
	}
	if expressAs {
		ssml.WriteString("</mstts:express-as>")
	}
	ssml.WriteString("</voice></speak>")
	return ssml.String(), nil
}

// azureSpeechOutputFormat returns the X-Microsoft-OutputFormat value for a response format.
func azureSpeechOutputFormat(responseFormat string) string {
	if format, ok := azureSpeechOutputFormats[responseFormat]; ok {
		return format
	}
	return responseFormat
}

// isAzureSSML reports whether a text to speech input is already SSML.
func isAzureSSML(input string) bool {
	trimmed := strings.TrimSpace(input)
	return strings.HasPrefix(trimmed, "<speak") || strings.HasPrefix(trimmed, "<?xml")
}

// azureSpeechServedModel returns the voice's suffix when it differs from the requested model, so
// "en-US-Ava:DragonHDLatestNeural" sent as "neural" is priced as the HD voice Azure synthesizes.
func azureSpeechServedModel(requested, voice string) *string {
	if _, suffix, ok := strings.Cut(voice, ":"); ok && suffix != "" && !strings.EqualFold(suffix, requested) {
		return new(suffix)
	}
	return nil
}

// azureSpeechBillableChars counts characters the way Azure bills text to speech: the whole SSML
// except the <speak> and <voice> tags, with each Han character counted twice.
func azureSpeechBillableChars(ssml string) int {
	count := 0
	for i := 0; i < len(ssml); {
		if ssml[i] == '<' {
			if end := strings.IndexByte(ssml[i:], '>'); end != -1 && isAzureSpeechEnvelopeTag(ssml[i+1:i+end]) {
				i += end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(ssml[i:])
		if unicode.Is(unicode.Han, r) {
			count += 2
		} else {
			count++
		}
		i += size
	}
	return count
}

// isAzureSpeechEnvelopeTag reports whether tag (the text between < and >) opens or closes <speak> or <voice>.
func isAzureSpeechEnvelopeTag(tag string) bool {
	name := strings.TrimSuffix(strings.TrimPrefix(tag, "/"), "/")
	if i := strings.IndexAny(name, " \t\r\n"); i != -1 {
		name = name[:i]
	}
	return name == "speak" || name == "voice"
}

// ToAzureSpeechTranscriptionDefinition builds the fast transcription definition. "fast-transcription"
// is the classic model, "llm-speech" runs LLM Speech, and any other model names a MAI-Transcribe model.
func ToAzureSpeechTranscriptionDefinition(model string, params *schemas.TranscriptionParameters) *AzureSpeechTranscriptionDefinition {
	definition := &AzureSpeechTranscriptionDefinition{}
	switch model {
	case azureSpeechFastModel:
	case azureSpeechLLMModel:
		definition.EnhancedMode = &AzureSpeechEnhancedMode{Enabled: true, Task: new("transcribe")}
	default:
		definition.EnhancedMode = &AzureSpeechEnhancedMode{Enabled: true, Model: new(model)}
	}
	if params == nil {
		return definition
	}

	// The classic model rejects a bare language code such as "en", so it is left to language identification.
	if params.Language != nil && *params.Language != "" && (definition.EnhancedMode != nil || strings.Contains(*params.Language, "-")) {
		definition.Locales = []string{*params.Language}
	}
	// Several candidate locales let Azure identify the language.
	if locales := azureSpeechExtraList(params.ExtraParams["locales"]); len(locales) > 0 {
		definition.Locales = locales
	}
	if model == azureSpeechLLMModel {
		if params.Prompt != nil && *params.Prompt != "" {
			definition.EnhancedMode.Prompt = []string{*params.Prompt}
		}
		if target, ok := schemas.SafeExtractString(params.ExtraParams["target_language"]); ok && target != "" {
			definition.EnhancedMode.Task = new("translate")
			definition.EnhancedMode.TargetLanguage = new(target)
		}
	}
	if definition.EnhancedMode != nil && definition.EnhancedMode.Model != nil {
		options := &AzureSpeechModelOptions{}
		if params.ResponseFormat != nil && *params.ResponseFormat == "verbose_json" {
			options.Timestamps = new("word")
		}
		if style, ok := schemas.SafeExtractString(params.ExtraParams["transcribe_style"]); ok && style != "" {
			options.TranscribeStyle = new(style)
		}
		if options.Timestamps != nil || options.TranscribeStyle != nil {
			definition.EnhancedMode.ModelOptions = options
		}
	}

	if diarize, ok := schemas.SafeExtractBool(params.ExtraParams["diarization"]); (ok && diarize) || schemas.IsDiarizedTranscriptionFormat(params.ResponseFormat) {
		definition.Diarization = &AzureSpeechDiarization{Enabled: true}
	}
	if maxSpeakers, ok := schemas.SafeExtractIntPointer(params.ExtraParams["max_speakers"]); ok {
		definition.Diarization = &AzureSpeechDiarization{Enabled: true, MaxSpeakers: maxSpeakers}
	}
	if mode, ok := schemas.SafeExtractString(params.ExtraParams["profanity_filter_mode"]); ok && mode != "" {
		definition.ProfanityFilterMode = new(mode)
	}
	if phrases := azureSpeechExtraList(params.ExtraParams["phrases"]); len(phrases) > 0 {
		definition.PhraseList = &AzureSpeechPhraseList{Phrases: phrases}
	}
	for _, channel := range azureSpeechExtraList(params.ExtraParams["channels"]) {
		if n, err := strconv.Atoi(channel); err == nil {
			definition.Channels = append(definition.Channels, n)
		}
	}
	return definition
}

// azureSpeechExtraList reads a list extra param: a JSON array, or a comma-separated multipart value.
func azureSpeechExtraList(value interface{}) []string {
	switch v := value.(type) {
	case string:
		// A JSON list such as channels=[0,1] arrives as a string from a multipart form.
		if trimmed := strings.TrimSpace(v); strings.HasPrefix(trimmed, "[") {
			var list []interface{}
			if err := sonic.UnmarshalString(trimmed, &list); err == nil {
				return azureSpeechExtraList(list)
			}
		}
		var items []string
		for item := range strings.SplitSeq(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		return items
	case []interface{}:
		items := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := schemas.SafeExtractString(item); ok {
				items = append(items, s)
			} else if n, ok := schemas.SafeExtractInt(item); ok {
				items = append(items, strconv.Itoa(n))
			}
		}
		return items
	case []int:
		items := make([]string, 0, len(v))
		for _, n := range v {
			items = append(items, strconv.Itoa(n))
		}
		return items
	}
	items, _ := schemas.SafeExtractStringSlice(value)
	return items
}

// ToBifrostTranscriptionResponse converts a fast transcription result. Usage is the audio duration
// rounded up to the second, which is what Azure meters.
func ToBifrostTranscriptionResponse(response *AzureSpeechTranscriptionResponse) *schemas.BifrostTranscriptionResponse {
	texts := make([]string, 0, len(response.CombinedPhrases))
	for _, phrase := range response.CombinedPhrases {
		texts = append(texts, phrase.Text)
	}
	duration := float64(response.DurationMilliseconds) / 1000
	billedSeconds := math.Ceil(duration)
	result := &schemas.BifrostTranscriptionResponse{
		Text:     strings.Join(texts, "\n"),
		Duration: &duration,
		Usage:    &schemas.TranscriptionUsage{Type: "duration", Seconds: &billedSeconds},
	}
	if len(response.Phrases) > 0 && response.Phrases[0].Locale != "" {
		result.Language = new(response.Phrases[0].Locale)
	}

	diarized := slices.ContainsFunc(response.Phrases, func(phrase AzureSpeechPhrase) bool { return phrase.Speaker != nil })
	for i, phrase := range response.Phrases {
		start := float64(phrase.OffsetMilliseconds) / 1000
		end := float64(phrase.OffsetMilliseconds+phrase.DurationMilliseconds) / 1000
		var speaker *string
		if phrase.Speaker != nil {
			speaker = new(strconv.Itoa(*phrase.Speaker))
		}
		if diarized {
			segment := schemas.TranscriptionDiarizedSegment{ID: "seg_" + strconv.Itoa(i), Type: "transcript.text.segment", Start: start, End: end, Text: phrase.Text}
			if speaker != nil {
				segment.Speaker = *speaker
			}
			result.DiarizedSegments = append(result.DiarizedSegments, segment)
		} else {
			result.Segments = append(result.Segments, schemas.TranscriptionSegment{ID: i, Start: start, End: end, Text: phrase.Text})
		}
		for _, word := range phrase.Words {
			result.Words = append(result.Words, schemas.TranscriptionWord{
				Word:    word.Text,
				Start:   float64(word.OffsetMilliseconds) / 1000,
				End:     float64(word.OffsetMilliseconds+word.DurationMilliseconds) / 1000,
				Speaker: speaker,
			})
		}
	}
	return result
}

// parseAzureSpeechError maps the Azure Speech error bodies to a BifrostError. Text to speech
// answers most 4xx with an empty body, so a bare 400 says what Azure validates.
func parseAzureSpeechError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp AzureSpeechError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	var code, message string
	switch {
	case errorResp.Error != nil && errorResp.Error.Message != "":
		code, message = errorResp.Error.Code, errorResp.Error.Message
	case errorResp.Message != "":
		code, message = errorResp.Code, errorResp.Message
		if errorResp.InnerError != nil && errorResp.InnerError.Code != "" {
			code = errorResp.InnerError.Code
		}
	case errorResp.Title != "":
		message = errorResp.Title
		for _, field := range slices.Sorted(maps.Keys(errorResp.Errors)) {
			message += " " + field + ": " + strings.Join(errorResp.Errors[field], " ")
		}
	case resp.StatusCode() == fasthttp.StatusBadRequest && len(bytes.TrimSpace(resp.Body())) == 0:
		message = "azure speech rejected the request (400): check the voice name, SSML and output format"
	}
	if message != "" {
		bifrostErr.Error.Message = message
	}
	if code != "" {
		bifrostErr.Error.Code = new(code)
	}
	return bifrostErr
}

// useSpeechEndpoints reports whether speech and transcription requests go to Azure AI Speech
// rather than Azure OpenAI. A per-alias value wins over the key's.
func useSpeechEndpoints(ctx *schemas.BifrostContext, key schemas.Key) bool {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.AzureAliasCfg != nil && ra.Config.AzureAliasCfg.UseSpeechEndpoints != nil {
		return *ra.Config.AzureAliasCfg.UseSpeechEndpoints
	}
	return key.AzureKeyConfig != nil && key.AzureKeyConfig.UseSpeechEndpoints
}

// getAzureSpeechAuthHeaders returns auth headers for Azure AI Speech, which takes a resource key as
// Ocp-Apim-Subscription-Key and answers 401 to api-key. Bearer tokens are sent unchanged.
func (provider *AzureProvider) getAzureSpeechAuthHeaders(ctx *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if apiKey, ok := authHeader["api-key"]; ok {
		delete(authHeader, "api-key")
		authHeader["Ocp-Apim-Subscription-Key"] = apiKey
	}
	return authHeader, nil
}

// listSpeechVoices returns the text to speech voices the key's Azure AI Speech region serves.
func (provider *AzureProvider) listSpeechVoices(ctx *schemas.BifrostContext, key schemas.Key) ([]AzureSpeechVoice, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	authHeader, bifrostErr := provider.getAzureSpeechAuthHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(endpoint + azureSpeechVoicesPath)
	req.Header.SetMethod(http.MethodGet)
	for k, v := range authHeader {
		req.Header.Set(k, v)
	}

	_, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseAzureSpeechError(resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}
	var voices []AzureSpeechVoice
	if err := sonic.Unmarshal(body, &voices); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	return voices, nil
}

// prepareSpeechSynthesisRequest writes an Azure AI Speech text to speech call onto req and
// returns the SSML body it carries and the model Azure serves when it differs from the requested one.
func (provider *AzureProvider) prepareSpeechSynthesisRequest(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest, req *fasthttp.Request) ([]byte, *string, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	authHeader, bifrostErr := provider.getAzureSpeechAuthHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, nil, bifrostErr
	}

	body, ok := providerUtils.CheckAndGetRawRequestBody(ctx, request)
	if !ok {
		ct, ch := providerUtils.StartPhaseSpan(ctx, "convertor")
		ssml, err := ToAzureSpeechSSML(request.Model, request)
		if ct != nil {
			if err != nil {
				ct.EndSpan(ch, schemas.SpanStatusError, err.Error())
			} else {
				ct.EndSpan(ch, schemas.SpanStatusOk, "")
			}
		}
		if err != nil {
			return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
		}
		body = []byte(ssml)
	}
	// The voice Azure synthesizes is the request's own; only SSML the caller sent is parsed for it.
	var voice string
	if !ok && !isAzureSSML(request.Input.Input) {
		voice = *request.Params.VoiceConfig.Voice
	} else {
		_, voice = azureSSMLLanguageAndVoice(string(body))
	}
	responseFormat := ""
	if request.Params != nil {
		responseFormat = request.Params.ResponseFormat
	}

	req.SetRequestURI(endpoint + providerUtils.GetPathFromContext(ctx, azureSpeechSynthesisPath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", azureSpeechOutputFormat(responseFormat))
	req.Header.SetUserAgent("bifrost") // Azure answers 400 to a text to speech call without one
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	for k, v := range authHeader {
		req.Header.Set(k, v)
	}
	req.SetBody(body)
	return body, azureSpeechServedModel(request.Model, voice), nil
}

// speechSynthesis serves text to speech on Azure AI Speech: the input goes out as SSML and the
// audio comes back as is.
func (provider *AzureProvider) speechSynthesis(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	body, servedModel, bifrostErr := provider.prepareSpeechSynthesisRequest(ctx, key, request, req)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	// The SSML body is not JSON, so it is attached as a string rather than through EnrichError.
	enrich := func(bifrostErr *schemas.BifrostError, latency time.Duration) *schemas.BifrostError {
		bifrostErr = providerUtils.EnrichError(ctx, bifrostErr, nil, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		if sendBackRawRequest {
			bifrostErr.ExtraFields.RawRequest = string(body)
		}
		return bifrostErr
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, enrich(bifrostErr, latency)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, enrich(parseAzureSpeechError(resp), latency)
	}

	ft, fh := providerUtils.StartPhaseSpan(ctx, "response-finalize")
	audio, err := providerUtils.CheckAndDecodeBody(resp)
	if ft != nil {
		if err != nil {
			ft.EndSpan(fh, schemas.SpanStatusError, err.Error())
		} else {
			ft.EndSpan(fh, schemas.SpanStatusOk, "")
		}
	}
	if err != nil {
		return nil, enrich(providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), latency)
	}

	response := &schemas.BifrostSpeechResponse{
		Audio: audio,
		Usage: &schemas.SpeechUsage{InputChars: azureSpeechBillableChars(string(body))},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
			RoutingInfo:             schemas.RoutingInfo{ServerSideFallbackModel: servedModel},
		},
	}
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = string(body)
	}
	return response, nil
}

// speechSynthesisStream streams Azure AI Speech audio as it is synthesized. Azure sends the audio
// as a chunked body, so each read becomes a delta and the done event carries the billable characters.
func (provider *AzureProvider) speechSynthesisStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	body, servedModel, bifrostErr := provider.prepareSpeechSynthesisRequest(ctx, key, request, req)
	if bifrostErr != nil {
		fasthttp.ReleaseResponse(resp)
		return nil, bifrostErr
	}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	enrich := func(bifrostErr *schemas.BifrostError, latency time.Duration) *schemas.BifrostError {
		bifrostErr = providerUtils.EnrichError(ctx, bifrostErr, nil, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		if sendBackRawRequest {
			bifrostErr.ExtraFields.RawRequest = string(body)
		}
		return bifrostErr
	}

	startTime := time.Now()
	requestErr := providerUtils.DoStreamingRequest(ctx, provider.streamingClient, req, resp)
	latency := time.Since(startTime)
	if requestErr != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(requestErr, context.Canceled) {
			return nil, enrich(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   requestErr,
				},
			}, latency)
		}
		if errors.Is(requestErr, fasthttp.ErrTimeout) || errors.Is(requestErr, context.DeadlineExceeded) {
			return nil, enrich(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, requestErr), latency)
		}
		return nil, enrich(providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, requestErr), latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, enrich(parseAzureSpeechError(resp), latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		// The helpers attach their body as raw JSON, which the SSML is not.
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, nil)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, nil)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()

		buffer := make([]byte, 64*1024)
		chunkIndex := -1
		lastChunkTime := startTime
		for {
			if ctx.Err() != nil {
				return
			}
			n, readErr := reader.Read(buffer)
			if n > 0 {
				chunkIndex++
				audioChunk := make([]byte, n)
				copy(audioChunk, buffer[:n])
				response := &schemas.BifrostSpeechStreamResponse{
					Type:  schemas.SpeechStreamResponseTypeDelta,
					Audio: audioChunk,
					ExtraFields: schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()
				if sendBackRawResponse {
					response.ExtraFields.RawResponse = audioChunk
				}
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, response, nil, nil), responseChan, postHookSpanFinalizer)
			}
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				if readErr == io.EOF {
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error reading stream: %v", readErr)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}
		}

		finalResponse := &schemas.BifrostSpeechStreamResponse{
			Type:  schemas.SpeechStreamResponseTypeDone,
			Audio: []byte{},
			Usage: &schemas.SpeechUsage{InputChars: azureSpeechBillableChars(string(body))},
			ExtraFields: schemas.BifrostResponseExtraFields{
				ChunkIndex:  chunkIndex + 1,
				Latency:     time.Since(startTime).Milliseconds(),
				RoutingInfo: schemas.RoutingInfo{ServerSideFallbackModel: servedModel},
			},
		}
		if sendBackRawRequest {
			finalResponse.ExtraFields.RawRequest = string(body)
		}
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, finalResponse, nil, nil), responseChan, postHookSpanFinalizer)
	}()

	return responseChan, nil
}

// speechTranscription serves speech to text on Azure AI Speech fast transcription: the audio and a
// JSON definition go out as multipart, and the phrases come back with offsets in milliseconds.
func (provider *AzureProvider) speechTranscription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	if request.Input == nil || len(request.Input.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("transcription file is not provided", nil)
	}
	authHeader, bifrostErr := provider.getAzureSpeechAuthHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	ct, ch := providerUtils.StartPhaseSpan(ctx, "convertor")
	definition := ToAzureSpeechTranscriptionDefinition(request.Model, request.Params)
	if ct != nil {
		ct.EndSpan(ch, schemas.SpanStatusOk, "")
	}

	mt, mh := providerUtils.StartPhaseSpan(ctx, "request-marshal")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	err := writeAzureSpeechTranscriptionMultipart(writer, request.Input, definition)
	if mt != nil {
		if err != nil {
			mt.EndSpan(mh, schemas.SpanStatusError, err.Error())
		} else {
			mt.EndSpan(mh, schemas.SpanStatusOk, "")
		}
	}
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(endpoint + providerUtils.GetPathFromContext(ctx, azureSpeechTranscriptionPath) + "?api-version=" + resolveAPIVersion(ctx, azureSpeechAPIVersion))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	for k, v := range authHeader {
		req.Header.Set(k, v)
	}
	req.SetBody(body.Bytes())

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, nil, nil, false, provider.sendBackRawResponse, latency)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseAzureSpeechError(resp), nil, nil, false, provider.sendBackRawResponse, latency)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), latency)
	}
	var azureResponse AzureSpeechTranscriptionResponse
	pt, ph := providerUtils.StartResponseParseSpan(ctx)
	err = sonic.Unmarshal(responseBody, &azureResponse)
	if pt != nil {
		if err != nil {
			pt.EndSpan(ph, schemas.SpanStatusError, "response parse failed")
		} else {
			pt.EndSpan(ph, schemas.SpanStatusOk, "")
		}
	}
	if err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err), latency)
	}

	response := ToBifrostTranscriptionResponse(&azureResponse)
	// diarized_json requires segments, even for audio with no speech.
	if request.Params != nil && schemas.IsDiarizedTranscriptionFormat(request.Params.ResponseFormat) && response.DiarizedSegments == nil {
		response.DiarizedSegments = []schemas.TranscriptionDiarizedSegment{}
	}
	response.ExtraFields = schemas.BifrostResponseExtraFields{
		Latency:                 latency.Milliseconds(),
		ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
	}
	if sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			response.ExtraFields.RawResponse = rawResponse
		}
	}
	return response, nil
}

// writeAzureSpeechTranscriptionMultipart writes the "audio" and "definition" parts and closes the writer.
func writeAzureSpeechTranscriptionMultipart(writer *multipart.Writer, input *schemas.TranscriptionInput, definition *AzureSpeechTranscriptionDefinition) error {
	filename := input.Filename
	if filename == "" {
		filename = providerUtils.AudioFilenameFromBytes(input.File)
	}
	fileWriter, err := writer.CreateFormFile("audio", filename)
	if err != nil {
		return err
	}
	if _, err := fileWriter.Write(input.File); err != nil {
		return err
	}
	definitionJSON, err := providerUtils.MarshalSorted(definition)
	if err != nil {
		return err
	}
	if err := writer.WriteField("definition", string(definitionJSON)); err != nil {
		return err
	}
	return writer.Close()
}

// ToBifrostSpeechRequest converts an Azure REST text to speech call. The SSML goes to Azure as is;
// the model is the first voice's suffix ("en-US-Ava:DragonHDLatestNeural" is DragonHDLatestNeural),
// else "neural", so the price follows the voice. Routing picks the provider.
func (r *AzureSpeechSynthesisRequest) ToBifrostSpeechRequest() *schemas.BifrostSpeechRequest {
	request := &schemas.BifrostSpeechRequest{
		Model:  azureSpeechNeuralModel,
		Input:  &schemas.SpeechInput{Input: r.SSML},
		Params: &schemas.SpeechParameters{ResponseFormat: r.OutputFormat},
	}
	language, voice := azureSSMLLanguageAndVoice(r.SSML)
	if voice != "" {
		request.Params.VoiceConfig = &schemas.SpeechVoiceInput{Voice: new(voice)}
		if _, suffix, ok := strings.Cut(voice, ":"); ok && suffix != "" {
			request.Model = suffix
		}
	}
	if language != "" {
		request.Params.LanguageCode = new(language)
	}
	return request
}

// azureSSMLLanguageAndVoice reads the xml:lang of <speak> and the name of the first <voice>.
func azureSSMLLanguageAndVoice(ssml string) (string, string) {
	decoder := xml.NewDecoder(strings.NewReader(ssml))
	language := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			return language, ""
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attr := range start.Attr {
			switch {
			case start.Name.Local == "speak" && attr.Name.Local == "lang":
				language = attr.Value
			case start.Name.Local == "voice" && attr.Name.Local == "name":
				return language, attr.Value
			}
		}
	}
}

// AzureSpeechContentType returns the Content-Type Azure sends for a text to speech output format.
func AzureSpeechContentType(responseFormat string) string {
	format := azureSpeechOutputFormat(responseFormat)
	switch {
	case strings.HasPrefix(format, "riff-"):
		return "audio/x-wav"
	case strings.HasPrefix(format, "ogg-"):
		return "audio/ogg"
	case strings.HasPrefix(format, "webm-"):
		return "audio/webm"
	case strings.HasPrefix(format, "raw-"):
		return "audio/basic"
	case strings.HasSuffix(format, "-mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(format, "-opus"):
		return "audio/opus"
	case strings.HasPrefix(format, "amr-wb-"):
		return "audio/x-amr-wb-sh"
	case strings.HasPrefix(format, "g722-"):
		return "audio/g722"
	}
	return "application/octet-stream"
}

// ToBifrostTranscriptionRequest converts a fast transcription call. The model follows enhancedMode
// (its model, else "llm-speech") or is "fast-transcription", and the definition fields go to the
// params ToAzureSpeechTranscriptionDefinition reads, so Azure receives the definition as sent.
// Only the first prompt is kept, and timestamps other than "word" are left to the model default.
// Routing picks the provider.
func (r *AzureSpeechFastTranscriptionRequest) ToBifrostTranscriptionRequest() *schemas.BifrostTranscriptionRequest {
	definition := r.Definition
	params := &schemas.TranscriptionParameters{ExtraParams: map[string]interface{}{}}
	model := azureSpeechFastModel
	if mode := definition.EnhancedMode; mode != nil && mode.Enabled {
		model = azureSpeechLLMModel
		if mode.Model != nil && *mode.Model != "" {
			model = *mode.Model
		}
		if len(mode.Prompt) > 0 {
			params.Prompt = new(mode.Prompt[0])
		}
		if mode.Task != nil && *mode.Task == "translate" && mode.TargetLanguage != nil {
			params.ExtraParams["target_language"] = *mode.TargetLanguage
		}
		if options := mode.ModelOptions; options != nil {
			if options.Timestamps != nil && *options.Timestamps == "word" {
				params.ResponseFormat = new("verbose_json")
			}
			if options.TranscribeStyle != nil {
				params.ExtraParams["transcribe_style"] = *options.TranscribeStyle
			}
		}
	}
	switch len(definition.Locales) {
	case 0:
	case 1:
		params.Language = new(definition.Locales[0])
	default:
		params.ExtraParams["locales"] = definition.Locales
	}
	if len(definition.Channels) > 0 {
		params.ExtraParams["channels"] = definition.Channels
	}
	if diarization := definition.Diarization; diarization != nil && diarization.Enabled {
		params.ExtraParams["diarization"] = true
		if diarization.MaxSpeakers != nil {
			params.ExtraParams["max_speakers"] = *diarization.MaxSpeakers
		}
	}
	if definition.ProfanityFilterMode != nil {
		params.ExtraParams["profanity_filter_mode"] = *definition.ProfanityFilterMode
	}
	if definition.PhraseList != nil && len(definition.PhraseList.Phrases) > 0 {
		params.ExtraParams["phrases"] = definition.PhraseList.Phrases
	}
	return &schemas.BifrostTranscriptionRequest{
		Model:  model,
		Input:  &schemas.TranscriptionInput{File: r.Audio, Filename: r.Filename},
		Params: params,
	}
}

// ToBifrostTranscriptionRequest converts a short audio recognition call to the "short-audio" model;
// the mode, format, profanity option, custom endpoint and pronunciation assessment go as extra params.
// Routing picks the provider.
func (r *AzureSpeechShortAudioRequest) ToBifrostTranscriptionRequest() *schemas.BifrostTranscriptionRequest {
	params := &schemas.TranscriptionParameters{ExtraParams: map[string]interface{}{"recognition_mode": r.Mode}}
	if r.Language != "" {
		params.Language = new(r.Language)
	}
	if r.Format != "" {
		params.ExtraParams["format"] = r.Format
	}
	if r.Profanity != "" {
		params.ExtraParams["profanity"] = r.Profanity
	}
	if r.EndpointID != "" {
		params.ExtraParams["cid"] = r.EndpointID
	}
	if r.PronunciationAssessment != "" {
		params.ExtraParams["pronunciation_assessment"] = r.PronunciationAssessment
	}
	return &schemas.BifrostTranscriptionRequest{
		Model:  azureSpeechShortAudioModel,
		Input:  &schemas.TranscriptionInput{File: r.Audio},
		Params: params,
	}
}

// ToBifrostShortAudioTranscriptionResponse converts a short audio result. audioSeconds is the length
// of the audio sent, which is what Azure meters, rounded up to the second.
func ToBifrostShortAudioTranscriptionResponse(response *AzureSpeechShortAudioResponse, audioSeconds float64) *schemas.BifrostTranscriptionResponse {
	text := response.DisplayText
	if text == "" && len(response.NBest) > 0 {
		text = response.NBest[0].Display
	}
	billedSeconds := math.Ceil(audioSeconds)
	result := &schemas.BifrostTranscriptionResponse{
		Text:     text,
		Duration: &audioSeconds,
		Usage:    &schemas.TranscriptionUsage{Type: "duration", Seconds: &billedSeconds},
	}
	if text != "" {
		result.Segments = []schemas.TranscriptionSegment{{
			Start: float64(response.Offset) / azureSpeechTicksPerSecond,
			End:   float64(response.Offset+response.Duration) / azureSpeechTicksPerSecond,
			Text:  text,
		}}
	}
	return result
}

// azureSpeechAudioSeconds reads the length of a WAV (PCM) or Ogg Opus clip, the two containers the
// short audio endpoint accepts.
func azureSpeechAudioSeconds(audio []byte) (float64, bool) {
	switch {
	case len(audio) >= 12 && string(audio[:4]) == "RIFF" && string(audio[8:12]) == "WAVE":
		byteRate := 0
		for offset := 12; offset+8 <= len(audio); {
			size := int(binary.LittleEndian.Uint32(audio[offset+4 : offset+8]))
			body := offset + 8
			switch string(audio[offset : offset+4]) {
			case "fmt ":
				if body+12 <= len(audio) {
					byteRate = int(binary.LittleEndian.Uint32(audio[body+8 : body+12]))
				}
			case "data":
				if byteRate == 0 {
					return 0, false
				}
				// A streamed WAV leaves the data size unset, so the rest of the file is the audio.
				if size == 0 || body+size > len(audio) {
					size = len(audio) - body
				}
				return float64(size) / float64(byteRate), true
			}
			offset = body + size + size%2
		}
	case len(audio) >= 4 && string(audio[:4]) == "OggS":
		// The last page's granule position counts 48 kHz samples for Opus.
		if last := bytes.LastIndex(audio, []byte("OggS")); last+14 <= len(audio) {
			return float64(binary.LittleEndian.Uint64(audio[last+6:last+14])) / 48000, true
		}
	}
	return 0, false
}

// speechShortAudioTranscription serves the "short-audio" model on the Azure AI Speech short audio
// endpoint: the audio is the body and the options go in the query string.
func (provider *AzureProvider) speechShortAudioTranscription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	if request.Input == nil || len(request.Input.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("transcription file is not provided", nil)
	}
	authHeader, bifrostErr := provider.getAzureSpeechAuthHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var params schemas.TranscriptionParameters
	if request.Params != nil {
		params = *request.Params
	}
	mode := azureSpeechDefaultRecognitionMode
	if value, ok := schemas.SafeExtractString(params.ExtraParams["recognition_mode"]); ok && value != "" {
		if !azureSpeechRecognitionModes[value] {
			return nil, providerUtils.NewBifrostBadRequestError("recognition_mode must be conversation, interactive or dictation")
		}
		mode = value
	}
	language := azureSpeechDefaultLanguage
	if params.Language != nil && *params.Language != "" {
		language = *params.Language
	}
	query := url.Values{"language": {language}}
	for _, name := range []string{"format", "profanity", "cid"} {
		if value, ok := schemas.SafeExtractString(params.ExtraParams[name]); ok && value != "" {
			query.Set(name, value)
		}
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(endpoint + providerUtils.GetPathFromContext(ctx, "/stt/speech/recognition/"+mode+"/cognitiveservices/v1") + "?" + query.Encode())
	req.Header.SetMethod(http.MethodPost)
	// Azure reads the format from the audio itself; these name the two containers it accepts.
	if bytes.HasPrefix(request.Input.File, []byte("OggS")) {
		req.Header.SetContentType("audio/ogg; codecs=opus")
	} else {
		req.Header.SetContentType("audio/wav; codecs=audio/pcm")
	}
	if assessment, ok := schemas.SafeExtractString(params.ExtraParams["pronunciation_assessment"]); ok && assessment != "" {
		req.Header.Set("Pronunciation-Assessment", assessment)
	}
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	for k, v := range authHeader {
		req.Header.Set(k, v)
	}
	req.SetBody(request.Input.File)

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, nil, nil, false, provider.sendBackRawResponse, latency)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseAzureSpeechError(resp), nil, nil, false, provider.sendBackRawResponse, latency)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), latency)
	}
	var azureResponse AzureSpeechShortAudioResponse
	if err := sonic.Unmarshal(responseBody, &azureResponse); err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err), latency)
	}

	// Azure sends no billing header for short audio, so an unreadable clip falls back to where speech ended.
	audioSeconds, ok := azureSpeechAudioSeconds(request.Input.File)
	if !ok {
		audioSeconds = float64(azureResponse.Offset+azureResponse.Duration) / azureSpeechTicksPerSecond
	}
	response := ToBifrostShortAudioTranscriptionResponse(&azureResponse, audioSeconds)
	response.Language = new(language)
	response.ExtraFields = schemas.BifrostResponseExtraFields{
		Latency:                 latency.Milliseconds(),
		ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
	}
	if sendBackRawResponse {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			response.ExtraFields.RawResponse = rawResponse
		}
	}
	return response, nil
}

// ToAzureSpeechTranscriptionResponse renders a transcription in the fast transcription shape, for a
// result that Azure AI Speech did not produce.
func ToAzureSpeechTranscriptionResponse(response *schemas.BifrostTranscriptionResponse) *AzureSpeechTranscriptionResponse {
	result := &AzureSpeechTranscriptionResponse{
		CombinedPhrases: []AzureSpeechCombinedPhrase{{Text: response.Text}},
		Phrases:         []AzureSpeechPhrase{},
	}
	if response.Duration != nil {
		result.DurationMilliseconds = azureSpeechMilliseconds(*response.Duration)
	}
	locale := ""
	if response.Language != nil {
		locale = *response.Language
	}
	addPhrase := func(start, end float64, text string, speaker *int) {
		phrase := AzureSpeechPhrase{
			Speaker:              speaker,
			OffsetMilliseconds:   azureSpeechMilliseconds(start),
			DurationMilliseconds: azureSpeechMilliseconds(end - start),
			Text:                 text,
			Locale:               locale,
		}
		for _, word := range response.Words {
			if word.Start >= start && word.Start < end {
				phrase.Words = append(phrase.Words, AzureSpeechWord{
					Text:                 word.Word,
					OffsetMilliseconds:   azureSpeechMilliseconds(word.Start),
					DurationMilliseconds: azureSpeechMilliseconds(word.End - word.Start),
				})
			}
		}
		result.Phrases = append(result.Phrases, phrase)
	}
	for _, segment := range response.Segments {
		addPhrase(segment.Start, segment.End, segment.Text, nil)
	}
	for _, segment := range response.DiarizedSegments {
		var speaker *int
		if n, err := strconv.Atoi(segment.Speaker); err == nil {
			speaker = &n
		}
		addPhrase(segment.Start, segment.End, segment.Text, speaker)
	}
	return result
}

func azureSpeechMilliseconds(seconds float64) int64 {
	return int64(math.Round(seconds * 1000))
}

// ToAzureSpeechShortAudioResponse renders a transcription in the short audio shape, for a result that
// Azure AI Speech did not produce. NBest carries only the display text.
func ToAzureSpeechShortAudioResponse(response *schemas.BifrostTranscriptionResponse) *AzureSpeechShortAudioResponse {
	result := &AzureSpeechShortAudioResponse{
		RecognitionStatus: "Success",
		DisplayText:       response.Text,
		NBest:             []AzureSpeechShortAudioNBest{{Display: response.Text}},
	}
	switch {
	case len(response.Segments) > 0:
		first, last := response.Segments[0], response.Segments[len(response.Segments)-1]
		result.Offset = int64(math.Round(first.Start * azureSpeechTicksPerSecond))
		result.Duration = int64(math.Round((last.End - first.Start) * azureSpeechTicksPerSecond))
	case response.Duration != nil:
		result.Duration = int64(math.Round(*response.Duration * azureSpeechTicksPerSecond))
	}
	return result
}

// ToAzureSpeechError renders an error in the {code, message} shape fast transcription answers with.
func ToAzureSpeechError(bifrostErr *schemas.BifrostError) *AzureSpeechError {
	if bifrostErr == nil {
		return nil
	}
	result := &AzureSpeechError{}
	if bifrostErr.Error != nil {
		result.Message = bifrostErr.Error.Message
		if bifrostErr.Error.Code != nil {
			result.Code = *bifrostErr.Error.Code
		} else if bifrostErr.Error.Type != nil {
			result.Code = *bifrostErr.Error.Type
		}
	}
	return result
}
