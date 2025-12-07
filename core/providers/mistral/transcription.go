// Package mistral implements transcription support for Mistral's audio API.
package mistral

import (
	"bytes"
	"mime/multipart"
	"strconv"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToMistralTranscriptionRequest converts a Bifrost transcription request to Mistral format.
func ToMistralTranscriptionRequest(bifrostReq *schemas.BifrostTranscriptionRequest) *MistralTranscriptionRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || len(bifrostReq.Input.File) == 0 {
		return nil
	}

	req := &MistralTranscriptionRequest{
		Model: bifrostReq.Model,
		File:  bifrostReq.Input.File,
	}

	if bifrostReq.Params != nil {
		req.Language = bifrostReq.Params.Language
		req.Prompt = bifrostReq.Params.Prompt
		req.ResponseFormat = bifrostReq.Params.ResponseFormat

		// Handle extra params for Mistral-specific fields
		if bifrostReq.Params.ExtraParams != nil {
			if temp, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["temperature"]); ok {
				req.Temperature = temp
			}
			if granularities, ok := bifrostReq.Params.ExtraParams["timestamp_granularities"].([]string); ok {
				req.TimestampGranularities = granularities
			}
		}
	}

	return req
}

// ToBifrostTranscriptionResponse converts a Mistral transcription response to Bifrost format.
func (r *MistralTranscriptionResponse) ToBifrostTranscriptionResponse() *schemas.BifrostTranscriptionResponse {
	if r == nil {
		return nil
	}

	response := &schemas.BifrostTranscriptionResponse{
		Text:     r.Text,
		Duration: r.Duration,
		Language: r.Language,
		Task:     schemas.Ptr("transcribe"),
	}

	// Convert segments
	if len(r.Segments) > 0 {
		response.Segments = make([]schemas.TranscriptionSegment, len(r.Segments))
		for i, seg := range r.Segments {
			response.Segments[i] = schemas.TranscriptionSegment{
				ID:               seg.ID,
				Seek:             seg.Seek,
				Start:            seg.Start,
				End:              seg.End,
				Text:             seg.Text,
				Tokens:           seg.Tokens,
				Temperature:      seg.Temperature,
				AvgLogProb:       seg.AvgLogProb,
				CompressionRatio: seg.CompressionRatio,
				NoSpeechProb:     seg.NoSpeechProb,
			}
		}
	}

	// Convert words
	if len(r.Words) > 0 {
		response.Words = make([]schemas.TranscriptionWord, len(r.Words))
		for i, word := range r.Words {
			response.Words[i] = schemas.TranscriptionWord{
				Word:  word.Word,
				Start: word.Start,
				End:   word.End,
			}
		}
	}

	return response
}

// parseMistralTranscriptionFormData writes the transcription request to a multipart form.
func parseMistralTranscriptionFormData(writer *multipart.Writer, req *MistralTranscriptionRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Add file field - Mistral uses "file" as the form field name
	fileWriter, err := writer.CreateFormFile("file", "audio.mp3")
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to create form file", err, providerName)
	}
	if _, err := fileWriter.Write(req.File); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write file data", err, providerName)
	}

	// Add model field (required)
	if err := writer.WriteField("model", req.Model); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write model field", err, providerName)
	}

	// Add optional fields
	if req.Language != nil {
		if err := writer.WriteField("language", *req.Language); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write language field", err, providerName)
		}
	}

	if req.Prompt != nil {
		if err := writer.WriteField("prompt", *req.Prompt); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write prompt field", err, providerName)
		}
	}

	if req.ResponseFormat != nil {
		if err := writer.WriteField("response_format", *req.ResponseFormat); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write response_format field", err, providerName)
		}
	}

	if req.Temperature != nil {
		if err := writer.WriteField("temperature", formatFloat64(*req.Temperature)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write temperature field", err, providerName)
		}
	}

	// Close the multipart writer to finalize the form
	if err := writer.Close(); err != nil {
		return providerUtils.NewBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	return nil
}

// createMistralTranscriptionMultipartBody creates the multipart form body for a transcription request.
func createMistralTranscriptionMultipartBody(req *MistralTranscriptionRequest, providerName schemas.ModelProvider) (*bytes.Buffer, string, *schemas.BifrostError) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := parseMistralTranscriptionFormData(writer, req, providerName); err != nil {
		return nil, "", err
	}

	return &body, writer.FormDataContentType(), nil
}

// formatFloat64 converts a float64 to string for form fields.
func formatFloat64(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

