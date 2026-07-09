package sarvam

import (
	"bytes"
	"context"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Transcription performs a speech-to-text request against Sarvam's
// Saaras/Saarika API (POST /speech-to-text, real-time/sync only - files over
// 30s are rejected with "use the batch API for longer audio files"; Sarvam's
// separate async Batch API is out of scope here). Sarvam's response field
// names and shape diverge from OpenAI's transcriptions endpoint (transcript/
// timestamps/diarized_transcript vs text/words/segments);
// ToBifrostTranscriptionResponse does the mapping, reusing
// schemas.TranscriptionDiarizedSegment (added for OpenAI's diarized_json
// support) for Sarvam's diarization data.
//
// Note: verified live against Sarvam's docs that diarization is a Batch-API-
// only feature ("Diarization is only available in Batch API with separate
// pricing") - the sync REST endpoint used here never populates
// diarized_transcript despite it appearing in the documented response
// schema. The mapping below is therefore correct but currently unverifiable/
// dormant against the real API; it will only activate if Sarvam starts
// returning that field from this endpoint, or when Batch API support is
// added.
func (provider *SarvamProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	if request.Input == nil || len(request.Input.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("a transcription file is required", nil)
	}

	var bodyBuf bytes.Buffer
	writer := multipart.NewWriter(&bodyBuf)

	filename := request.Input.Filename
	if filename == "" {
		filename = providerUtils.AudioFilenameFromBytes(request.Input.File)
	}
	fileWriter, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create file field", err)
	}
	if _, err := fileWriter.Write(request.Input.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file data", err)
	}

	if request.Model != "" {
		if err := writer.WriteField("model", request.Model); err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to write model field", err)
		}
	}

	if request.Params != nil {
		if request.Params.Language != nil && *request.Params.Language != "" {
			if err := writer.WriteField("language_code", normalizeSarvamLanguageCode(*request.Params.Language)); err != nil {
				return nil, providerUtils.NewBifrostOperationError("failed to write language_code field", err)
			}
		}
		if request.Params.ExtraParams != nil {
			// Read-only: nothing else in this function consumes ExtraParams
			// afterward, so there's no need to delete from the caller's map
			// (which would mutate it for any retry/fallback that reuses the
			// same *BifrostTranscriptionRequest).
			if mode, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["mode"]); ok {
				if err := writer.WriteField("mode", *mode); err != nil {
					return nil, providerUtils.NewBifrostOperationError("failed to write mode field", err)
				}
			}
			if codec, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["input_audio_codec"]); ok {
				if err := writer.WriteField("input_audio_codec", *codec); err != nil {
					return nil, providerUtils.NewBifrostOperationError("failed to write input_audio_codec field", err)
				}
			}
		}
	}

	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to finalize multipart transcription request", err)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/speech-to-text"))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(contentType)
	for k, v := range AuthHeaders(key) {
		req.Header.Set(k, v)
	}
	req.SetBody(bodyBuf.Bytes())

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseSarvamError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	var sarvamResp SarvamTranscriptionResponse
	if err := sonic.Unmarshal(body, &sarvamResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to parse Sarvam transcription response", err)
	}

	response := ToBifrostTranscriptionResponse(&sarvamResp)
	response.ExtraFields = schemas.BifrostResponseExtraFields{
		Latency:                 latency.Milliseconds(),
		ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			rawResponse = string(body)
		}
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// normalizeSarvamLanguageCode adapts Bifrost's generic bare ISO-639-1 language
// codes (e.g. "en", matching OpenAI's convention) to Sarvam's required BCP-47
// region-qualified codes (e.g. "en-IN"). Sarvam's only valid codes are
// "unknown" or "<lang>-IN" (hi-IN, ta-IN, en-IN, ...), so a bare code without
// a region is assumed to mean the Indian-region variant; codes that already
// carry a region (contain "-") or are "unknown" pass through unchanged.
func normalizeSarvamLanguageCode(code string) string {
	if code == "unknown" || strings.Contains(code, "-") {
		return code
	}
	return code + "-IN"
}

// ToBifrostTranscriptionResponse maps Sarvam's native transcription response
// into Bifrost's canonical shape.
func ToBifrostTranscriptionResponse(sarvamResp *SarvamTranscriptionResponse) *schemas.BifrostTranscriptionResponse {
	response := &schemas.BifrostTranscriptionResponse{
		Text: sarvamResp.Transcript,
	}

	if sarvamResp.LanguageCode != nil && *sarvamResp.LanguageCode != "" && *sarvamResp.LanguageCode != "unknown" {
		response.Language = sarvamResp.LanguageCode
	}

	if sarvamResp.Timestamps != nil {
		words := sarvamResp.Timestamps.Words
		starts := sarvamResp.Timestamps.StartTimeSeconds
		ends := sarvamResp.Timestamps.EndTimeSeconds
		n := len(words)
		if len(starts) < n {
			n = len(starts)
		}
		if len(ends) < n {
			n = len(ends)
		}
		transcriptionWords := make([]schemas.TranscriptionWord, 0, n)
		for i := 0; i < n; i++ {
			transcriptionWords = append(transcriptionWords, schemas.TranscriptionWord{
				Word:  words[i],
				Start: starts[i],
				End:   ends[i],
			})
		}
		response.Words = transcriptionWords
	}

	if sarvamResp.DiarizedTranscript != nil && len(sarvamResp.DiarizedTranscript.Entries) > 0 {
		segments := make([]schemas.TranscriptionDiarizedSegment, len(sarvamResp.DiarizedTranscript.Entries))
		for i, entry := range sarvamResp.DiarizedTranscript.Entries {
			segments[i] = schemas.TranscriptionDiarizedSegment{
				ID:      strconv.Itoa(i),
				Type:    "transcript.text.segment",
				Speaker: entry.SpeakerID,
				Start:   entry.StartTimeSeconds,
				End:     entry.EndTimeSeconds,
				Text:    entry.Transcript,
			}
		}
		response.DiarizedSegments = segments
	}

	return response
}

// isWAV reports whether data starts with a RIFF/WAVE header.
func isWAV(data []byte) bool {
	return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE"
}

// TranscriptionStream performs a streaming speech-to-text request against
// Sarvam's STT WebSocket (wss://.../speech-to-text/ws), undocumented in
// Sarvam's REST OpenAPI spec but specified in their AsyncAPI spec (same
// source as the TTS WebSocket in speech.go). Unlike the TTS WebSocket,
// connection config is via query params (not a config message), and the
// audio encoding is constrained to WAV only (AudioDataEncoding has a single
// enum value, "audio/wav") - unlike the sync REST /speech-to-text endpoint,
// which accepts many formats. Protocol: connect, send one "audio" message
// with the whole file base64-encoded, send a "flush" signal, then read the
// "data"-type response (same transcript/diarized_transcript shape as the
// REST response). Sarvam's STT WS is designed for continuous audio (no
// separate terminal/"final" event distinct from the data message itself, and
// the connection stays open for more audio after replying) - since this
// method sends the whole file as a single chunk, the first "data" response
// received is treated as complete and the connection is closed proactively.
func (provider *SarvamProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if request.Input == nil || len(request.Input.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("a transcription file is required", nil)
	}
	if !isWAV(request.Input.File) {
		return nil, providerUtils.NewBifrostOperationError("Sarvam's streaming speech-to-text WebSocket only accepts WAV audio (AudioDataEncoding is audio/wav only); use the non-streaming Transcription endpoint for other formats", nil)
	}

	model := "saaras:v3"
	if request.Model != "" {
		model = request.Model
	}

	query := url.Values{}
	query.Set("model", model)
	if request.Params != nil {
		if request.Params.Language != nil && *request.Params.Language != "" {
			query.Set("language-code", normalizeSarvamLanguageCode(*request.Params.Language))
		}
		if request.Params.ExtraParams != nil {
			if mode, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["mode"]); ok {
				query.Set("mode", *mode)
			}
		}
	}

	wsURL := strings.Replace(provider.networkConfig.BaseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/speech-to-text/ws?" + query.Encode()

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

	audioMsg := &SarvamSTTWSAudioMessage{
		Audio: SarvamSTTWSAudioData{
			Data:       base64.StdEncoding.EncodeToString(request.Input.File),
			SampleRate: "16000",
			Encoding:   "audio/wav",
		},
	}
	audioBytes, err := sonic.Marshal(audioMsg)
	if err != nil {
		conn.Close()
		return nil, providerUtils.NewBifrostOperationError("failed to marshal Sarvam STT WebSocket audio message", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, audioBytes); err != nil {
		conn.Close()
		return nil, providerUtils.NewBifrostOperationError("failed to send Sarvam STT WebSocket audio message", err)
	}

	flushBytes, _ := sonic.Marshal(&SarvamSTTWSFlushSignal{Type: "flush"})
	if err := conn.WriteMessage(websocket.TextMessage, flushBytes); err != nil {
		conn.Close()
		return nil, providerUtils.NewBifrostOperationError("failed to send Sarvam STT WebSocket flush signal", err)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	go func() {
		defer conn.Close()
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, audioBytes)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, audioBytes)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)

		for {
			if ctx.Err() != nil {
				return
			}

			_, raw, err := conn.ReadMessage()
			if err != nil {
				// The connection closing before any data arrived (e.g. upstream
				// hangup) - surface as an error rather than a silent empty done.
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Sarvam STT WebSocket closed before a transcript was received: %v", err)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			var msg SarvamSTTWSServerMessage
			if err := sonic.Unmarshal(raw, &msg); err != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			switch msg.Type {
			case "data":
				// This provider sends the whole file as a single audio message
				// followed by flush, so the first "data" response is the complete
				// transcript for this request - Sarvam's STT WS has no separate
				// terminal/"final" event and the connection otherwise stays open
				// for further audio, so close proactively here instead of waiting
				// for the server to hang up.
				deltaResponse := &schemas.BifrostTranscriptionStreamResponse{
					Type:  schemas.TranscriptionStreamResponseTypeDelta,
					Delta: &msg.Data.Transcript,
					Text:  msg.Data.Transcript,
					ExtraFields: schemas.BifrostResponseExtraFields{
						Latency: time.Since(startTime).Milliseconds(),
					},
				}
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, deltaResponse, nil), responseChan, postHookSpanFinalizer)

				doneResponse := &schemas.BifrostTranscriptionStreamResponse{
					Type: schemas.TranscriptionStreamResponseTypeDone,
					Text: msg.Data.Transcript,
					ExtraFields: schemas.BifrostResponseExtraFields{
						Latency: time.Since(startTime).Milliseconds(),
					},
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, doneResponse, nil), responseChan, postHookSpanFinalizer)
				return

			case "error":
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				bifrostErr := providerUtils.NewBifrostOperationError("Sarvam STT WebSocket error: "+msg.Data.Error, nil)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}
		}
	}()

	return responseChan, nil
}
