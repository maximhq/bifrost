package sarvam

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToSarvamSpeechRequest converts a BifrostSpeechRequest into Sarvam's native
// Bulbul TTS request shape. bifrostReq.Model carries the Sarvam TTS model
// ("bulbul:v2"/"bulbul:v3"); Params.VoiceConfig.Voice carries the speaker.
func ToSarvamSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) (*SarvamSpeechRequest, *schemas.BifrostError) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, nil
	}

	sarvamReq := &SarvamSpeechRequest{
		Text: bifrostReq.Input.Input,
	}

	if bifrostReq.Model != "" {
		sarvamReq.Model = &bifrostReq.Model
	}

	// Sarvam requires target_language_code on every request with no default of
	// its own; Bifrost's LanguageCode param is optional across providers, so
	// default to en-IN rather than erroring when the caller omits it.
	sarvamReq.TargetLanguageCode = "en-IN"

	if bifrostReq.Params != nil {
		if bifrostReq.Params.LanguageCode != nil && *bifrostReq.Params.LanguageCode != "" {
			sarvamReq.TargetLanguageCode = *bifrostReq.Params.LanguageCode
		}

		if bifrostReq.Params.VoiceConfig != nil {
			if len(bifrostReq.Params.VoiceConfig.MultiVoiceConfig) > 0 {
				return nil, providerUtils.NewUnsupportedOperationError("multi-voice speech synthesis", schemas.Sarvam)
			}
			sarvamReq.Speaker = bifrostReq.Params.VoiceConfig.Voice
		}

		if bifrostReq.Params.Speed != nil {
			sarvamReq.Pace = bifrostReq.Params.Speed
		}

		if bifrostReq.Params.ExtraParams != nil {
			// Copy before stripping provider-specific keys below - ExtraParams is
			// the caller's own map, and retries/fallbacks may reuse this request.
			sarvamReq.ExtraParams = make(map[string]interface{}, len(bifrostReq.Params.ExtraParams))
			for k, v := range bifrostReq.Params.ExtraParams {
				sarvamReq.ExtraParams[k] = v
			}
			if pitch, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["pitch"]); ok {
				delete(sarvamReq.ExtraParams, "pitch")
				sarvamReq.Pitch = pitch
			}
			if loudness, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["loudness"]); ok {
				delete(sarvamReq.ExtraParams, "loudness")
				sarvamReq.Loudness = loudness
			}
			if sampleRate, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["speech_sample_rate"]); ok {
				delete(sarvamReq.ExtraParams, "speech_sample_rate")
				sarvamReq.SpeechSampleRate = sampleRate
			}
			if enablePreprocessing, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_preprocessing"]); ok {
				delete(sarvamReq.ExtraParams, "enable_preprocessing")
				sarvamReq.EnablePreprocessing = enablePreprocessing
			}
			if temperature, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["temperature"]); ok {
				delete(sarvamReq.ExtraParams, "temperature")
				sarvamReq.Temperature = temperature
			}
			if dictID, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["dict_id"]); ok {
				delete(sarvamReq.ExtraParams, "dict_id")
				sarvamReq.DictID = dictID
			}
			if enableCached, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_cached_responses"]); ok {
				delete(sarvamReq.ExtraParams, "enable_cached_responses")
				sarvamReq.EnableCachedResponses = enableCached
			}
		}

		if bifrostReq.Params.ResponseFormat != "" {
			sarvamReq.OutputAudioCodec = &bifrostReq.Params.ResponseFormat
		}
	}

	return sarvamReq, nil
}

// Speech performs a text-to-speech request against Sarvam's Bulbul API.
// Sarvam returns a JSON body with base64-encoded audio (audios[]), not raw
// binary like OpenAI's /v1/audio/speech, so the response is carried in
// BifrostSpeechResponse.AudioBase64 (the same field ElevenLabs' with-timestamps
// variant uses) rather than .Audio.
func (provider *SarvamProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	sarvamReq, bifrostErr := ToSarvamSpeechRequest(request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/text-to-speech"))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for k, v := range AuthHeaders(key) {
		req.Header.Set(k, v)
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return sarvamReq, nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if !providerUtils.ApplyLargePayloadRequestBody(ctx, req) {
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseSarvamError(resp), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	var sarvamResp SarvamSpeechResponse
	if err := sonic.Unmarshal(body, &sarvamResp); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to parse Sarvam text-to-speech response", err), jsonData, body, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	if len(sarvamResp.Audios) == 0 {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("Sarvam text-to-speech response contained no audio", nil), jsonData, body, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Sarvam's only wire shape is base64 JSON (no raw-binary option like OpenAI's
	// /v1/audio/speech), so decode into .Audio to match what every other provider
	// hands callers - ready-to-use bytes, no extra base64 decode step required.
	audioBytes, err := base64.StdEncoding.DecodeString(sarvamResp.Audios[0])
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to decode Sarvam base64 audio", err), jsonData, body, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	bifrostResponse := &schemas.BifrostSpeechResponse{
		Audio: audioBytes,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
		},
	}

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	return bifrostResponse, nil
}

// buildSarvamTTSWSConfig converts a BifrostSpeechRequest into the config
// message Sarvam's TTS WebSocket requires as the first client message.
func buildSarvamTTSWSConfig(bifrostReq *schemas.BifrostSpeechRequest) (*SarvamTTSWSConfigMessage, *schemas.BifrostError) {
	data := SarvamTTSWSConfigData{
		TargetLanguageCode: "en-IN",
		Speaker:            "shubh",
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.LanguageCode != nil && *bifrostReq.Params.LanguageCode != "" {
			data.TargetLanguageCode = *bifrostReq.Params.LanguageCode
		}
		if bifrostReq.Params.VoiceConfig != nil {
			if len(bifrostReq.Params.VoiceConfig.MultiVoiceConfig) > 0 {
				return nil, providerUtils.NewUnsupportedOperationError("multi-voice speech synthesis", schemas.Sarvam)
			}
			if bifrostReq.Params.VoiceConfig.Voice != nil {
				data.Speaker = *bifrostReq.Params.VoiceConfig.Voice
			}
		}
		if bifrostReq.Params.Speed != nil {
			data.Pace = bifrostReq.Params.Speed
		}
		if bifrostReq.Params.ResponseFormat != "" {
			data.OutputAudioCodec = &bifrostReq.Params.ResponseFormat
		}
		if bifrostReq.Params.ExtraParams != nil {
			if pitch, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["pitch"]); ok {
				data.Pitch = pitch
			}
			if loudness, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["loudness"]); ok {
				data.Loudness = loudness
			}
			if temperature, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["temperature"]); ok {
				data.Temperature = temperature
			}
			if sampleRate, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["speech_sample_rate"]); ok {
				data.SpeechSampleRate = sampleRate
			}
			if enablePreprocessing, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enable_preprocessing"]); ok {
				data.EnablePreprocessing = enablePreprocessing
			}
			if bitrate, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["output_audio_bitrate"]); ok {
				data.OutputAudioBitrate = bitrate
			}
			if dictID, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["dict_id"]); ok {
				data.DictID = dictID
			}
		}
	}

	return &SarvamTTSWSConfigMessage{Type: "config", Data: data}, nil
}

// SpeechStream performs a streaming text-to-speech request against Sarvam's
// TTS WebSocket (wss://.../text-to-speech/ws), which is undocumented in
// Sarvam's public REST OpenAPI spec but is specified in their AsyncAPI spec
// (served at docs.sarvam.ai, linked from llms.txt). Protocol: connect, send
// one "config" message, then a "text" message with the input, then a "flush"
// signal; the server streams "audio" messages (base64-encoded chunks) and
// finishes with an "event" message where event_type is "final".
func (provider *SarvamProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if request == nil || request.Input == nil {
		return nil, providerUtils.NewBifrostOperationError("speech input is required", nil)
	}

	config, bifrostErr := buildSarvamTTSWSConfig(request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	model := "bulbul:v2"
	if request.Model != "" {
		model = request.Model
	}

	wsURL := strings.Replace(provider.networkConfig.BaseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/text-to-speech/ws?model=" + url.QueryEscape(model) + "&send_completion_event=true"

	header := http.Header{}
	for k, v := range AuthHeaders(key) {
		header.Set(k, v)
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		header.Set(k, v)
	}

	startTime := time.Now()
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), time.Since(startTime))
	}

	configBytes, err := sonic.Marshal(config)
	if err != nil {
		conn.Close()
		return nil, providerUtils.NewBifrostOperationError("failed to marshal Sarvam TTS WebSocket config message", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, configBytes); err != nil {
		conn.Close()
		return nil, providerUtils.NewBifrostOperationError("failed to send Sarvam TTS WebSocket config message", err)
	}

	textBytes, err := sonic.Marshal(&SarvamTTSWSTextMessage{Type: "text", Data: SarvamTTSWSTextData{Text: request.Input.Input}})
	if err != nil {
		conn.Close()
		return nil, providerUtils.NewBifrostOperationError("failed to marshal Sarvam TTS WebSocket text message", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, textBytes); err != nil {
		conn.Close()
		return nil, providerUtils.NewBifrostOperationError("failed to send Sarvam TTS WebSocket text message", err)
	}

	flushBytes, _ := sonic.Marshal(&SarvamTTSWSSignalMessage{Type: "flush"})
	if err := conn.WriteMessage(websocket.TextMessage, flushBytes); err != nil {
		conn.Close()
		return nil, providerUtils.NewBifrostOperationError("failed to send Sarvam TTS WebSocket flush signal", err)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	go func() {
		defer conn.Close()
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, configBytes)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, configBytes)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)

		chunkIndex := -1
		lastChunkTime := time.Now()

		for {
			if ctx.Err() != nil {
				return
			}

			_, raw, err := conn.ReadMessage()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error reading Sarvam TTS WebSocket: %v", err)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			var msg SarvamTTSWSServerMessage
			if err := sonic.Unmarshal(raw, &msg); err != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			switch msg.Type {
			case "audio":
				audioBytes, err := base64.StdEncoding.DecodeString(msg.Data.Audio)
				if err != nil {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}
				chunkIndex++
				deltaResponse := &schemas.BifrostSpeechStreamResponse{
					Type:  schemas.SpeechStreamResponseTypeDelta,
					Audio: audioBytes,
					ExtraFields: schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, deltaResponse, nil, nil), responseChan, postHookSpanFinalizer)

			case "error":
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				bifrostErr := providerUtils.NewBifrostOperationError("Sarvam TTS WebSocket error: "+msg.Data.Message, nil)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger, postHookSpanFinalizer)
				return

			case "event":
				if msg.Data.EventType == "final" {
					finalResponse := &schemas.BifrostSpeechStreamResponse{
						Type:  schemas.SpeechStreamResponseTypeDone,
						Audio: []byte{},
						ExtraFields: schemas.BifrostResponseExtraFields{
							ChunkIndex: chunkIndex + 1,
							Latency:    time.Since(startTime).Milliseconds(),
						},
					}
					if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
						providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, configBytes)
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, finalResponse, nil, nil), responseChan, postHookSpanFinalizer)
					return
				}
			}
		}
	}()

	return responseChan, nil
}
