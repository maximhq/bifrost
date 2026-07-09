package sarvam

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToSarvamTranscriptionRequest maps a Bifrost transcription request onto Sarvam's speech-to-text fields.
func ToSarvamTranscriptionRequest(bifrostReq *schemas.BifrostTranscriptionRequest) *SarvamTranscriptionRequest {
	if bifrostReq == nil {
		return nil
	}

	req := &SarvamTranscriptionRequest{
		Model: bifrostReq.Model,
	}

	if bifrostReq.Input != nil {
		req.File = bifrostReq.Input.File
		req.Filename = bifrostReq.Input.Filename
	}

	if bifrostReq.Params == nil {
		return req
	}

	if bifrostReq.Params.Language != nil {
		req.LanguageCode = bifrostReq.Params.Language
	}

	if ep := bifrostReq.Params.ExtraParams; ep != nil {
		if v, ok := schemas.SafeExtractStringPointer(ep["mode"]); ok {
			req.Mode = v
		}
		if v, ok := schemas.SafeExtractStringPointer(ep["language_code"]); ok {
			req.LanguageCode = v
		}
		if v, ok := schemas.SafeExtractStringPointer(ep["input_audio_codec"]); ok {
			req.InputAudioCodec = v
		}
	}

	return req
}

// ToBifrostTranscriptionResponse maps Sarvam's speech-to-text response onto Bifrost's transcription response.
func ToBifrostTranscriptionResponse(sarvamResp *SarvamTranscriptionResponse) *schemas.BifrostTranscriptionResponse {
	if sarvamResp == nil {
		return nil
	}

	response := &schemas.BifrostTranscriptionResponse{
		Text: sarvamResp.Transcript,
	}

	if sarvamResp.LanguageCode != nil && *sarvamResp.LanguageCode != "" {
		response.Language = sarvamResp.LanguageCode
	}

	var maxEnd float64
	var hasEnd bool

	if ts := sarvamResp.Timestamps; ts != nil && len(ts.Words) > 0 {
		words := make([]schemas.TranscriptionWord, 0, len(ts.Words))
		for i, w := range ts.Words {
			tw := schemas.TranscriptionWord{Word: w}
			if i < len(ts.StartTimeSeconds) {
				tw.Start = ts.StartTimeSeconds[i]
			}
			if i < len(ts.EndTimeSeconds) {
				tw.End = ts.EndTimeSeconds[i]
				if !hasEnd || tw.End > maxEnd {
					maxEnd = tw.End
					hasEnd = true
				}
			}
			words = append(words, tw)
		}
		response.Words = words
	}

	if dt := sarvamResp.DiarizedTranscript; dt != nil && len(dt.Entries) > 0 {
		segments := make([]schemas.TranscriptionDiarizedSegment, 0, len(dt.Entries))
		for _, e := range dt.Entries {
			segments = append(segments, schemas.TranscriptionDiarizedSegment{
				Type:    "transcript.text.segment",
				Speaker: e.SpeakerID,
				Start:   e.StartTimeSeconds,
				End:     e.EndTimeSeconds,
				Text:    e.Transcript,
			})
			if !hasEnd || e.EndTimeSeconds > maxEnd {
				maxEnd = e.EndTimeSeconds
				hasEnd = true
			}
		}
		response.DiarizedSegments = segments
	}

	if hasEnd {
		response.Duration = &maxEnd
	}

	return response
}

// Transcription performs a speech-to-text request to Sarvam's API using multipart/form-data.
func (provider *SarvamProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	reqBody := ToSarvamTranscriptionRequest(request)
	if reqBody == nil || len(reqBody.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("transcription file is required", nil)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if bifrostErr := writeSarvamTranscriptionMultipart(writer, reqBody); bifrostErr != nil {
		return nil, bifrostErr
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

	req.SetRequestURI(provider.networkConfig.BaseURL + "/speech-to-text")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(contentType)
	if key.Value.GetValue() != "" {
		req.Header.Set("api-subscription-key", key.Value.GetValue())
	}
	req.SetBody(body.Bytes())

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, nil, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseSarvamError(resp), nil, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	var sarvamResp SarvamTranscriptionResponse
	if err := sonic.Unmarshal(responseBody, &sarvamResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to parse Sarvam speech-to-text response", err)
	}

	response := ToBifrostTranscriptionResponse(&sarvamResp)
	response.ExtraFields = schemas.BifrostResponseExtraFields{
		Latency:                 latency.Milliseconds(),
		ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			rawResponse = string(responseBody)
		}
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// writeSarvamTranscriptionMultipart writes the multipart form for Sarvam's speech-to-text request.
func writeSarvamTranscriptionMultipart(writer *multipart.Writer, reqBody *SarvamTranscriptionRequest) *schemas.BifrostError {
	filename := reqBody.Filename
	if filename == "" {
		filename = providerUtils.AudioFilenameFromBytes(reqBody.File)
	}
	fileWriter, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to create file field", err)
	}
	if _, err := fileWriter.Write(reqBody.File); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write file data", err)
	}

	if reqBody.Model != "" {
		if err := writer.WriteField("model", reqBody.Model); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write model field", err)
		}
	}
	if reqBody.Mode != nil && strings.TrimSpace(*reqBody.Mode) != "" {
		if err := writer.WriteField("mode", *reqBody.Mode); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write mode field", err)
		}
	}
	if reqBody.LanguageCode != nil && strings.TrimSpace(*reqBody.LanguageCode) != "" {
		if err := writer.WriteField("language_code", *reqBody.LanguageCode); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write language_code field", err)
		}
	}
	if reqBody.InputAudioCodec != nil && strings.TrimSpace(*reqBody.InputAudioCodec) != "" {
		if err := writer.WriteField("input_audio_codec", *reqBody.InputAudioCodec); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write input_audio_codec field", err)
		}
	}

	return nil
}
