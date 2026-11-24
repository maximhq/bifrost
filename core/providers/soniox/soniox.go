// Package soniox implements the Soniox provider (speech-to-text and text-to-speech).
package soniox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// SonioxProvider implements the Provider interface for Soniox's API.
type SonioxProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	ttsBaseURL          string                // Text-to-speech host, derived from the REST base URL
	realtimeURL         string                // Real-time transcription WebSocket URL, derived from the REST base URL
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewSonioxProvider creates a new Soniox provider instance.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewSonioxProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*SonioxProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	// Configure proxy and retry policy
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)
	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = defaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	ttsBaseURL, err := deriveTTSBaseURL(config.NetworkConfig.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid soniox base_url: %w", err)
	}
	realtimeURL, err := deriveRealtimeURL(config.NetworkConfig.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid soniox base_url: %w", err)
	}

	return &SonioxProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		ttsBaseURL:          ttsBaseURL,
		realtimeURL:         realtimeURL,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Soniox.
func (provider *SonioxProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Soniox
}

// doRequest performs one unary Soniox call and returns the decoded body of a 2xx response.
// Non-2xx responses come back as a parsed BifrostError.
func (provider *SonioxProvider) doRequest(ctx *schemas.BifrostContext, key schemas.Key, method, requestURL, contentType string, body []byte) ([]byte, map[string]string, time.Duration, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(method)
	if contentType != "" {
		req.Header.SetContentType(contentType)
	}
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	if len(body) > 0 {
		req.SetBody(body)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, nil, latency, bifrostErr
	}
	// Extract and set provider response headers so they're available on error paths
	headers := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	if resp.StatusCode() < fasthttp.StatusOK || resp.StatusCode() >= fasthttp.StatusMultipleChoices {
		return nil, headers, latency, providerUtils.SetErrorLatency(parseSonioxError(resp), latency)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, headers, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}
	return responseBody, headers, latency, nil
}

// listModelsByKey lists Soniox's speech-to-text and text-to-speech models for a single key.
func (provider *SonioxProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	sttBody, headers, sttLatency, bifrostErr := provider.doRequest(ctx, key, http.MethodGet, provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/models"), "application/json", nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	ttsBody, _, ttsLatency, bifrostErr := provider.doRequest(ctx, key, http.MethodGet, provider.networkConfig.BaseURL+"/v1/tts-models", "application/json", nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var sttModels, ttsModels SonioxListModelsResponse
	if err := sonic.Unmarshal(sttBody, &sttModels); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	if err := sonic.Unmarshal(ttsBody, &ttsModels); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	combined := SonioxListModelsResponse{Models: append(sttModels.Models, ttsModels.Models...)}

	response := combined.ToBifrostListModelsResponse(provider.GetProviderKey(), key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)
	response.ExtraFields.Latency = (sttLatency + ttsLatency).Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = headers

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawSTT, rawTTS interface{}
		_ = sonic.Unmarshal(sttBody, &rawSTT)
		_ = sonic.Unmarshal(ttsBody, &rawTTS)
		response.ExtraFields.RawResponse = map[string]interface{}{"models": rawSTT, "tts_models": rawTTS}
	}

	return response, nil
}

// ListModels performs a list models request to Soniox's API.
// Requests are made concurrently for improved performance.
func (provider *SonioxProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

// Speech performs a text to speech request.
func (provider *SonioxProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*SonioxSpeechRequest, error) {
			return ToSonioxSpeechRequest(request), nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.ttsBaseURL + providerUtils.GetPathFromContext(ctx, "/tts"))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	if !providerUtils.ApplyLargePayloadRequestBody(ctx, req) {
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	// Extract and set provider response headers so they're available on error paths
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseSonioxError(resp), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	ft, fh := providerUtils.StartPhaseSpan(ctx, "response-finalize")
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if ft != nil {
		if err != nil {
			ft.EndSpan(fh, schemas.SpanStatusError, err.Error())
		} else {
			ft.EndSpan(fh, schemas.SpanStatusOk, "")
		}
	}
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	bifrostResponse := &schemas.BifrostSpeechResponse{
		Audio: body,
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

// SpeechStream performs a text to speech stream request. Soniox streams the audio as the
// chunked body of the same POST /tts call, so chunks are forwarded as they arrive.
func (provider *SonioxProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*SonioxSpeechRequest, error) {
			return ToSonioxSpeechRequest(request), nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.ttsBaseURL + providerUtils.GetPathFromContext(ctx, "/tts"))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	if !providerUtils.ApplyLargePayloadRequestBody(ctx, req) {
		req.SetBody(jsonBody)
	}

	startTime := time.Now()
	err := providerUtils.DoStreamingRequest(ctx, provider.streamingClient, req, resp)
	latency := time.Since(startTime)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, contextDoneError(err), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		// Request failed before the first response byte: surface a retriable upstream connection
		// error so executeRequestWithRetries honors max_retries, as the non-streaming path does.
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Extract provider response headers before status check so error responses also forward them
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, parseSonioxError(resp), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.RecoverStreamPanic(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer)
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		// Decompress gzip-encoded streams transparently (no-op for non-gzip)
		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()

		// Wrap reader with idle timeout to detect stalled streams.
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		// Close the raw network stream on ctx cancellation, which unblocks any in-progress read.
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)

		buffer := make([]byte, 4096)
		chunkIndex := -1
		lastChunkTime := time.Now()

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			n, err := reader.Read(buffer)

			// A reader may return its final bytes together with io.EOF, so forward the data
			// before looking at the error.
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

				if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
					response.ExtraFields.RawResponse = audioChunk
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, response, nil, nil), responseChan, postHookSpanFinalizer)
			}

			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if err == io.EOF {
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error reading stream: %v", err)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}
		}

		finalResponse := &schemas.BifrostSpeechStreamResponse{
			Type:  schemas.SpeechStreamResponseTypeDone,
			Audio: []byte{},
			ExtraFields: schemas.BifrostResponseExtraFields{
				ChunkIndex: chunkIndex + 1,
				Latency:    time.Since(startTime).Milliseconds(),
			},
		}
		finalResponse.BackfillParams(request)
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonBody)
		}
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, finalResponse, nil, nil), responseChan, postHookSpanFinalizer)
	}()

	return responseChan, nil
}

// Transcription performs a speech-to-text request. Soniox transcribes asynchronously, so one
// call uploads the audio, creates the job, polls it to completion, and reads the transcript. A
// finished job and the uploaded file are then deleted in the background.
func (provider *SonioxProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	ct, ch := providerUtils.StartPhaseSpan(ctx, "convertor")
	reqBody := ToSonioxTranscriptionRequest(request)
	if ct != nil {
		if reqBody == nil {
			ct.EndSpan(ch, schemas.SpanStatusError, "transcription request is not provided")
		} else {
			ct.EndSpan(ch, schemas.SpanStatusOk, "")
		}
	}
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription request is not provided", nil)
	}
	if len(reqBody.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("a transcription file must be provided", nil)
	}

	startTime := time.Now()
	apiURL := provider.networkConfig.BaseURL

	// 1. Upload the audio.
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	filename := reqBody.Filename
	if filename == "" {
		filename = providerUtils.AudioFilenameFromBytes(reqBody.File)
	}
	fileWriter, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create file field", err)
	}
	if _, err := fileWriter.Write(reqBody.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file data", err)
	}
	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to finalize multipart transcription request", err)
	}

	fileBody, _, _, bifrostErr := provider.doRequest(ctx, key, http.MethodPost, apiURL+"/v1/files", contentType, upload.Bytes())
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	var file SonioxFile
	if err := sonic.Unmarshal(fileBody, &file); err != nil || file.ID == "" {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	var job SonioxTranscription
	jobUnknown := false // Soniox may hold a job this call never heard of
	defer func() {
		// Soniox cannot delete a job it is still processing, so an unfinished job and its file are left to its 30-day expiry.
		if jobUnknown {
			return
		}
		if job.ID == "" || job.Status == "completed" || job.Status == "error" {
			provider.deleteInBackground(key, job.ID, file.ID)
		}
	}()

	// 2. Create the transcription job.
	reqBody.FileID = file.ID
	jobRequest, err := providerUtils.MarshalProviderRequest(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	if passthrough, _ := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams).(bool); passthrough && len(reqBody.ExtraParams) > 0 {
		jobRequest, err = providerUtils.MergeExtraParamsIntoJSON(jobRequest, reqBody.ExtraParams)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
		}
	}
	jobBody, _, _, bifrostErr := provider.doRequest(ctx, key, http.MethodPost, apiURL+"/v1/transcriptions", "application/json", jobRequest)
	if bifrostErr != nil {
		// A lost connection, a timeout or a cancel leaves the job's fate unknown; only Soniox's own answer refuses it.
		jobUnknown = bifrostErr.StatusCode == nil
		if errField := bifrostErr.Error; errField != nil && errField.Type != nil {
			jobUnknown = jobUnknown || *errField.Type == schemas.ProviderConnectionFailed || *errField.Type == schemas.RequestTimedOut || *errField.Type == schemas.RequestCancelled
		}
		return nil, bifrostErr
	}
	if err := sonic.Unmarshal(jobBody, &job); err != nil || job.ID == "" {
		jobUnknown = true
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}

	// 3. Poll the job until it finishes, for at most the provider's request timeout.
	interval := transcriptionPollInitial
	maxWait := time.Duration(provider.networkConfig.DefaultRequestTimeoutInSeconds) * time.Second
	deadline := time.Now().Add(maxWait)
	for job.Status != "completed" {
		if job.Status == "error" {
			return nil, parseSonioxJobError(&job)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, providerUtils.NewBifrostTimeoutError(fmt.Sprintf("soniox transcription %s did not finish within %s", job.ID, maxWait), nil)
		}
		select {
		case <-ctx.Done():
			return nil, contextDoneError(ctx.Err())
		case <-time.After(min(interval, remaining)):
		}
		interval = min(interval*3/2, transcriptionPollMax)

		jobBody, _, _, bifrostErr = provider.doRequest(ctx, key, http.MethodGet, apiURL+"/v1/transcriptions/"+job.ID, "", nil)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		if err := sonic.Unmarshal(jobBody, &job); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
		}
	}

	// 4. Read the transcript.
	transcriptBody, headers, _, bifrostErr := provider.doRequest(ctx, key, http.MethodGet, apiURL+"/v1/transcriptions/"+job.ID+"/transcript", "", nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	var transcript SonioxTranscript
	pt, ph := providerUtils.StartResponseParseSpan(ctx)
	err = sonic.Unmarshal(transcriptBody, &transcript)
	if pt != nil {
		if err != nil {
			pt.EndSpan(ph, schemas.SpanStatusError, "response parse failed")
		} else {
			pt.EndSpan(ph, schemas.SpanStatusOk, "")
		}
	}
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}

	response := ToBifrostTranscriptionResponse(&transcript, job.AudioDurationMs)
	response.ExtraFields = schemas.BifrostResponseExtraFields{
		Latency:                 time.Since(startTime).Milliseconds(),
		ProviderResponseHeaders: headers,
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(transcriptBody, &rawResponse); err != nil {
			rawResponse = string(transcriptBody)
		}
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// deleteInBackground removes a finished job and then its uploaded file after the response, so the
// caller does not wait for the two DELETE round trips. An empty jobID means no job was created.
func (provider *SonioxProvider) deleteInBackground(key schemas.Key, jobID, fileID string) {
	go func() {
		if jobID != "" {
			provider.deleteResource(key, "/v1/transcriptions/"+jobID)
		}
		provider.deleteResource(key, "/v1/files/"+fileID)
	}()
}

// deleteResource removes an uploaded file or a finished job from Soniox. It runs on its own
// context so cleanup still happens after the request was cancelled or timed out; a failure
// is logged and does not fail the request.
func (provider *SonioxProvider) deleteResource(key schemas.Key, path string) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(cleanupTimeout))
	defer ctx.Cancel()
	if _, _, _, bifrostErr := provider.doRequest(ctx, key, http.MethodDelete, provider.networkConfig.BaseURL+path, "", nil); bifrostErr != nil {
		provider.logger.Warn("failed to delete soniox resource %s: %s", path, bifrostErr.GetErrorString())
	}
}

// contextDoneError maps a finished request context onto a BifrostError.
func contextDoneError(err error) *schemas.BifrostError {
	if errors.Is(err, context.Canceled) {
		return &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Type:    new(schemas.RequestCancelled),
				Message: schemas.ErrRequestCancelled,
				Error:   err,
			},
		}
	}
	return providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err)
}

// TextCompletion is not supported by the Soniox provider
func (provider *SonioxProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Soniox provider
func (provider *SonioxProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion is not supported by the Soniox provider
func (provider *SonioxProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

// ChatCompletionStream is not supported by the Soniox provider
func (provider *SonioxProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}

// Responses is not supported by the Soniox provider
func (provider *SonioxProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, provider.GetProviderKey())
}

// ResponsesStream is not supported by the Soniox provider
func (provider *SonioxProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Soniox provider.
func (provider *SonioxProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, input *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Rerank is not supported by the Soniox provider.
func (provider *SonioxProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// Decision is not supported by the Soniox provider.
func (provider *SonioxProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, provider.GetProviderKey())
}

// OCR is not supported by the Soniox provider.
func (provider *SonioxProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Soniox provider
func (provider *SonioxProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Soniox provider.
func (provider *SonioxProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Soniox provider.
func (provider *SonioxProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the Soniox provider.
func (provider *SonioxProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Soniox provider.
func (provider *SonioxProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Soniox provider.
func (provider *SonioxProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration is not supported by the Soniox provider.
func (provider *SonioxProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the Soniox provider.
func (provider *SonioxProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the Soniox provider.
func (provider *SonioxProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by Soniox provider.
func (provider *SonioxProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by Soniox provider.
func (provider *SonioxProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoEdit is not supported by the Soniox provider.
func (provider *SonioxProvider) VideoEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoEditRequest) (*schemas.BifrostVideoEditResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoEditRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by Soniox provider.
func (provider *SonioxProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by Soniox provider.
func (provider *SonioxProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by Soniox provider.
func (provider *SonioxProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by Soniox provider.
func (provider *SonioxProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by Soniox provider.
func (provider *SonioxProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by Soniox provider.
func (provider *SonioxProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by Soniox provider.
func (provider *SonioxProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by Soniox provider.
func (provider *SonioxProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by Soniox provider.
func (provider *SonioxProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by Soniox provider.
func (provider *SonioxProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by Soniox provider.
func (provider *SonioxProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by Soniox provider.
func (provider *SonioxProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the Soniox provider.
func (provider *SonioxProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ModelRetrieve is not supported by the Soniox provider.
func (provider *SonioxProvider) ModelRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostModelRetrieveRequest) (*schemas.BifrostModelRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ModelRetrieveRequest, provider.GetProviderKey())
}

// Compaction is not supported by the Soniox provider.
func (provider *SonioxProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Soniox provider.
func (provider *SonioxProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the Soniox provider.
func (provider *SonioxProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

func (provider *SonioxProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
